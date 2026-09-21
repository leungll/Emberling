// Command mockprovider serves the deterministic HTTP Mock Provider
// (internal/mockprovider) as a standalone process, the form deploy/compose.yaml starts for
// `make dev` and the MVP acceptance scenarios. All behaviour lives in that package, which
// the contract tests serve in-process instead; this command only owns the listen address
// and the graceful-shutdown sequence.
package main

import (
	"context"
	"log"
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

func main() {
	addr := os.Getenv("MOCKPROVIDER_ADDR")
	if addr == "" {
		addr = defaultAddr
	}

	dispatcher := mockprovider.NewDispatcher(nil)
	server := mockprovider.NewServer(dispatcher)
	httpServer := &http.Server{Addr: addr, Handler: server}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("mockprovider: listening on %s", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		log.Printf("mockprovider: shutdown signal received")
	case err := <-serveErr:
		if err != nil {
			log.Printf("mockprovider: listen error: %v", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("mockprovider: http shutdown error: %v", err)
	}
	if err := dispatcher.Shutdown(shutdownCtx); err != nil {
		log.Printf("mockprovider: dispatcher shutdown error: %v", err)
	}
}
