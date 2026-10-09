// Command sandboxrunner serves the sandbox runner (internal/sandboxrunner) as a standalone
// process, the form deploy/compose.yaml starts. All behaviour lives in that package; this
// command owns the configuration, the startup cleanup and redelivery, and the
// graceful-shutdown sequence.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/leungll/Emberling/backend/internal/mockcontrol"
	"github.com/leungll/Emberling/backend/internal/sandboxrunner"
)

// defaultAddr matches the port deploy/compose.yaml maps for the sandboxrunner service.
const defaultAddr = ":9103"

// Backend names SANDBOXRUNNER_BACKEND accepts.
const (
	backendMock   = "mock"
	backendDocker = "docker"
)

// shutdownTimeout bounds how long main waits for in-flight requests, tests and callbacks
// once a shutdown signal arrives; tests still running then are stopped.
const shutdownTimeout = 10 * time.Second

// readHeaderTimeout bounds how long a client may take to send request headers.
const readHeaderTimeout = 10 * time.Second

// startupTimeout bounds removing leftover sandbox containers at startup.
const startupTimeout = time.Minute

// config is the process configuration read from the environment.
type config struct {
	addr    string
	backend string
	// testControls enables the barrier, the request record and result redelivery. It is
	// off unless explicitly enabled.
	testControls bool
	// recordPath is the request record file; required exactly when testControls is on.
	recordPath  string
	image       string
	testTimeout time.Duration
}

// loadConfig reads SANDBOXRUNNER_ADDR, SANDBOXRUNNER_BACKEND, SANDBOXRUNNER_TEST_CONTROLS,
// SANDBOXRUNNER_RECORD_PATH, SANDBOXRUNNER_IMAGE and SANDBOXRUNNER_TEST_TIMEOUT through
// getenv. A record path without test controls is rejected rather than ignored, and an image
// not pinned by digest is rejected, so a tag move cannot change what runs.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		addr:        getenv("SANDBOXRUNNER_ADDR"),
		backend:     getenv("SANDBOXRUNNER_BACKEND"),
		recordPath:  getenv("SANDBOXRUNNER_RECORD_PATH"),
		image:       getenv("SANDBOXRUNNER_IMAGE"),
		testTimeout: sandboxrunner.DefaultTestTimeout,
	}
	if cfg.addr == "" {
		cfg.addr = defaultAddr
	}
	if cfg.backend == "" {
		cfg.backend = backendMock
	}
	if cfg.backend != backendMock && cfg.backend != backendDocker {
		return config{}, fmt.Errorf("SANDBOXRUNNER_BACKEND must be %q or %q", backendMock, backendDocker)
	}
	if cfg.image == "" {
		cfg.image = sandboxrunner.DefaultImage
	}
	// The image is passed to docker run as one argument; a leading dash or whitespace would
	// let it be read as a flag or split, so neither is accepted.
	if !strings.Contains(cfg.image, "@sha256:") || strings.HasPrefix(cfg.image, "-") || strings.ContainsFunc(cfg.image, unicode.IsSpace) {
		return config{}, errors.New("SANDBOXRUNNER_IMAGE must be one image reference pinned by digest (name@sha256:...)")
	}
	if raw := getenv("SANDBOXRUNNER_TEST_TIMEOUT"); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout <= 0 {
			return config{}, errors.New("SANDBOXRUNNER_TEST_TIMEOUT must be a positive duration such as 120s")
		}
		cfg.testTimeout = timeout
	}
	if raw := getenv("SANDBOXRUNNER_TEST_CONTROLS"); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return config{}, fmt.Errorf("SANDBOXRUNNER_TEST_CONTROLS must be a boolean: %w", err)
		}
		cfg.testControls = enabled
	}
	switch {
	case cfg.testControls && cfg.recordPath == "":
		return config{}, errors.New("SANDBOXRUNNER_RECORD_PATH is required when SANDBOXRUNNER_TEST_CONTROLS is enabled")
	case !cfg.testControls && cfg.recordPath != "":
		return config{}, errors.New("SANDBOXRUNNER_RECORD_PATH is set but SANDBOXRUNNER_TEST_CONTROLS is not enabled")
	}
	return cfg, nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("sandboxrunner: " + err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var backend sandboxrunner.Backend = sandboxrunner.NewMockBackend()
	if cfg.backend == backendDocker {
		docker := sandboxrunner.NewDockerBackend(cfg.image, cfg.testTimeout, sandboxrunner.ExecRunner{})
		startupCtx, cancel := context.WithTimeout(ctx, startupTimeout)
		removed, err := docker.RemoveOrphans(startupCtx)
		cancel()
		if err != nil {
			return err
		}
		logger.Info("sandboxrunner: docker backend ready", slog.String("image", cfg.image), slog.Int("orphans_removed", removed))
		backend = docker
	}

	var (
		opts   []sandboxrunner.Option
		record *mockcontrol.Record
		store  *sandboxrunner.RedeliveryStore
		stored []sandboxrunner.StoredResult
	)
	if cfg.testControls {
		record, err = mockcontrol.OpenRecord(cfg.recordPath)
		if err != nil {
			return fmt.Errorf("open request record: %w", err)
		}
		defer closeLogged(logger, "request record", record.Close)
		store, stored, err = sandboxrunner.OpenRedeliveryStore(sandboxrunner.RedeliveryPath(cfg.recordPath))
		if err != nil {
			return err
		}
		defer closeLogged(logger, "redelivery store", store.Close)
		opts = append(opts, sandboxrunner.WithTestControls(record, store))
		logger.Info("sandboxrunner: test controls enabled", slog.String("record_path", cfg.recordPath), slog.Int("results_to_redeliver", len(stored)))
	}

	server := sandboxrunner.NewServer(backend, opts...)
	server.Redeliver(stored)
	httpServer := &http.Server{Addr: cfg.addr, Handler: server, ReadHeaderTimeout: readHeaderTimeout}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("sandboxrunner: listening", slog.String("addr", cfg.addr), slog.String("backend", cfg.backend))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("sandboxrunner: shutdown signal received")
	case err := <-serveErr:
		if err != nil {
			logger.Error("sandboxrunner: listen error", slog.String("error", err.Error()))
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	// Held requests are rejected first: both shutdowns below wait for them otherwise.
	server.StopHolding()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("sandboxrunner: http shutdown error", slog.String("error", err.Error()))
	}
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("sandboxrunner: test shutdown error", slog.String("error", err.Error()))
	}
	return nil
}

func closeLogged(logger *slog.Logger, name string, closeFn func() error) {
	if err := closeFn(); err != nil {
		logger.Error("sandboxrunner: close "+name, slog.String("error", err.Error()))
	}
}
