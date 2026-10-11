package main

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSupervisor_SIGTERM_EndsCommandProcessGroup(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "group")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	cfg := productionConfig(os.Getenv)
	cfg.workDir = filepath.Join(t.TempDir(), "work")
	cfg.command = helperCommand("group", fifo)
	s := newSupervisor(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- serve(context.Background(), listener, s, workspaceYAML(t, sampleFiles...)) }()

	// The command and its child both hold the FIFO's write end, so reading it reaches EOF
	// only once every process in the command's group has ended.
	reader, err := os.Open(fifo)
	if err != nil {
		t.Fatalf("open fifo: %v", err)
	}
	defer reader.Close()
	lines := bufio.NewReader(reader)
	if line, err := lines.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("command group did not report ready: %q, %v", line, err)
	}
	healthz, err := http.Get("http://" + listener.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	_ = healthz.Body.Close()
	if healthz.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", healthz.StatusCode)
	}

	// serve has registered for SIGTERM before it started the command, so the signal
	// reaches the supervisor rather than ending the test process.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	if rest, err := io.ReadAll(lines); err != nil || len(rest) != 0 {
		t.Fatalf("fifo after SIGTERM = %q, %v; want EOF once the group has ended", rest, err)
	}
	if err := <-served; err != nil {
		t.Fatalf("serve: %v", err)
	}
	result, _ := doneResultOf(t, s)
	if result.ExitCode != -1 {
		t.Fatalf("exit code = %d, want -1 for a command ended by a signal", result.ExitCode)
	}
}
