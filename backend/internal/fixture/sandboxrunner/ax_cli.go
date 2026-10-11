package sandboxrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"slices"
	"strings"
)

// The real AX control below drives the ax CLI with the smallest set of assumptions about
// its interface. Its flags, its output formats and its answer for a missing resource were
// read from the AX source and documentation but are still to be confirmed against a
// running AX: every behaviour test of the ax Backend runs against the deterministic
// stand-in instead.

// maxAXOutputBytes bounds what the runner keeps of one ax CLI call's output.
const maxAXOutputBytes = 1 << 20

// atenetTargetHeader selects the actor the AX router forwards a request to, as
// "<atespace>/<task>".
const atenetTargetHeader = "ate-target-actor"

// AXCommandRunner runs the ax CLI. The real AX control reaches AX only through it, so
// tests can stand in for the CLI.
type AXCommandRunner interface {
	// AX runs `ax args...` with stdin, writing combined output to output, and returns its
	// exit code. err is non-nil only when the command could not be run or did not exit on
	// its own, for example because ctx ended.
	AX(ctx context.Context, args []string, stdin io.Reader, output io.Writer) (int, error)
}

// ExecAXRunner runs the ax CLI at Path. The CLI finds the control plane through the kube
// context of the runner's environment; that access never leaves the runner process.
type ExecAXRunner struct {
	Path string
}

// AX implements AXCommandRunner.
func (r ExecAXRunner) AX(ctx context.Context, args []string, stdin io.Reader, output io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, r.Path, args...) //nolint:gosec // G204: the binary is the operator-configured ax CLI and args are the backend's own fixed lists with derived names, never request text, and no shell is involved
	cmd.Stdin = stdin
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// AXCLI is the AXTaskControl that drives the ax CLI.
type AXCLI struct {
	runner AXCommandRunner
}

// NewAXCLI returns the ax CLI control running commands through runner.
func NewAXCLI(runner AXCommandRunner) *AXCLI {
	return &AXCLI{runner: runner}
}

// run runs one ax command. Errors name the subcommand and exit status only: CLI output
// may describe the cluster and is never copied into an error.
func (c *AXCLI) run(ctx context.Context, args []string, stdin io.Reader) (int, []byte, error) {
	output := &boundedBuffer{limit: maxAXOutputBytes}
	code, err := c.runner.AX(ctx, args, stdin, output)
	if err != nil {
		return code, output.data, fmt.Errorf("ax %s could not be run: %w", args[0], err)
	}
	return code, output.data, nil
}

// Apply implements AXTaskControl with `ax apply -f -`, reading the documents from stdin.
func (c *AXCLI) Apply(ctx context.Context, atespace string, documents []byte) error {
	code, _, err := c.run(ctx, []string{"apply", "--atespace", atespace, "-f", "-"}, bytes.NewReader(documents))
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("ax apply exited with status %d", code)
	}
	return nil
}

// TaskState implements AXTaskControl with `ax get task <name>`, which prints the Task as
// YAML.
func (c *AXCLI) TaskState(ctx context.Context, atespace, name string) (string, error) {
	code, output, err := c.run(ctx, []string{"get", "task", name, "--atespace", atespace}, nil)
	if err != nil {
		return "", err
	}
	if code != 0 {
		if reportsNotFound(output) {
			return AXTaskMissing, nil
		}
		return "", fmt.Errorf("ax get task exited with status %d", code)
	}
	return parseAXTaskState(output), nil
}

// Delete implements AXTaskControl by deleting the Task, then its Workspace.
func (c *AXCLI) Delete(ctx context.Context, atespace, name string) error {
	for _, kind := range []string{"task", "workspace"} {
		code, output, err := c.run(ctx, []string{"delete", kind, name, "--atespace", atespace}, nil)
		if err != nil {
			return err
		}
		if code != 0 && !reportsNotFound(output) {
			return fmt.Errorf("ax delete %s exited with status %d", kind, code)
		}
	}
	return nil
}

// List implements AXTaskControl from the `ax get tasks` and `ax get workspaces` tables,
// whose first column is the name.
func (c *AXCLI) List(ctx context.Context, atespace, prefix string) ([]string, error) {
	var names []string
	for _, kind := range []string{"tasks", "workspaces"} {
		code, output, err := c.run(ctx, []string{"get", kind, "--atespace", atespace}, nil)
		if err != nil {
			return nil, err
		}
		if code != 0 {
			return nil, fmt.Errorf("ax get %s exited with status %d", kind, code)
		}
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 || fields[0] == "NAME" || !strings.HasPrefix(fields[0], prefix) {
				continue
			}
			names = append(names, fields[0])
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// reportsNotFound reports whether CLI output says the resource does not exist.
func reportsNotFound(output []byte) bool {
	lower := strings.ToLower(string(output))
	return strings.Contains(lower, "not found") || strings.Contains(lower, "notfound")
}

// parseAXTaskState reads status.phase and the Ready condition from a Task printed as
// YAML. It understands only the block layout the CLI prints and maps any phase it does not
// recognise to pending, which the ready wait bounds.
func parseAXTaskState(output []byte) string {
	var phase string
	ready := false
	inStatus := false
	var conditionType, conditionStatus string
	flush := func() {
		if conditionType == "Ready" && conditionStatus == "True" {
			ready = true
		}
		conditionType, conditionStatus = "", ""
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "-") {
			flush()
			inStatus = strings.TrimSpace(line) == "status:"
			continue
		}
		if !inStatus {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") {
			flush()
			trimmed = strings.TrimPrefix(trimmed, "- ")
		}
		key, value, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch key {
		case "phase":
			phase = value
		case "type":
			conditionType = value
		case "status":
			conditionStatus = value
		}
	}
	flush()
	lowerPhase := strings.ToLower(phase)
	switch {
	case strings.Contains(lowerPhase, "fail"):
		return AXTaskFailed
	case strings.Contains(lowerPhase, "suspend"):
		return AXTaskSuspended
	case ready:
		return AXTaskReady
	default:
		return AXTaskPending
	}
}

// AXRouterReader reads the supervisor result through the AX router's HTTP address.
type AXRouterReader struct {
	baseURL string
	client  *http.Client
}

// NewAXRouterReader returns a reader for the router at baseURL. The caller bounds each
// request through its context.
func NewAXRouterReader(baseURL string, client *http.Client) *AXRouterReader {
	return &AXRouterReader{baseURL: strings.TrimSuffix(baseURL, "/"), client: client}
}

// ReadResult implements AXResultReader with one GET /v1/result routed to the Task's
// actor.
func (r *AXRouterReader) ReadResult(ctx context.Context, atespace, taskName string) (int, io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/v1/result", nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set(atenetTargetHeader, atespace+"/"+taskName)
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, resp.Body, nil
}
