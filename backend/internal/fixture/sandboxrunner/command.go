package sandboxrunner

import (
	"context"
	"errors"
	"io"
	"os/exec"
)

// CommandRunner runs the docker CLI. The docker Backend reaches the Docker daemon only
// through it, so tests can stand in for the daemon.
type CommandRunner interface {
	// Docker runs `docker args...` with stdin, writing combined output to output, and
	// returns its exit code. err is non-nil only when the command could not be run or did
	// not exit on its own, for example because ctx ended.
	Docker(ctx context.Context, args []string, stdin io.Reader, output io.Writer) (int, error)
}

// ExecRunner runs the docker CLI found on PATH.
type ExecRunner struct{}

// Docker implements CommandRunner.
func (ExecRunner) Docker(ctx context.Context, args []string, stdin io.Reader, output io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // G204: the binary is fixed and args are the backend's own fixed lists, never request text, and no shell is involved
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

// boundedBuffer keeps the first limit bytes written to it and discards the rest, so a
// test's output cannot grow the runner's memory without bound.
type boundedBuffer struct {
	limit int
	data  []byte
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - len(b.data); room > 0 {
		if len(p) <= room {
			b.data = append(b.data, p...)
		} else {
			b.data = append(b.data, p[:room]...)
		}
	}
	return len(p), nil
}
