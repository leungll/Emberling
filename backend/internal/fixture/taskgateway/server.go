package taskgateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxIdempotencyKeys and maxTasks bound the two in-memory maps exactly as the Mock
// Provider bounds its own. Once full the oldest entry is evicted: a replay of an evicted
// key creates a new task, and a status query for an evicted task answers 404.
const (
	maxIdempotencyKeys = 10_000
	maxTasks           = 10_000
)

// maxTaskRequestBytes bounds one POST /v1/tasks body. A dispatch carries a reference, not
// image bytes, so it is small.
const maxTaskRequestBytes = 1 << 20

// maxFailureMessageBytes bounds an upstream failure label copied into a payload.
const maxFailureMessageBytes = 200

// Config is everything a Server needs. Models maps each gateway Model ID to the upstream
// generator that serves it. AssetBaseURL is Emberling's origin, used to read ASSET
// reference images; PublicBaseURL is the browser-reachable origin of this gateway, which
// every reported image URI starts with.
type Config struct {
	Models        map[string]ImageGenerator
	AssetBaseURL  string
	PublicBaseURL string

	Workers         int
	QueueSize       int
	MaxTaskDuration time.Duration
	MaxImageBytes   int64
	ImageStoreBytes int64

	// ReferenceClient reads reference images; CallbackClient delivers callbacks. Nil
	// clients get bounded defaults.
	ReferenceClient *http.Client
	CallbackClient  *http.Client
	Logger          *slog.Logger

	// Now, After and NewID are injection points for tests; nil means the real clock,
	// real timers and random identifiers.
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time
	NewID func(prefix string) (string, error)
}

// Server serves the gateway's routes and owns its workers and Dispatcher.
type Server struct {
	models          map[string]ImageGenerator
	assetBaseURL    string
	publicBaseURL   string
	maxTaskDuration time.Duration
	maxImageBytes   int64
	referenceClient *http.Client
	logger          *slog.Logger
	now             func() time.Time
	newID           func(prefix string) (string, error)

	dispatcher *Dispatcher
	images     *imageStore
	queue      chan job
	mux        *http.ServeMux

	workCtx    context.Context
	cancelWork context.CancelFunc
	workers    sync.WaitGroup

	// mu guards the idempotency map, the task map and the enqueue: a key is claimed, its
	// task recorded and the job queued as one critical section, so two concurrent
	// dispatches with one key create one task, and a refused enqueue leaves no claim.
	mu        sync.Mutex
	keys      map[string]string
	keyOrder  []string
	tasks     map[string]*task
	taskOrder []string
}

// task is what the status map keeps: no token and no callback URL.
type task struct {
	status  string
	payload json.RawMessage
}

// job is one accepted task waiting for a worker. It holds the callback token until the
// task's callback is handed to the Dispatcher.
type job struct {
	externalTaskID string
	modelID        string
	prompt         string
	reference      referenceWire
	callbackURL    string
	callbackToken  string
	deadline       time.Time
}

