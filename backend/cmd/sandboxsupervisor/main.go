// Command sandboxsupervisor is the entry point of the sandbox image used by the runner's AX
// backend, installed as /usr/local/bin/ax-task-runner. It writes the one Workspace the
// platform injects through AX_WORKSPACES_YAML into /work, runs the same test command with
// the same fixed environment as the docker backend, and serves the result read-only on
// port 80. It holds no credential and imports no Emberling package.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	// listenAddr is the port the platform probes and routes requests to.
	listenAddr = ":80"
	// workDir is the workspace directory, the same path the docker backend uses.
	workDir = "/work"
	// readHeaderTimeout bounds how long a client may take to send request headers.
	readHeaderTimeout = 10 * time.Second
	// shutdownTimeout bounds closing the HTTP server after the command has ended.
	shutdownTimeout = 5 * time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	// All interfaces: the platform's router reaches the sandbox over its network interface.
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", listenAddr)
	if err != nil {
		logger.Error("listen", "addr", listenAddr, "error", err)
		os.Exit(1)
	}
	s := newSupervisor(productionConfig(os.Getenv), logger)
	if err := serve(context.Background(), listener, s, os.Getenv("AX_WORKSPACES_YAML")); err != nil {
		logger.Error("serve", "error", err)
		os.Exit(1)
	}
}

// productionConfig is the supervisor's fixed setup. The command and its environment match
// the docker backend's container exactly, so a test behaves the same on either backend:
// the nine variables the docker backend passes plus the image's PATH, and nothing else.
// In particular no AX_ variable, which carries the Task and Workspace specifications,
// reaches the tests.
func productionConfig(getenv func(string) string) config {
	return config{
		workDir: workDir,
		command: []string{"go", "test", "-json", "./..."},
		env: []string{
			"HOME=/tmp",
			"GOCACHE=/tmp/gocache",
			"GOPATH=/tmp/gopath",
			"GOTMPDIR=/work/.gotmp",
			"GOFLAGS=-mod=readonly",
			"GOPROXY=off",
			"GOTOOLCHAIN=local",
			"GOTELEMETRY=off",
			"CGO_ENABLED=0",
			"PATH=" + getenv("PATH"),
		},
	}
}

// serve serves s on listener and runs its single test. On SIGTERM, which the platform
// sends when it stops the sandbox, the command's process group is ended; serve returns
// once the command has ended and the server has closed. After the command exits on its
// own, the result stays available until SIGTERM.
func serve(ctx context.Context, listener net.Listener, s *supervisor, workspaces string) error {
	// Registering for SIGTERM before the command starts means the signal can never end the
	// supervisor while leaving the command's group running.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	server := &http.Server{Handler: s, ReadHeaderTimeout: readHeaderTimeout}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	go s.run(ctx, workspaces)

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-served:
		cancel()
	}
	<-s.finished

	shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil && serveErr == nil {
		serveErr = err
	}
	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return serveErr
}
