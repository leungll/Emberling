package mockprovider

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
)

// maxIdempotencyKeys bounds the per-instance idempotency map. The Mock Provider is a test
// fixture with no persistence, so the map exists only to deduplicate a retried dispatch
// within one process lifetime; 10k keys is far more than any test needs while keeping a
// long-running instance from growing without limit. Once full, the oldest key is evicted,
// and a replay of an evicted key simply creates a new task again.
const maxIdempotencyKeys = 10_000

// imageMaxAgeSeconds is the Cache-Control lifetime of GET /v1/images/{name}.png. The bytes
// are a compile-time constant, so any lifetime is safe; one day keeps a cached response
// well inside the retention window of the Run whose output points at it.
const imageMaxAgeSeconds = 86_400

// fixedPNGBase64 is the single 16x16 PNG every image route response carries, embedded as a
// constant so the Provider serves real, decodable image bytes without reading a file,
// generating content per request or depending on a Go image encoder's output staying
// byte-stable across releases.
const fixedPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAIAAACQkWg2AAAAIElEQVR42mJhYGgQYGAgHrGACFLAqIZRDUNHAyAAAP//JzoCof3gAH0AAAAASUVORK5CYII="

// fixedPNG is fixedPNGBase64 decoded once. A decode failure is impossible for a constant
// literal, so it is treated as the programming error it would be.
var fixedPNG = mustDecodeBase64(fixedPNGBase64)

func mustDecodeBase64(encoded string) []byte {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		panic("mockprovider: fixed PNG constant is not valid base64: " + err.Error())
	}
	return decoded
}

// Server implements the Mock Provider's routes: a liveness probe, a synchronous
// text-generation simulation, a synchronous image generation with the route serving each
// generated asset, an asynchronous task/callback simulation (including video tasks) with a
// task status query, and the image route an asynchronous result's EXTERNAL reference
// points at.
type Server struct {
	dispatcher *Dispatcher
	newTaskID  func() (string, error)
	router     chi.Router

	// mu guards the idempotency map and its eviction order. handleTasks resolves a key to
	// a task id and records a new one as a single critical section, so two concurrent
	// POSTs carrying the same key cannot both schedule a callback.
	mu sync.Mutex
	// tasksByIdempotencyKey maps a caller's idempotency key to the externalTaskId of the
	// task it first created. It holds no callback token or payload.
	tasksByIdempotencyKey map[string]string
	// idempotencyKeyOrder records insertion order so the oldest key can be evicted once
	// maxIdempotencyKeys is reached.
	idempotencyKeyOrder []string

	// controls is the demo and test control surface (barrier, dispatch record, callback
	// redelivery). It is nil unless WithTestControls was passed, and every use of it is a
	// no-op when nil, so the default request path is unchanged.
	controls *testControls

	// statuses answers GET /v1/tasks/{externalTaskId}. It exists with or without test
	// controls and is independent of the redeliver control's task map.
	statuses *taskStatuses

	// publicBaseURL, when set, prefixes every generated imageUrl in place of the
	// request's own scheme and host. It is empty unless WithPublicBaseURL was passed.
	publicBaseURL string
}

// NewServer wires dispatcher into a ready-to-serve Server. dispatcher is owned by the
// caller, which is also responsible for calling dispatcher.Shutdown during graceful
// shutdown. With WithTestControls, the caller also owns the Record and calls StopHolding
// before shutting the HTTP server down.
func NewServer(dispatcher *Dispatcher, opts ...Option) *Server {
	s := &Server{
		dispatcher:            dispatcher,
		newTaskID:             randomExternalTaskID,
		tasksByIdempotencyKey: make(map[string]string),
		statuses:              newTaskStatuses(),
	}
	for _, opt := range opts {
		opt(s)
	}
	dispatcher.due = s.statuses.markDue
	r := chi.NewRouter()
	r.Get("/healthz", s.handleHealthz)
	r.Post("/v1/text/generate", s.handleGenerate)
	r.Post("/v1/tasks", s.handleTasks)
	r.Get("/v1/tasks/{externalTaskId}", s.handleTaskStatus)
	r.Get("/v1/images/{name}", s.handleImage)
	r.Post("/v1/assets", s.handleGenerateImage)
	r.Get("/v1/assets/{name}", s.handleAsset)
	if s.controls != nil {
		dispatcher.observe = s.controls.record.appendDelivery
		s.mountControlRoutes(r)
	}
	s.router = r
	return s
}

