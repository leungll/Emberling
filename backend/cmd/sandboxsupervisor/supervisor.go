package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// workspaceInvalidReason is the only failure the result reports: the Workspace could not be
// accepted or written, so no command ran.
const workspaceInvalidReason = "WORKSPACE_INVALID"

// outputDrainTimeout bounds how long the supervisor waits for the command's output pipe
// to close after the command has exited or been ended, in case a process outside the
// command's group still holds it.
const outputDrainTimeout = 10 * time.Second

// phase is where the supervisor is in its single run.
type phase int

const (
	// phasePreparing: the workspace is being written or the command is not started yet.
	phasePreparing phase = iota
	// phaseInvalid: the workspace was refused; nothing runs, ever.
	phaseInvalid
	// phaseRunning: the command has started and not exited.
	phaseRunning
	// phaseDone: the command has exited and the result is fixed.
	phaseDone
)

// config is the fixed setup of one supervisor. Only tests change it.
type config struct {
	// workDir is where the workspace is written and the command runs.
	workDir string
	// command is the test command and its arguments.
	command []string
	// env is the command's complete environment; nothing else is inherited.
	env []string
}

// supervisor writes one workspace, runs one test command in it and reports the result.
// It holds no credential and computes only the digest of the tree it wrote.
type supervisor struct {
	cfg    config
	logger *slog.Logger
	// beforeStart, when set, runs after the workspace is written and before the command
	// starts. Tests use it to hold the window in which the supervisor must not be ready.
	beforeStart func()
	// finished is closed when run returns.
	finished chan struct{}

	mu     sync.Mutex
	phase  phase
	result []byte
}

func newSupervisor(cfg config, logger *slog.Logger) *supervisor {
	return &supervisor{cfg: cfg, logger: logger, finished: make(chan struct{})}
}

// run writes the workspace described by workspaces and runs the command to completion.
// An invalid workspace runs nothing. Cancelling ctx ends the command's whole process
// group; the command is then reported as ended by a signal.
func (s *supervisor) run(ctx context.Context, workspaces string) {
	defer close(s.finished)
	files, err := parseWorkspace(workspaces)
	var tree map[string][]byte
	if err == nil {
		tree, err = writeWorkspace(s.cfg.workDir, files)
	}
	if err != nil {
		s.logger.Error("workspace refused; the test command will not run", "error", err)
		s.setPhase(phaseInvalid)
		return
	}
	digest := workspaceDigest(tree)
	if s.beforeStart != nil {
		s.beforeStart()
	}
	if ctx.Err() != nil {
		return
	}

	output := &boundedOutput{limit: maxTestOutputBytes}
	cmd := exec.CommandContext(ctx, s.cfg.command[0], s.cfg.command[1:]...) //nolint:gosec // G204: the command is the fixed go test invocation from productionConfig, never workspace text, and no shell is involved
	cmd.Dir = s.cfg.workDir
	cmd.Env = s.cfg.env
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return endProcessGroup(cmd.Process.Pid) }
	cmd.WaitDelay = outputDrainTimeout
	if err := cmd.Start(); err != nil {
		// Without a started command there is nothing to report; the supervisor stays
		// unready, which the runner sees as a sandbox that never became ready.
		s.logger.Error("test command could not start", "error", err)
		return
	}
	s.setPhase(phaseRunning)
	waitErr := cmd.Wait()
	// The group outlives its leader when a test leaves processes behind; none may keep
	// running once the result is published.
	if err := endProcessGroup(cmd.Process.Pid); err != nil && !errors.Is(err, syscall.ESRCH) {
		s.logger.Warn("could not end the test command's process group", "error", err)
	}
	// ExitCode is -1 when a signal ended the command.
	exitCode := cmd.ProcessState.ExitCode()
	s.logger.Info("test command exited", "exit_code", exitCode, "wait_error", errorText(waitErr))

	text, truncated := reportedOutput(output.data, output.dropped)
	body := encodeResult(resultBody{
		State:           "done",
		ExitCode:        &exitCode,
		WorkspaceDigest: digest,
		Output:          &text,
		OutputTruncated: &truncated,
	})
	s.mu.Lock()
	s.phase = phaseDone
	s.result = body
	s.mu.Unlock()
}

// endProcessGroup kills every process in the group the command leads. The command runs
// in its own group, so this reaches the processes it started without touching the
// supervisor.
func endProcessGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *supervisor) setPhase(p phase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = p
}

// snapshot returns the current phase and, once done, the encoded result.
func (s *supervisor) snapshot() (phase, []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase, s.result
}

// resultBody is the result endpoint's response. Unset fields are omitted, which yields
// exactly the three forms the runner accepts: running, done and failed.
type resultBody struct {
	State           string  `json:"state"`
	ExitCode        *int    `json:"exitCode,omitempty"`
	WorkspaceDigest string  `json:"workspaceDigest,omitempty"`
	Output          *string `json:"output,omitempty"`
	OutputTruncated *bool   `json:"outputTruncated,omitempty"`
	Reason          string  `json:"reason,omitempty"`
}

// encodeResult encodes body with the encoder whose string length bounds the output.
func encodeResult(body resultBody) []byte {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	// The body holds only strings, integers and booleans, which always encode.
	_ = encoder.Encode(body)
	return buf.Bytes()
}