// NewServer validates cfg and starts the worker pool. The caller owns the Server and
// calls Shutdown to stop it.
func NewServer(cfg Config) (*Server, error) {
	var problems []string
	if len(cfg.Models) == 0 {
		problems = append(problems, "at least one model")
	}
	if strings.TrimSpace(cfg.AssetBaseURL) == "" {
		problems = append(problems, "asset base URL")
	}
	if strings.TrimSpace(cfg.PublicBaseURL) == "" {
		problems = append(problems, "public base URL")
	}
	if cfg.Workers <= 0 || cfg.QueueSize <= 0 {
		problems = append(problems, "positive worker count and queue size")
	}
	if cfg.MaxTaskDuration <= 0 {
		problems = append(problems, "positive maximum task duration")
	}
	if cfg.MaxImageBytes <= 0 || cfg.ImageStoreBytes <= 0 {
		problems = append(problems, "positive image size limit and staging budget")
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("taskgateway: missing %s", strings.Join(problems, ", "))
	}
	if cfg.ReferenceClient == nil {
		cfg.ReferenceClient = &http.Client{Timeout: time.Minute}
	}
	if cfg.CallbackClient == nil {
		cfg.CallbackClient = &http.Client{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.After == nil {
		cfg.After = time.After
	}
	if cfg.NewID == nil {
		cfg.NewID = randomID
	}

	workCtx, cancelWork := context.WithCancel(context.Background())
	s := &Server{
		models:          cfg.Models,
		assetBaseURL:    strings.TrimRight(cfg.AssetBaseURL, "/"),
		publicBaseURL:   strings.TrimRight(cfg.PublicBaseURL, "/"),
		maxTaskDuration: cfg.MaxTaskDuration,
		maxImageBytes:   cfg.MaxImageBytes,
		referenceClient: cfg.ReferenceClient,
		logger:          cfg.Logger,
		now:             cfg.Now,
		newID:           cfg.NewID,
		dispatcher:      newDispatcher(cfg.CallbackClient, cfg.Logger, cfg.After, cfg.Now),
		images:          newImageStore(cfg.ImageStoreBytes),
		queue:           make(chan job, cfg.QueueSize),
		workCtx:         workCtx,
		cancelWork:      cancelWork,
		keys:            make(map[string]string),
		tasks:           make(map[string]*task),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /v1/tasks", s.handleTasks)
	mux.HandleFunc("GET /v1/tasks/{externalTaskId}", s.handleTaskStatus)
	mux.HandleFunc("GET /v1/images/{imageId}", s.handleImage)
	s.mux = mux

	for range cfg.Workers {
		s.workers.Add(1)
		go s.work()
	}
	return s, nil
}

// ServeHTTP makes Server an http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// Shutdown stops the workers - a queued or running task is not completed - and then the
// Dispatcher, which waits for deliveries already on the wire until ctx ends. The caller
// stops its HTTP server first, so no new task arrives meanwhile.
func (s *Server) Shutdown(ctx context.Context) error {
	s.cancelWork()
	done := make(chan struct{})
	go func() {
		s.workers.Wait()
		close(done)
	}()
	var workersErr error
	select {
	case <-done:
	case <-ctx.Done():
		workersErr = ctx.Err()
	}
	return errors.Join(workersErr, s.dispatcher.Shutdown(ctx))
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleTasks accepts one task. Validation failures answer 400 without claiming the
// idempotency key; a replayed key answers 200 with the original task and starts nothing;
// a full queue answers 429 and releases the key, so the refusal is definite and a later
// retry with the same key is accepted as new.
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	var req taskRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTaskRequestBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "request body is not valid JSON"})
		return
	}
	accepted, reason := s.validate(req)
	if reason != "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: reason})
		return
	}

	externalTaskID, err := s.newID("gw_")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "task id could not be generated"})
		return
	}
	accepted.externalTaskID = externalTaskID

	existing, status := s.accept(req.IdempotencyKey, accepted)
	switch status {
	case http.StatusOK:
		writeJSON(w, http.StatusOK, taskResponse{ExternalTaskID: existing})
	case http.StatusTooManyRequests:
		s.logger.Warn("taskgateway: task refused, queue full", slog.String("model_id", accepted.modelID))
		writeJSON(w, http.StatusTooManyRequests, errorResponse{Error: "task queue is full"})
	default:
		s.logger.Info("taskgateway: task accepted",
			slog.String("external_task_id", externalTaskID), slog.String("model_id", accepted.modelID))
		writeJSON(w, http.StatusAccepted, taskResponse{ExternalTaskID: externalTaskID})
	}
}

// validate checks a dispatch synchronously and returns the job it describes, or the fixed
// reason it is refused.
func (s *Server) validate(req taskRequest) (job, string) {
	if strings.TrimSpace(req.CallbackURL) == "" || strings.TrimSpace(req.CallbackToken) == "" {
		return job{}, "callbackUrl and callbackToken are required"
	}
	var options taskOptions
	if len(req.Options) > 0 {
		if err := json.Unmarshal(req.Options, &options); err != nil {
			return job{}, "options must be an object"
		}
	}
	if _, ok := s.models[options.ModelID]; !ok {
		return job{}, "options.modelId is not a gateway model"
	}
	if req.Reference == nil {
		return job{}, "reference is required"
	}
	switch req.Reference.Source {
	case sourceAsset:
		if req.Reference.Asset == nil || strings.TrimSpace(req.Reference.Asset.AssetID) == "" {
			return job{}, "reference asset is incomplete"
		}
	case sourceExternal:
		if strings.TrimSpace(req.Reference.URI) == "" {
			return job{}, "reference uri is required"
		}
	default:
		return job{}, "reference.source must be ASSET or EXTERNAL"
	}
	deadline := s.now().Add(s.maxTaskDuration)
	if req.Deadline != "" {
		requested, err := time.Parse(time.RFC3339Nano, req.Deadline)
		if err != nil {
			return job{}, "deadline must be an RFC 3339 timestamp"
		}
		if requested.Before(deadline) {
			deadline = requested
		}
	}
	return job{
		modelID:       options.ModelID,
		prompt:        req.Prompt,
		reference:     *req.Reference,
		callbackURL:   req.CallbackURL,
		callbackToken: req.CallbackToken,
		deadline:      deadline,
	}, ""
}

// accept claims key, records the task and queues its job as one critical section. It
// returns the original task id with 200 for a replayed key, 429 when the queue is full
// and 202 for a new task.
func (s *Server) accept(key string, j job) (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key != "" {
		if existing, ok := s.keys[key]; ok {
			return existing, http.StatusOK
		}
	}
	select {
	case s.queue <- j:
	default:
		return "", http.StatusTooManyRequests
	}
	if key != "" {
		if len(s.keyOrder) >= maxIdempotencyKeys {
			delete(s.keys, s.keyOrder[0])
			s.keyOrder = s.keyOrder[1:]
		}
		s.keys[key] = j.externalTaskID
		s.keyOrder = append(s.keyOrder, key)
	}
	if len(s.taskOrder) >= maxTasks {
		delete(s.tasks, s.taskOrder[0])
		s.taskOrder = s.taskOrder[1:]
	}
	s.tasks[j.externalTaskID] = &task{status: statusRunning}
	s.taskOrder = append(s.taskOrder, j.externalTaskID)
	return j.externalTaskID, http.StatusAccepted
}