// ServeHTTP makes Server an http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGenerate simulates a synchronous external text-generation call. It never logs the
// prompt; the request body only ever lives in this handler's local scope.
func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	arrival := recordEntry{Kind: kindGenerate}
	var req generateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.reject(w, arrival, http.StatusBadRequest, "request body is not valid JSON")
		return
	}

	scenario := req.Scenario
	if scenario == "" {
		scenario = "final"
	}
	arrival.Scenario = "scenario=" + truncateSummary(scenario)
	if err := s.admit(r, arrival); err != nil {
		s.respondHeldError(w, kindGenerate, "", err)
		return
	}

	var (
		status int
		body   any
	)
	switch scenario {
	case "final":
		status, body = http.StatusOK, generateResponse{Decision: decision{
			Kind:   "FINAL",
			Output: "echo: " + req.Prompt,
		}}
	case "tool-call":
		status, body = http.StatusOK, generateResponse{Decision: decision{
			Kind:      "TOOL_CALL",
			ToolName:  req.ToolName,
			Arguments: json.RawMessage(`{}`),
		}}
	case "invalid-decision":
		// A TOOL_CALL missing its required toolName/arguments: structurally recognised
		// but not a valid Decision, for exercising the caller's own validation path.
		status, body = http.StatusOK, generateResponse{Decision: decision{Kind: "TOOL_CALL"}}
	case "fail":
		status, body = http.StatusBadGateway, errorResponse{Error: "mock provider: generate failed"}
	default:
		status, body = http.StatusBadRequest, errorResponse{Error: fmt.Sprintf("unknown scenario %q", scenario)}
	}
	s.recordResponse(kindGenerate, "", status, false)
	writeJSON(w, status, body)
}

// handleImage serves the image an asynchronous task's callback refers to. A caller that
// reports a result as `{"source":"EXTERNAL","uri":...}` needs that URI to actually resolve:
// this route is what makes such a reference stable, readable and free of any credential -
// it takes no token, sets no cookie and stores nothing.
//
// Every valid name serves the same fixed image. The Provider holds no per-task content, so
// the name only has to be stable for a given task, which is the caller's business, not this
// route's.
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	base, isPNG := strings.CutSuffix(name, ".png")
	if !isPNG || base == "" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(fixedPNG)))
	// The bytes never change, so a caller (or a browser rendering a Run output) may cache
	// them for the whole retention window of the Run that references them.
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(imageMaxAgeSeconds))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(fixedPNG)
}

