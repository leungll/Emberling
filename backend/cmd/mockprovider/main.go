// Command mockprovider serves the deterministic HTTP Mock Provider
// (internal/mockprovider) as a standalone process, the form deploy/compose.yaml starts for
// `make dev` and the MVP acceptance scenarios. All behaviour lives in that package, which
// the contract tests serve in-process instead; this command only owns the listen address
// and the graceful-shutdown sequence.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	addr := os.Getenv("MOCKPROVIDER_ADDR")
	if addr == "" {
		addr = defaultAddr
	}

	dispatcher := mockprovider.NewDispatcher(nil)
	server := mockprovider.NewServer(dispatcher)
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

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("mockprovider: http shutdown error", slog.String("error", err.Error()))
	}
	if err := dispatcher.Shutdown(shutdownCtx); err != nil {
		logger.Error("mockprovider: dispatcher shutdown error", slog.String("error", err.Error()))
	}
}