// handleTaskStatus answers one status query. It never starts, repeats or delivers
// anything.
func (s *Server) handleTaskStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("externalTaskId")
	s.mu.Lock()
	t, ok := s.tasks[id]
	var resp taskStatusResponse
	if ok {
		resp = taskStatusResponse{ExternalTaskID: id, Status: t.status, Payload: t.payload}
	}
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "unknown externalTaskId"})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// work runs jobs until Shutdown.
func (s *Server) work() {
	defer s.workers.Done()
	for {
		select {
		case <-s.workCtx.Done():
			return
		case j := <-s.queue:
			s.run(j)
		}
	}
}

// run executes one job under its deadline. When Shutdown interrupted it the task is left
// unfinished: the gateway keeps no state across a restart, so no result is reported.
func (s *Server) run(j job) {
	ctx, cancel := context.WithDeadline(s.workCtx, j.deadline)
	defer cancel()

	result := s.execute(ctx, j)
	if s.workCtx.Err() != nil {
		return
	}
	payload, err := json.Marshal(result)
	if err != nil {
		result = resultPayload{Status: statusFailed, Error: &errorWire{Code: codeProviderTaskFailed, Message: "result could not be encoded"}}
		payload, _ = json.Marshal(result)
	}

	s.mu.Lock()
	if t, ok := s.tasks[j.externalTaskID]; ok {
		t.status = result.Status
		t.payload = payload
	}
	s.mu.Unlock()

	attrs := []any{slog.String("external_task_id", j.externalTaskID), slog.String("model_id", j.modelID), slog.String("status", result.Status)}
	if result.Error != nil {
		attrs = append(attrs, slog.String("error_code", result.Error.Code))
	}
	s.logger.Info("taskgateway: task finished", attrs...)

	s.dispatcher.deliver(delivery{
		externalTaskID: j.externalTaskID,
		url:            j.callbackURL,
		token:          j.callbackToken,
		payload:        payload,
		deadline:       j.deadline,
	})
}

// execute resolves the reference, calls the upstream once and stages the image.
func (s *Server) execute(ctx context.Context, j job) resultPayload {
	expired := resultPayload{Status: statusFailed, Error: &errorWire{Code: codeTaskExpired, Message: "task deadline passed before the image was generated"}}
	if ctx.Err() != nil || !s.now().Before(j.deadline) {
		return expired
	}
	reference, failure := s.fetchReference(ctx, j.reference)
	if failure != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return expired
		}
		return resultPayload{Status: statusFailed, Error: &errorWire{Code: failure.code, Message: failure.message}}
	}

	image, err := s.models[j.modelID].Generate(ctx, GenerateRequest{
		Prompt:             j.prompt,
		ReferenceImage:     reference.content,
		ReferenceMediaType: reference.mediaType,
	})
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return expired
		}
		return resultPayload{Status: statusFailed, Error: &errorWire{Code: codeProviderTaskFailed, Message: boundedLabel(err.Error())}}
	}

	mediaType := image.MediaType
	if mediaType == "" {
		mediaType = http.DetectContentType(image.Content)
	}
	if !strings.HasPrefix(mediaType, "image/") {
		return resultPayload{Status: statusFailed, Error: &errorWire{Code: codeProviderTaskFailed, Message: "upstream answer is not an image"}}
	}
	imageID, err := s.newID("img_")
	if err != nil {
		return resultPayload{Status: statusFailed, Error: &errorWire{Code: codeProviderTaskFailed, Message: "image id could not be generated"}}
	}
	if err := s.images.put(imageID, stagedImage{mediaType: mediaType, content: image.Content}); err != nil {
		return resultPayload{Status: statusFailed, Error: &errorWire{Code: codeProviderTaskFailed, Message: err.Error()}}
	}
	result := resultPayload{
		Status: statusSucceeded,
		Image:  &imageWire{Source: sourceExternal, URI: s.publicBaseURL + "/v1/images/" + imageID, MediaType: mediaType},
	}
	if image.Cost != nil {
		result.Usage = &usageWire{Cost: *image.Cost}
	}
	return result
}

// boundedLabel caps an upstream failure label.
func boundedLabel(label string) string {
	if len(label) <= maxFailureMessageBytes {
		return label
	}
	return label[:maxFailureMessageBytes]
}

// randomID returns prefix plus random hex: 8 bytes for a task id, 16 for an image id,
// which is served without a credential and so must not be guessable.
func randomID(prefix string) (string, error) {
	size := 8
	if prefix == "img_" {
		size = 16
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("taskgateway: generate id: %w", err)
	}
	return prefix + hex.EncodeToString(buf), nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