// handleTasks simulates an external Provider accepting one asynchronous task and
// delivering its result later, as a callback POST to the caller-supplied CallbackURL. It
// never persists callbackToken beyond forwarding it unchanged in that callback.
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	arrival := recordEntry{Kind: kindTask}
	var req taskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.reject(w, arrival, http.StatusBadRequest, "request body is not valid JSON")
		return
	}
	arrival.ExternalTaskID = truncateSummary(req.ExternalTaskID)
	arrival.CallbackTarget = truncateSummary(callbackTarget(req.CallbackURL))
	arrival.Scenario = taskScenarioSummary(req)
	if strings.TrimSpace(req.CallbackURL) == "" {
		s.reject(w, arrival, http.StatusBadRequest, "callbackUrl is required")
		return
	}
	if strings.TrimSpace(req.CallbackToken) == "" {
		s.reject(w, arrival, http.StatusBadRequest, "callbackToken is required")
		return
	}
	if req.Media != "" && req.Media != mediaVideo {
		s.reject(w, arrival, http.StatusBadRequest, `media must be absent or "video"`)
		return
	}

	externalTaskID := req.ExternalTaskID
	if externalTaskID == "" {
		generated, err := s.newTaskID()
		if err != nil {
			s.reject(w, arrival, http.StatusInternalServerError, "failed to generate externalTaskId")
			return
		}
		externalTaskID = generated
	}
	arrival.ExternalTaskID = truncateSummary(externalTaskID)

	// The barrier holds the request after it is recorded and before the Provider accepts
	// anything, so a held request has created no task and scheduled no callback yet.
	if err := s.admit(r, arrival); err != nil {
		s.respondHeldError(w, kindTask, arrival.ExternalTaskID, err)
		return
	}

	if key := req.IdempotencyKey; key != "" {
		existing, created := s.claimIdempotencyKey(key, externalTaskID)
		if !created {
			// A replayed dispatch of an already-accepted task: the Provider answers with
			// the original task id and does not start a second one, so the retry produces
			// no second callback and no duplicate external side effect (KEYED).
			s.recordResponse(kindTask, truncateSummary(existing), http.StatusOK, true)
			writeJSON(w, http.StatusOK, taskResponse{ExternalTaskID: existing})
			return
		}
	}

	callbackToken := req.CallbackToken
	if req.Outcome == outcomeWrongToken {
		// Deliberately mismatched credential for the 401 scenario. The real token is
		// neither logged nor echoed; only the delivered header differs from it.
		callbackToken += wrongTokenSuffix
	}

	task := callbackTask{
		ExternalTaskID: externalTaskID,
		CallbackURL:    req.CallbackURL,
		CallbackToken:  callbackToken,
		Payload:        callbackPayload(req),
	}
	if req.Media == mediaVideo && len(req.Payload) == 0 && req.Outcome != outcomeFailed {
		task.Payload = videoSucceededPayload(requestBaseURL(r), externalTaskID)
	}
	s.rememberTask(task)
	s.statuses.accept(externalTaskID, task.Payload, req.Outcome == outcomeFailed)

	switch req.DelayMs.Mode {
	case delayLost:
		// Deliberately never scheduled: this scenario simulates a Provider that accepted
		// a task and then never reports back.
	case delayDuplicate:
		s.dispatcher.Schedule(task, 0)
		s.dispatcher.Schedule(task, 0)
	case delayBeforeResponse:
		// Errors are intentionally not surfaced to the caller: a real external Provider
		// cannot report its own callback delivery failure back through this response,
		// since the callback endpoint is invisible to it.
		_ = s.dispatcher.SendNow(r.Context(), task)
	case delayAfter:
		s.dispatcher.Schedule(task, req.DelayMs.Duration)
	default: // delayImmediate
		s.dispatcher.Schedule(task, 0)
	}

	if req.Media == mediaVideo {
		s.recordMedia(recordEntry{Event: recordVideoDispatched, Kind: kindTask, ExternalTaskID: arrival.ExternalTaskID})
	}
	s.recordResponse(kindTask, arrival.ExternalTaskID, http.StatusAccepted, false)
	writeJSON(w, http.StatusAccepted, taskResponse{ExternalTaskID: externalTaskID})
}

// claimIdempotencyKey binds key to externalTaskID unless key is already bound. It returns
// the task id now bound to key and whether this call created that binding. Lookup and
// insertion happen under one lock so concurrent replays of the same key cannot both win.
func (s *Server) claimIdempotencyKey(key, externalTaskID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.tasksByIdempotencyKey[key]; ok {
		return existing, false
	}
	if len(s.idempotencyKeyOrder) >= maxIdempotencyKeys {
		oldest := s.idempotencyKeyOrder[0]
		s.idempotencyKeyOrder = s.idempotencyKeyOrder[1:]
		delete(s.tasksByIdempotencyKey, oldest)
	}
	s.tasksByIdempotencyKey[key] = externalTaskID
	s.idempotencyKeyOrder = append(s.idempotencyKeyOrder, key)
	return externalTaskID, true
}

// callbackPayload resolves the payload the scheduled callback will carry. An explicit
// payload is forwarded verbatim, as it always has been; otherwise the request's outcome
// selects this package's own deterministic success or failure body.
func callbackPayload(req taskRequest) json.RawMessage {
	if len(req.Payload) > 0 {
		return req.Payload
	}
	if req.Outcome == outcomeFailed {
		return failedPayload
	}
	return succeededPayload
}

// randomExternalTaskID returns a fresh, unpredictable task identifier. It is a package
// variable indirection point (via Server.newTaskID) so tests can inject a deterministic
// generator instead of asserting against random output.
func randomExternalTaskID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mockprovider: generate externalTaskId: %w", err)
	}
	return "task_" + hex.EncodeToString(buf), nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
