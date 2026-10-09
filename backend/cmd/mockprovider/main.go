// Command mockprovider serves the deterministic HTTP Mock Provider
// (internal/mockprovider) as a standalone process, the form deploy/compose.yaml starts for
// `make dev` and the MVP acceptance scenarios. All behaviour lives in that package, which
// the contract tests serve in-process instead; this command only owns the listen address
// and the graceful-shutdown sequence.
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
	"syscall"
	"time"

	"github.com/leungll/Emberling/backend/internal/mockprovider"
)

// defaultAddr matches deploy/compose.yaml, which maps host:container port 9101:9101 for
// the mockprovider service and points EMBERLING_MODEL_PROVIDER_BASE_URL at
// http://mockprovider:9101 with no MOCKPROVIDER_ADDR override. That already-deployed
// configuration is treated as authoritative over a differing default suggested elsewhere.
const defaultAddr = ":9101"

// shutdownTimeout bounds how long main waits for in-flight requests and scheduled
// callbacks to finish once a shutdown signal arrives.
const shutdownTimeout = 10 * time.Second

// readHeaderTimeout bounds how long a client may take to send request headers.
const readHeaderTimeout = 10 * time.Second

// config is the process configuration read from the environment.
type config struct {
	addr string
	// testControls enables the barrier, dispatch record and callback redelivery routes
	// under /control. It is off unless explicitly enabled, so a default deployment exposes
	// none of them and records nothing.
	testControls bool
	// recordPath is the append-only dispatch record file; required exactly when
	// testControls is on.
	recordPath string
}

// loadConfig reads MOCKPROVIDER_ADDR, MOCKPROVIDER_TEST_CONTROLS and
// MOCKPROVIDER_RECORD_PATH through getenv. A record path without test controls is
// rejected rather than ignored, so a configuration never looks like it records when it
// does not.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{addr: getenv("MOCKPROVIDER_ADDR"), recordPath: getenv("MOCKPROVIDER_RECORD_PATH")}
	if cfg.addr == "" {
		cfg.addr = defaultAddr
	}
	if raw := getenv("MOCKPROVIDER_TEST_CONTROLS"); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return config{}, fmt.Errorf("MOCKPROVIDER_TEST_CONTROLS must be a boolean: %w", err)
		}
		cfg.testControls = enabled
	}
	switch {
	case cfg.testControls && cfg.recordPath == "":
		return config{}, errors.New("MOCKPROVIDER_RECORD_PATH is required when MOCKPROVIDER_TEST_CONTROLS is enabled")
	case !cfg.testControls && cfg.recordPath != "":
		return config{}, errors.New("MOCKPROVIDER_RECORD_PATH is set but MOCKPROVIDER_TEST_CONTROLS is not enabled")
	}
	return cfg, nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		logger.Error("mockprovider: invalid configuration", slog.String("error", err.Error()))
		os.Exit(1)
	}
	addr := cfg.addr

	var (
		opts   []mockprovider.Option
		record *mockprovider.Record
	)
	if cfg.testControls {
		record, err = mockprovider.OpenRecord(cfg.recordPath)
		if err != nil {
			logger.Error("mockprovider: open dispatch record", slog.String("error", err.Error()))
			os.Exit(1)
		}
		opts = append(opts, mockprovider.WithTestControls(record))
		logger.Info("mockprovider: test controls enabled", slog.String("record_path", cfg.recordPath))
	}

	dispatcher := mockprovider.NewDispatcher(nil)
	server := mockprovider.NewServer(dispatcher, opts...)
	httpServer := &http.Server{Addr: addr, Handler: server, ReadHeaderTimeout: readHeaderTimeout}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("mockprovider: listening", slog.String("addr", addr))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("mockprovider: shutdown signal received")
	case err := <-serveErr:
		if err != nil {
			logger.Error("mockprovider: listen error", slog.String("error", err.Error()))
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// Held requests are rejected first: http.Server.Shutdown waits for every active
	// handler, and a held handler would otherwise wait for a release nobody will send.
	server.StopHolding()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("mockprovider: http shutdown error", slog.String("error", err.Error()))
	}
	if err := dispatcher.Shutdown(shutdownCtx); err != nil {
		logger.Error("mockprovider: dispatcher shutdown error", slog.String("error", err.Error()))
	}
	if record != nil {
		if err := record.Close(); err != nil {
			logger.Error("mockprovider: close dispatch record", slog.String("error", err.Error()))
		}
	}
}
