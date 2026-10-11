// Command sandboxrunner serves the sandbox runner (internal/fixture/sandboxrunner) as a standalone
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
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/leungll/Emberling/backend/internal/fixture/mockcontrol"
	"github.com/leungll/Emberling/backend/internal/fixture/sandboxrunner"
)

// defaultAddr matches the port deploy/compose.yaml maps for the sandboxrunner service.
const defaultAddr = ":9103"

// Backend names SANDBOXRUNNER_BACKEND accepts.
const (
	backendMock   = "mock"
	backendDocker = "docker"
	backendAX     = "ax"
)

// shutdownTimeout bounds how long main waits for in-flight requests, tests and callbacks
// once a shutdown signal arrives; tests still running then are stopped.
const shutdownTimeout = 10 * time.Second

// readHeaderTimeout bounds how long a client may take to send request headers.
const readHeaderTimeout = 10 * time.Second

// axRouterClientTimeout bounds one AX router request at the transport level; the ax
// backend bounds each result query more tightly through its context.
const axRouterClientTimeout = 30 * time.Second

// startupTimeout bounds removing leftover sandbox containers or AX resources at startup.
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
	// The ax backend's settings; read and required only when the backend is ax.
	axAtespace     string
	axRouterURL    string
	axCLI          string
	axReadyTimeout time.Duration
}

// loadConfig reads SANDBOXRUNNER_ADDR, SANDBOXRUNNER_BACKEND, SANDBOXRUNNER_TEST_CONTROLS,
// SANDBOXRUNNER_RECORD_PATH, SANDBOXRUNNER_IMAGE and SANDBOXRUNNER_TEST_TIMEOUT through
// getenv, and the SANDBOXRUNNER_AX_* settings when the backend is ax. A record path without
// test controls is rejected rather than ignored, and an image not pinned by digest is
// rejected, so a tag move cannot change what runs.
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
	if cfg.backend != backendMock && cfg.backend != backendDocker && cfg.backend != backendAX {
		return config{}, fmt.Errorf("SANDBOXRUNNER_BACKEND must be %q, %q or %q", backendMock, backendDocker, backendAX)
	}
	if cfg.image == "" {
		// The default image is the docker backend's Go image; the ax backend needs the
		// sandbox supervisor image, which has no default.
		if cfg.backend == backendAX {
			return config{}, errors.New("SANDBOXRUNNER_IMAGE is required when SANDBOXRUNNER_BACKEND is ax")
		}
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
	if cfg.backend == backendAX {
		if err := loadAXConfig(getenv, &cfg); err != nil {
			return config{}, err
		}
	}
	switch {
	case cfg.testControls && cfg.recordPath == "":
		return config{}, errors.New("SANDBOXRUNNER_RECORD_PATH is required when SANDBOXRUNNER_TEST_CONTROLS is enabled")
	case !cfg.testControls && cfg.recordPath != "":
		return config{}, errors.New("SANDBOXRUNNER_RECORD_PATH is set but SANDBOXRUNNER_TEST_CONTROLS is not enabled")
	}
	return cfg, nil
}

// loadAXConfig reads the ax backend's settings. Every one is required; an error names
// only the key, never a value.
func loadAXConfig(getenv func(string) string, cfg *config) error {
	cfg.axAtespace = getenv("SANDBOXRUNNER_AX_ATESPACE")
	cfg.axRouterURL = getenv("SANDBOXRUNNER_AX_ROUTER_URL")
	cfg.axCLI = getenv("SANDBOXRUNNER_AX_CLI")
	rawReady := getenv("SANDBOXRUNNER_AX_READY_TIMEOUT")
	for _, setting := range []struct{ key, value string }{
		{"SANDBOXRUNNER_AX_ATESPACE", cfg.axAtespace},
		{"SANDBOXRUNNER_AX_ROUTER_URL", cfg.axRouterURL},
		{"SANDBOXRUNNER_AX_CLI", cfg.axCLI},
		{"SANDBOXRUNNER_AX_READY_TIMEOUT", rawReady},
	} {
		if strings.TrimSpace(setting.value) == "" {
			return fmt.Errorf("%s is required when SANDBOXRUNNER_BACKEND is ax", setting.key)
		}
	}
	if !sandboxrunner.ValidAXName(cfg.axAtespace) {
		return errors.New("SANDBOXRUNNER_AX_ATESPACE must be a lowercase RFC 1123 label of at most 63 characters")
	}
	router, err := url.Parse(cfg.axRouterURL)
	if err != nil || (router.Scheme != "http" && router.Scheme != "https") || router.Host == "" {
		return errors.New("SANDBOXRUNNER_AX_ROUTER_URL must be an absolute http or https URL")
	}
	readyTimeout, err := time.ParseDuration(rawReady)
	if err != nil || readyTimeout <= 0 {
		return errors.New("SANDBOXRUNNER_AX_READY_TIMEOUT must be a positive duration such as 2m")
	}
	cfg.axReadyTimeout = readyTimeout
	return nil
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

	var backend sandboxrunner.Backend = sandboxrunner.NewMockBackend()
	switch cfg.backend {
	case backendDocker:
		docker := sandboxrunner.NewDockerBackend(cfg.image, cfg.testTimeout, sandboxrunner.ExecRunner{})
		startupCtx, cancel := context.WithTimeout(ctx, startupTimeout)
		removed, err := docker.RemoveOrphans(startupCtx)
		cancel()
		if err != nil {
			return err
		}
		logger.Info("sandboxrunner: docker backend ready", slog.String("image", cfg.image), slog.Int("orphans_removed", removed))
		backend = docker
	case backendAX:
		ax, err := sandboxrunner.NewAXBackend(sandboxrunner.AXBackendConfig{
			Atespace: cfg.axAtespace, Image: cfg.image,
			TestTimeout: cfg.testTimeout, ReadyTimeout: cfg.axReadyTimeout,
			Control: sandboxrunner.NewAXCLI(sandboxrunner.ExecAXRunner{Path: cfg.axCLI}),
			Results: sandboxrunner.NewAXRouterReader(cfg.axRouterURL, &http.Client{Timeout: axRouterClientTimeout}),
			Logger:  logger, Record: record,
		})
		if err != nil {
			return err
		}
		startupCtx, cancel := context.WithTimeout(ctx, startupTimeout)
		removed, err := ax.RemoveOrphans(startupCtx)
		cancel()
		if err != nil {
			return err
		}
		logger.Info("sandboxrunner: ax backend ready", slog.String("image", cfg.image), slog.String("atespace", cfg.axAtespace), slog.Int("orphans_removed", removed))
		backend = ax
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
