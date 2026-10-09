// Command mockproduction serves the deterministic production deployment stand-in
// (internal/mockproduction) as a standalone process, the form deploy/compose.yaml starts
// for the deploy Tool. All behaviour lives in that package; this command only owns the
// listen address, the optional test controls and the graceful-shutdown sequence.
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

	"github.com/leungll/Emberling/backend/internal/mockproduction"
)

// defaultAddr matches deploy/compose.yaml, which maps port 9102:9102 for the
// mockproduction service and points EMBERLING_PRODUCTION_BASE_URL at
// http://mockproduction:9102.
const defaultAddr = ":9102"

// shutdownTimeout bounds how long main waits for in-flight requests once a shutdown
// signal arrives.
const shutdownTimeout = 10 * time.Second

// readHeaderTimeout bounds how long a client may take to send request headers.
const readHeaderTimeout = 10 * time.Second

// config is the process configuration read from the environment.
type config struct {
	addr string
	// testControls enables the barrier and request record routes under /control. It is
	// off unless explicitly enabled, so a default deployment exposes none of them and
	// records nothing.
	testControls bool
	// recordPath is the append-only request record file; required exactly when
	// testControls is on.
	recordPath string
}

// loadConfig reads MOCKPRODUCTION_ADDR, MOCKPRODUCTION_TEST_CONTROLS and
// MOCKPRODUCTION_RECORD_PATH through getenv. A record path without test controls is
// rejected rather than ignored, so a configuration never looks like it records when it
// does not.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{addr: getenv("MOCKPRODUCTION_ADDR"), recordPath: getenv("MOCKPRODUCTION_RECORD_PATH")}
	if cfg.addr == "" {
		cfg.addr = defaultAddr
	}
	if raw := getenv("MOCKPRODUCTION_TEST_CONTROLS"); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return config{}, fmt.Errorf("MOCKPRODUCTION_TEST_CONTROLS must be a boolean: %w", err)
		}
		cfg.testControls = enabled
	}
	switch {
	case cfg.testControls && cfg.recordPath == "":
		return config{}, errors.New("MOCKPRODUCTION_RECORD_PATH is required when MOCKPRODUCTION_TEST_CONTROLS is enabled")
	case !cfg.testControls && cfg.recordPath != "":
		return config{}, errors.New("MOCKPRODUCTION_RECORD_PATH is set but MOCKPRODUCTION_TEST_CONTROLS is not enabled")
	}
	return cfg, nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		logger.Error("mockproduction: invalid configuration", slog.String("error", err.Error()))
		os.Exit(1)
	}

	var (
		opts   []mockproduction.Option
		record *mockproduction.Record
	)
	if cfg.testControls {
		record, err = mockproduction.OpenRecord(cfg.recordPath)
		if err != nil {
			logger.Error("mockproduction: open request record", slog.String("error", err.Error()))
			os.Exit(1)
		}
		opts = append(opts, mockproduction.WithTestControls(record))
		logger.Info("mockproduction: test controls enabled", slog.String("record_path", cfg.recordPath))
	}

	server := mockproduction.NewServer(opts...)
	httpServer := &http.Server{Addr: cfg.addr, Handler: server, ReadHeaderTimeout: readHeaderTimeout}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("mockproduction: listening", slog.String("addr", cfg.addr))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("mockproduction: shutdown signal received")
	case err := <-serveErr:
		if err != nil {
			logger.Error("mockproduction: listen error", slog.String("error", err.Error()))
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// Held requests are rejected first: http.Server.Shutdown waits for every active
	// handler, and a held handler would otherwise wait for a release nobody will send.
	server.StopHolding()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("mockproduction: http shutdown error", slog.String("error", err.Error()))
	}
	if record != nil {
		if err := record.Close(); err != nil {
			logger.Error("mockproduction: close request record", slog.String("error", err.Error()))
		}
	}
}
