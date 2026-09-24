// Package api is the HTTP transport for Emberling: it parses requests, calls exactly one
// service method, and maps the result to the wire contract docs/08-interface-spec.md
// defines. It never queries a repository or advances an execution directly (CLAUDE.md
// package boundaries); every handler below is a thin translation over *service.
package api

import (
	"log/slog"
	"time"

	"github.com/leungll/Emberling/backend/internal/config/readiness"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

// defaultPollInterval bounds the SSE stream's fallback poll and heartbeat ticker. Not a
// documented requirement (same MVP-default category as internal/reconciler's
// defaultInterval): in-process notification is the fast path, so this only bounds how
// long a client waits when a notification is missed or never wired.
const defaultPollInterval = 3 * time.Second

// maxRequestBodyBytes bounds every JSON request body this API accepts via
// http.MaxBytesReader. 1 MiB comfortably covers a Definition graph or a Run's input JSON
// (Assets are referenced by AssetRef, never inlined as binary content) while still
// bounding memory for a single request.
const maxRequestBodyBytes = 1 << 20

// Deps carries every collaborator the HTTP layer needs. It holds the four services by
// pointer, never a store or runtime type directly.
type Deps struct {
	Definitions *service.DefinitionService
	Catalog     *service.CatalogService
	Query       *service.QueryService
	Execution   *service.ExecutionService
	Assets      *service.AssetService

	// Readiness answers /health and /ready. It is read-only from this package's
	// perspective; cmd/emberling owns running the startup gate against it.
	Readiness *readiness.Probe

	// Notifier wakes SSE cursor loops after Events commit. It also satisfies
	// service.EventNotifier, so cmd/emberling wires the same *Hub into both Deps.
	Notifier *Hub

	Clock  domain.Clock
	Logger *slog.Logger

	// PollInterval overrides defaultPollInterval. Tests set this to prove a notifier
	// wakeup beats the poll ticker (or to keep a stream's own polling from masking a
	// missing notification).
	PollInterval time.Duration

	// StreamHooks are observation points inside the SSE cursor loop. The zero value is
	// a no-op; only a test that must hold the loop at its query-to-wait switch sets it.
	StreamHooks StreamHooks

	// CallbackMaxPayloadBytes bounds POST /api/callbacks bodies via http.MaxBytesReader
	// (config.PendingCallback.MaxPayloadBytes in cmd/emberling; contract tests set a
	// small value here to exercise the 413 PAYLOAD_TOO_LARGE path without a large body).
	CallbackMaxPayloadBytes int

	// AssetMaxUploadBytes bounds POST /api/assets bodies (config.AssetStorage.MaxUploadBytes
	// in cmd/emberling). The same limit is enforced a second time by the storage layer while
	// it streams, which is what turns an oversized upload into a 413 without ever storing
	// the content.
	AssetMaxUploadBytes int64
}

func (d Deps) withDefaults() Deps {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Notifier == nil {
		d.Notifier = NewHub()
	}
	if d.PollInterval <= 0 {
		d.PollInterval = defaultPollInterval
	}
	if d.Clock == nil {
		d.Clock = domain.SystemClock{}
	}
	if d.CallbackMaxPayloadBytes <= 0 {
		d.CallbackMaxPayloadBytes = maxRequestBodyBytes
	}
	if d.AssetMaxUploadBytes <= 0 {
		d.AssetMaxUploadBytes = maxRequestBodyBytes
	}
	return d
}
