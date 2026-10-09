//go:build integration

// Package contract exercises internal/api's HTTP surface end-to-end against a real
// PostgreSQL database and the real production Node Type / Model registrations
// (cmd/emberling's own assembly), through httptest.Server. Unlike the internal/service
// integration tests, these tests never call a service method directly: every assertion
// goes through the HTTP contract, so a regression in request/response mapping (not just
// in the service layer underneath it) fails here.
package contract

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/adapters/mocktask"
	"github.com/leungll/Emberling/backend/internal/api"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/config/readiness"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/agent"
	"github.com/leungll/Emberling/backend/internal/nodes/imagegeneration"
	"github.com/leungll/Emberling/backend/internal/nodes/imageinput"
	"github.com/leungll/Emberling/backend/internal/nodes/mediabrief"
	"github.com/leungll/Emberling/backend/internal/nodes/mediaoutput"
	"github.com/leungll/Emberling/backend/internal/nodes/prompttemplate"
	"github.com/leungll/Emberling/backend/internal/nodes/textgeneration"
	"github.com/leungll/Emberling/backend/internal/nodes/textinput"
	"github.com/leungll/Emberling/backend/internal/nodes/textoutput"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
	"github.com/leungll/Emberling/backend/internal/tools/remotelookup"
	"github.com/leungll/Emberling/backend/internal/work"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// defaultTestPollInterval keeps the SSE stream's fallback poll (and, per the api package's
// own change, its idle keepalive tick) fast enough that a test bounded by a few seconds
// never has to wait on the api package's own (much longer) production default.
const defaultTestPollInterval = 20 * time.Millisecond

// testEnv is one fully wired Backend (minus the actual net listener, which
// httptest.Server provides): the same registries, services, work Pool and Reconciler
// cmd/emberling/main.go assembles, against a fresh per-test database.
type testEnv struct {
	server *httptest.Server
	probe  *readiness.Probe

	// provider is the same *mockmodel.Provider instance registered into modelRegistry.
	// Tests that need a barrier (BeforeReturn) or a Script override reach it here rather
	// than through the Registry, since registry.ModelRegistry.Get returns a
	// registry.ModelRegistration snapshot, not the live Provider value.
	provider *mockmodel.Provider

	// dispatcher is the same *fakeAsyncDispatcher instance bound to testAsyncNodeType, for
	// tests exercising POST /api/callbacks. It exists in every testEnv
	// (registration is unconditional, same as provider), but only a test that creates a
	// Run against asyncEchoDefinitionRequest ever calls into it.
	dispatcher *fakeAsyncDispatcher

	// uow is the same store.UnitOfWork the wired services use. A callback test reaches
	// it directly, read-only, to assert a rejected delivery persisted no Pending Callback
	// row -- a fact no HTTP response exposes.
	uow store.UnitOfWork

	// execution is the same *service.ExecutionService the work Pool drives. A recovery
	// test hands it to a reconciler.Reconciler it constructs itself: the harness wires no
	// Reconciler, so a test that must prove rediscovery drives exactly one pass rather
	// than waiting on a ticker.
	execution *service.ExecutionService

	// cancelWorkers cancels the work Pool's context without joining its workers, which is
	// what makes "the process died here" reproducible from inside a worker goroutine: a
	// service.EventNotifier fires on the worker's own goroutine, so calling stop (which
	// joins) from there would deadlock. Everything already committed stays committed; the
	// in-process continuation simply never happens.
	cancelWorkers context.CancelFunc

	// pool is the database this Backend was wired against. A restart test hands it to a
	// second newTestEnvWithOptions (testEnvOptions.Pool) so the new Backend observes the
	// same persisted Runs, Attempts and Callback Bindings the stopped one left behind.
	pool *pgxpool.Pool

	// assetRoot is the Asset storage volume this Backend was wired against. A restart
	// test hands it to a second newTestEnvWithOptions (testEnvOptions.AssetStorageRoot)
	// alongside pool, so the new Backend reads the same binaries and Metadata.
	assetRoot string

	// stop tears this Backend down the way a process exit would: the work Pool's workers
	// are cancelled and joined, and the HTTP server stops serving. The database is
	// untouched, so a second testEnv can be started against it. t.Cleanup runs the same
	// teardown, so calling stop early is safe and idempotent.
	stop func()
}

// testEnvOptions customizes newTestEnvWithOptions for the handful of tests that need
// something other than the fast-default wiring newTestEnv gives every other test.
type testEnvOptions struct {
	// PollInterval overrides defaultTestPollInterval when > 0.
	PollInterval time.Duration

	// WrapServiceNotifier, when set, replaces the service.EventNotifier the four
	// services are wired with (service.Deps.Notifier) with whatever it returns, given
	// the real *api.Hub the API layer subscribes against. This lets a test simulate a
	// dropped/never-arriving in-process notification (the "poll ticker is the fallback,
	// not the only path" contract) while api.Deps.Notifier still holds the real Hub, so
	// Subscribe/unsubscribe bookkeeping is unaffected.
	WrapServiceNotifier func(hub *api.Hub) service.EventNotifier

	// CallbackMaxPayloadBytes overrides api.Deps.CallbackMaxPayloadBytes when > 0; the
	// default (defaultTestCallbackMaxPayloadBytes) is small on purpose so a test can
	// exercise 413 PAYLOAD_TOO_LARGE without constructing a large request body.
	CallbackMaxPayloadBytes int

	// Logger, when set, is wired into service.Deps.Logger, the work.Pool and
	// api.Deps.Logger, so a test can capture log output (e.g. to prove a callback token
	// never appears in it) instead of relying on the process-default logger.
	Logger *slog.Logger

	// Pool, when set, is used instead of provisioning a fresh database. It is what makes
	// a restart test possible: the second Backend is a new process image over the same
	// PostgreSQL facts, which is the only recovery source the Runtime allows.
	Pool *pgxpool.Pool

	// MockTaskBaseURL overrides the Mock Provider origin the image_generation Node's
	// mocktask Adapter dispatches to; the default is a placeholder no test may reach.
	MockTaskBaseURL string

	// TaskClient is the *http.Client that same Adapter dispatches with, so a test can
	// inject an http.RoundTripper hook around POST /v1/tasks -- e.g. to hold the
	// Provider's acceptance response while its callback is already in flight.
	TaskClient *http.Client

	// CallbackBaseURL overrides the origin minted into registry.CallbackContext.URL.
	CallbackBaseURL string

	// AssetMaxUploadBytes overrides api.Deps.AssetMaxUploadBytes and the storage layer's
	// own streaming cap when > 0; the default (defaultTestAssetMaxUploadBytes) keeps a
	// 413 PAYLOAD_TOO_LARGE reachable without building a 16 MiB request body.
	AssetMaxUploadBytes int64

	// AssetStorageRoot, when set, is the Asset storage volume this Backend is wired
	// against instead of a fresh t.TempDir(). Together with Pool it completes a restart:
	// a real process restart finds both the same PostgreSQL facts and the same mounted
	// volume, so an Asset uploaded before it stays readable after it.
	AssetStorageRoot string

	// SkipLookupTool, when true, leaves the "lookup" Tool unregistered in this Backend's
	// Tool Registry. Combined with Pool (a second Backend reopened over the same
	// database), this reproduces the one scenario CreateRun's own Agent re-resolution
	// exists for: a Definition saved when "lookup" was registered, then run against a
	// Backend whose current Tool Registry no longer has it (the Registry is fixed for a
	// process's lifetime in the MVP; there is no production Unregister to call instead).
	SkipLookupTool bool

	// WorkHooks is handed to the work.Pool, so a test can observe or hold the worker path
	// with a barrier instead of sleeping.
	WorkHooks work.Hooks

	// StreamHooks is handed to api.Deps, so a test can hold the SSE cursor loop at its
	// query-to-wait switch with a barrier instead of sleeping.
	StreamHooks api.StreamHooks

	// ExtraModelProviders are registered into the Model Registry after the Mock Model
	// Provider, so a catalogue test can add a registration the deterministic Provider does
	// not ship (e.g. one storing a nil Capabilities slice) and observe how GET /api/models
	// serialises it.
	ExtraModelProviders []registry.ModelProvider

	// ExtraNodeRegistrations and ExtraToolRegistrations are registered after the built-in
	// Node Types and Tools, so a catalogue test can add a registration the built-ins do not
	// ship (e.g. one declaring a poll policy) and observe how the catalogue serialises it.
	ExtraNodeRegistrations []registry.NodeRegistration
	ExtraToolRegistrations []registry.ToolRegistration
}

// defaultTestCallbackMaxPayloadBytes bounds POST /api/callbacks bodies in every testEnv
// that does not override it explicitly.
const defaultTestCallbackMaxPayloadBytes = 4096

// defaultTestAssetMaxUploadBytes bounds POST /api/assets bodies in every testEnv that does
// not override it explicitly. Production configures 16 MiB (EMBERLING_ASSET_MAX_UPLOAD_BYTES).
const defaultTestAssetMaxUploadBytes = 1 << 20

// testCallbackSigningSecret is a fixed, non-empty HMAC key every testEnv signs callback
// tokens with. It exists only in-process for this test binary and is never a real
// production Secret.
const testCallbackSigningSecret = "contract-test-callback-signing-secret"

// testCallbackBaseURL is the externally reachable origin every testEnv reports in a
// minted registry.CallbackContext.URL. No test in this package dereferences it: the
// harness calls POST /api/callbacks directly against its own httptest.Server.
const testCallbackBaseURL = "http://callback.contract-test.invalid"

// defaultMockTaskBaseURL is the Mock Provider origin the image_generation Node dispatches
// to unless a test injects one of its own (testEnvOptions.MockTaskBaseURL). It is
// deliberately unreachable: a test that reaches it has wired something wrong.
const defaultMockTaskBaseURL = "http://mock-provider.test"

// testPendingCallbackTTL bounds how long an early callback with no Callback Binding yet
// is retained, mirroring internal/config.PendingCallback.TTL's role in production.
const testPendingCallbackTTL = 5 * time.Minute

// newTestEnv wires a full Backend against a fresh database with defaultTestPollInterval
// and no notifier override; see newTestEnvWithOptions for the full behavior.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvWithOptions(t, testEnvOptions{})
}

// newTestEnvWithOptions wires a full Backend against a fresh database, mirroring
// cmd/emberling/main.go's own assembly: real registries, the real four Node Types bound
// to the deterministic Mock Model Provider (CLAUDE.md: "Mock external systems through the
// deterministic Mock Provider"), a running work.Pool, and a running Reconciler. The
// readiness Probe is driven through the same seven-step gate a real process would run.
func newTestEnvWithOptions(t *testing.T, opts testEnvOptions) *testEnv {
	t.Helper()

	pool := opts.Pool
	if pool == nil {
		pool = testdb.Open(t)
	}
	uow := postgres.NewUnitOfWork(pool)
	clock := domain.SystemClock{}

	mockTaskBaseURL := opts.MockTaskBaseURL
	if mockTaskBaseURL == "" {
		mockTaskBaseURL = defaultMockTaskBaseURL
	}
	callbackBaseURL := opts.CallbackBaseURL
	if callbackBaseURL == "" {
		callbackBaseURL = testCallbackBaseURL
	}

	nodeRegistry := registry.NewNodeRegistry()
	modelRegistry := registry.NewModelRegistry()
	toolRegistry := registry.NewToolRegistry()

	provider := mockmodel.NewProvider()
	if err := modelRegistry.Register(context.Background(), provider); err != nil {
		t.Fatalf("register mock model provider: %v", err)
	}
	for _, extra := range opts.ExtraModelProviders {
		if err := modelRegistry.Register(context.Background(), extra); err != nil {
			t.Fatalf("register extra model provider: %v", err)
		}
	}
	dispatcher := &fakeAsyncDispatcher{}
	for _, reg := range append([]registry.NodeRegistration{
		textinput.Registration(),
		imageinput.Registration(),
		mediabrief.Registration(),
		prompttemplate.Registration(),
		textoutput.Registration(),
		mediaoutput.Registration(),
		textgeneration.Registration(modelRegistry),
		imagegeneration.Registration(modelRegistry, mocktask.New(mockTaskBaseURL, opts.TaskClient)),
		agent.Registration(),
		testAsyncNodeRegistration(dispatcher),
	}, opts.ExtraNodeRegistrations...) {
		if err := nodeRegistry.Register(reg); err != nil {
			t.Fatalf("register node type %s: %v", reg.Metadata.Type, err)
		}
	}
	if !opts.SkipLookupTool {
		if err := toolRegistry.Register(lookup.Registration()); err != nil {
			t.Fatalf("register tool %s: %v", lookup.ToolName, err)
		}
	}
	if err := toolRegistry.Register(remotelookup.Registration(mockTaskBaseURL, opts.TaskClient)); err != nil {
		t.Fatalf("register tool %s: %v", remotelookup.ToolName, err)
	}
	for _, reg := range opts.ExtraToolRegistrations {
		if err := toolRegistry.Register(reg); err != nil {
			t.Fatalf("register tool %s: %v", reg.Metadata.Name, err)
		}
	}
	if err := registry.ValidateFactDeclarations(toolRegistry.ListMetadata(), nodeRegistry.ListMetadata()); err != nil {
		t.Fatalf("validate fact declarations: %v", err)
	}

	compiler := runtime.NewCompiler(nodeRegistry, clock)
	queue := work.NewQueue(0)
	notifier := api.NewHub()

	assetMaxUploadBytes := opts.AssetMaxUploadBytes
	if assetMaxUploadBytes <= 0 {
		assetMaxUploadBytes = defaultTestAssetMaxUploadBytes
	}
	// Each Backend gets its own storage root, removed with the test: Asset content is a
	// file on a volume, so nothing here may outlive the test that uploaded it. A restart
	// test passes the stopped Backend's root back in, which is the volume a restarted
	// process would remount.
	assetRoot := opts.AssetStorageRoot
	if assetRoot == "" {
		assetRoot = t.TempDir()
	}
	assetStore := asset.NewStore(assetRoot, assetMaxUploadBytes)

	var serviceNotifier service.EventNotifier = notifier
	if opts.WrapServiceNotifier != nil {
		serviceNotifier = opts.WrapServiceNotifier(notifier)
	}

	deps := service.Deps{
		UoW:      uow,
		Nodes:    nodeRegistry,
		Models:   modelRegistry,
		Tools:    toolRegistry,
		Compiler: compiler,
		Clock:    clock,
		IDs:      store.NewUUIDGenerator(),
		Assets:   assetStore,
		Queue:    queue,
		Notifier: serviceNotifier,
		Logger:   opts.Logger,
		Callback: service.CallbackConfig{
			BaseURL:       callbackBaseURL,
			SigningSecret: []byte(testCallbackSigningSecret),
			PendingTTL:    testPendingCallbackTTL,
		},
	}

	definitionService := service.NewDefinitionService(deps)
	catalogService := service.NewCatalogService(deps)
	queryService := service.NewQueryService(deps)
	executionService := service.NewExecutionService(deps)
	assetService := service.NewAssetService(deps)

	workPool := work.NewPool(queue, executionService, 4, opts.WorkHooks, opts.Logger)

	ctx, cancel := context.WithCancel(context.Background())
	workPool.Start(ctx)

	probe := readiness.NewProbe()
	if err := probe.Run(context.Background(), readiness.Checks{
		Database:      func(context.Context) error { return nil },
		Configuration: func(context.Context) error { return nil },
		Registry:      func(context.Context) error { return nil },
		// Step 4 runs for real: the same check cmd/emberling gates startup with, so an
		// Asset storage root this Backend cannot use fails here instead of at upload.
		AssetStorage: assetStore.VerifyReadWrite,
		Reconciler:   func(context.Context) error { return nil },
		FirstScan:    func(context.Context) error { return nil },
	}, noopObserver{}); err != nil {
		t.Fatalf("probe.Run: %v", err)
	}

	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultTestPollInterval
	}
	callbackMaxPayloadBytes := opts.CallbackMaxPayloadBytes
	if callbackMaxPayloadBytes <= 0 {
		callbackMaxPayloadBytes = defaultTestCallbackMaxPayloadBytes
	}

	router := api.NewRouter(api.Deps{
		Definitions:             definitionService,
		Catalog:                 catalogService,
		Query:                   queryService,
		Execution:               executionService,
		Assets:                  assetService,
		Readiness:               probe,
		Notifier:                notifier,
		Clock:                   clock,
		PollInterval:            pollInterval,
		StreamHooks:             opts.StreamHooks,
		Logger:                  opts.Logger,
		CallbackMaxPayloadBytes: callbackMaxPayloadBytes,
		AssetMaxUploadBytes:     assetMaxUploadBytes,
	})

	server := httptest.NewServer(router)

	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			workPool.Stop()
			server.Close()
		})
	}
	t.Cleanup(stop)

	return &testEnv{
		server:        server,
		probe:         probe,
		provider:      provider,
		dispatcher:    dispatcher,
		uow:           uow,
		execution:     executionService,
		pool:          pool,
		assetRoot:     assetRoot,
		stop:          stop,
		cancelWorkers: cancel,
	}
}

type noopObserver struct{}

func (noopObserver) StepPassed(readiness.Step, bool)  {}
func (noopObserver) StepFailed(readiness.Step, error) {}

// ---- a test-only ASYNC Node Type, for the async Node callback contract ----
//
// The real image_generation Node (internal/nodes/imagegeneration) is registered above (and
// media_output alongside it, so a Text->Image->Media Output graph validates through
// POST /definitions -- see TestAPI_CreateDefinition_AIGCGraph_WithMediaOutput_Validates),
// but it still cannot be driven through the full HTTP /definitions + /runs flow here to
// completion: its Dispatch call needs a live HTTP endpoint for mocktask to hit, and this
// package has none. So this package separately
// registers its own minimal Text->Text ASYNC Node Type directly (not under
// internal/nodes), which composes as text_input -> test_async_echo -> text_output. That
// fixture -- not image_generation -- is what exercises a Run's actual dispatch/callback
// HTTP round trip in this package; production wiring of image_generation + mocktask against
// a real Provider endpoint is exercised by cmd/emberling's own assembly.

// testAsyncNodeType is the stable Node Type string this package registers.
const testAsyncNodeType = "test_async_echo"

// testAsyncConfigSchema declares no config fields: this fixture only needs the ASYNC
// dispatch/callback shape, not a real config contract.
const testAsyncConfigSchema = `{"type":"object","additionalProperties":false,"properties":{}}`

// testAsyncDispatch is one call fakeAsyncDispatcher recorded: the plaintext callback
// token production code handed it, and the external task id it minted in response. Tests
// read Token to drive POST /api/callbacks directly, exactly as a real Provider would have
// received it in registry.CallbackContext.
type testAsyncDispatch struct {
	Token          string
	ExternalTaskID string
}

// fakeAsyncDispatcher stands in for the Provider side of testAsyncNodeType's dispatch.
// BeforeReturn is installed the same way mockmodel.Provider.BeforeReturn is: a barrier
// that stalls this call, at the exact point production code invokes external code (after
// the STARTED Attempt already committed, before the DISPATCHED Attempt / Callback Binding
// commit), so a test can capture a valid token and call POST /api/callbacks while no
// Callback Binding exists yet.
type fakeAsyncDispatcher struct {
	BeforeReturn func(ctx context.Context)

	mu      sync.Mutex
	calls   int
	history []testAsyncDispatch
}

func (d *fakeAsyncDispatcher) dispatch(ctx context.Context, callback registry.CallbackContext) registry.ExternalTask {
	d.mu.Lock()
	d.calls++
	task := registry.ExternalTask{
		ProviderID:     "test-async-provider",
		ExternalTaskID: fmt.Sprintf("test-task-%d", d.calls),
	}
	d.history = append(d.history, testAsyncDispatch{Token: callback.Token, ExternalTaskID: task.ExternalTaskID})
	d.mu.Unlock()

	if d.BeforeReturn != nil {
		d.BeforeReturn(ctx)
	}
	return task
}

// last returns the most recently recorded dispatch, or the zero value if dispatch was
// never called. Safe to call only after a barrier installed as BeforeReturn has confirmed
// (via waitEntered) that the call it guards has already recorded its entry.
func (d *fakeAsyncDispatcher) last() testAsyncDispatch {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.history) == 0 {
		return testAsyncDispatch{}
	}
	return d.history[len(d.history)-1]
}

// testAsyncCallbackPayload is the callback body testAsyncExecutor.OnCallback interprets,
// mirroring internal/nodes/imagegeneration's own status/error discriminated shape:
// "SUCCEEDED" republishes Output on the node's `out` port, "FAILED" fails the
// Attempt through a *registry.ProviderFailure, and anything else is a payload this
// Executor cannot interpret.
type testAsyncCallbackPayload struct {
	Status string `json:"status"`
	Output string `json:"output"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// testAsyncExecutor implements registry.AsyncNodeExecutor for testAsyncNodeType.
type testAsyncExecutor struct {
	dispatcher *fakeAsyncDispatcher
}

func (testAsyncExecutor) ValidateSemantics(context.Context, map[string]any) error { return nil }

// Execute dispatches through dispatcher and returns DISPATCHED; it never returns a
// COMPLETED result, matching the real async Node contract.
func (e testAsyncExecutor) Execute(ctx context.Context, input registry.NodeInput, _ map[string]any) (registry.NodeResult, error) {
	if input.Callback == nil {
		return registry.NodeResult{}, fmt.Errorf("test_async_echo: asynchronous dispatch requires a callback context")
	}
	if _, present := input.Port("in"); !present {
		return registry.NodeResult{}, fmt.Errorf("test_async_echo: required input port %q is missing", "in")
	}
	task := e.dispatcher.dispatch(ctx, *input.Callback)
	return registry.NodeResult{Kind: registry.NodeResultDispatched, ExternalTask: &task}, nil
}

func (testAsyncExecutor) OnCallback(_ context.Context, state registry.NodeAsyncState, payload []byte) (registry.NodeOutput, error) {
	var body testAsyncCallbackPayload
	if err := json.Unmarshal(payload, &body); err != nil {
		return registry.NodeOutput{}, fmt.Errorf("test_async_echo: callback payload for external task %s is not a JSON object: %w", state.ExternalTask.ExternalTaskID, err)
	}
	switch body.Status {
	case "SUCCEEDED":
		encoded, err := json.Marshal(body.Output)
		if err != nil {
			return registry.NodeOutput{}, fmt.Errorf("test_async_echo: encode output: %w", err)
		}
		return registry.NodeOutput{Ports: map[string]json.RawMessage{"out": encoded}}, nil
	case "FAILED":
		code := "PROVIDER_TASK_FAILED"
		message := "provider reported the external task failed"
		if body.Error != nil {
			if body.Error.Code != "" {
				code = body.Error.Code
			}
			if body.Error.Message != "" {
				message = body.Error.Message
			}
		}
		return registry.NodeOutput{}, &registry.ProviderFailure{Err: domain.ExecutionError{Code: code, Message: message}}
	default:
		return registry.NodeOutput{}, fmt.Errorf("test_async_echo: callback payload for external task %s carries status %q, want SUCCEEDED or FAILED", state.ExternalTask.ExternalTaskID, body.Status)
	}
}

// testAsyncNodeRegistration returns the NodeRegistration bound to dispatcher, ready to be
// added alongside the built-in Node Types in newTestEnvWithOptions.
func testAsyncNodeRegistration(dispatcher *fakeAsyncDispatcher) registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          testAsyncNodeType,
			DisplayName:   "Test Async Echo",
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionAsync,
			Inputs:        []domain.PortMetadata{{Name: "in", DataType: domain.PortTypeText, Required: true}},
			Outputs:       []domain.PortMetadata{{Name: "out", DataType: domain.PortTypeText, Required: true}},
			ConfigSchema:  json.RawMessage(testAsyncConfigSchema),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		},
		Binding: registry.ExecutorBinding{Executor: testAsyncExecutor{dispatcher: dispatcher}},
	}
}

// asyncEchoDefinitionRequest builds a compilable Definition graph
// (text_input -> test_async_echo -> text_output) around testAsyncNodeType, so a contract
// test can create a Run that dispatches, waits on a callback and resumes through the real
// HTTP surface end-to-end.
func asyncEchoDefinitionRequest() map[string]any {
	return map[string]any{
		"name":        "Async Echo",
		"description": "",
		"nodes": []map[string]any{
			{"id": "in", "type": "text_input", "config": map[string]any{"inputKey": "prompt", "required": true}},
			{"id": "async", "type": testAsyncNodeType, "config": map[string]any{}},
			{"id": "out", "type": "text_output", "config": map[string]any{}},
		},
		"edges": []map[string]any{
			{"id": "e1", "source": "in", "sourceHandle": "text", "target": "async", "targetHandle": "in"},
			{"id": "e2", "source": "async", "sourceHandle": "out", "target": "out", "targetHandle": "text"},
		},
	}
}

// aigcMediaGraphRequest builds the compilable Definition graph
// text_input -> prompt_template -> image_generation (prompt) -> media_output.image, plus
// prompt_template -> text_generation -> media_output.caption: the AIGC scenario's shape
// (test/fixtures/definitions/aigc_media.json) minus its Reference Image
// edge, so that image_generation's optional `reference` input port
// (internal/nodes/imagegeneration/node.go) is proven to validate while unwired. The Run
// that does supply one is TestE2E_AIGC_ReferenceImage_... in e2e_async_test.go.
func aigcMediaGraphRequest() map[string]any {
	return map[string]any{
		"name":        "AIGC Media Generation (no image_input)",
		"description": "",
		"nodes": []map[string]any{
			{"id": "brief", "type": "text_input", "config": map[string]any{"inputKey": "brief", "required": true}},
			{"id": "prompt", "type": "prompt_template", "config": map[string]any{"template": "{{text}}"}},
			{"id": "rewrite", "type": "text_generation", "config": map[string]any{"modelId": "text-model-v1"}},
			{"id": "image", "type": "image_generation", "config": map[string]any{"modelId": "image-model-v1", "width": float64(1024)}},
			{"id": "caption", "type": "text_generation", "config": map[string]any{"modelId": "text-model-v1"}},
			{"id": "output", "type": "media_output", "config": map[string]any{}},
		},
		"edges": []map[string]any{
			{"id": "e1", "source": "brief", "sourceHandle": "text", "target": "prompt", "targetHandle": "text"},
			{"id": "e2", "source": "prompt", "sourceHandle": "text", "target": "rewrite", "targetHandle": "prompt"},
			{"id": "e3", "source": "rewrite", "sourceHandle": "text", "target": "image", "targetHandle": "prompt"},
			{"id": "e4", "source": "rewrite", "sourceHandle": "text", "target": "caption", "targetHandle": "prompt"},
			{"id": "e5", "source": "image", "sourceHandle": "image", "target": "output", "targetHandle": "image"},
			{"id": "e6", "source": "caption", "sourceHandle": "text", "target": "output", "targetHandle": "caption"},
		},
	}
}

// documentProcessingFixture is the same fixture internal/test/integration uses: four
// real Node Types (text_input -> prompt_template -> text_generation -> text_generation ->
// text_output) bound to the real Mock Model Provider, so a Run created against it
// executes through the genuine production pipeline, not a test double.
type documentProcessingFixture struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Nodes       []domain.Node `json:"nodes"`
	Edges       []domain.Edge `json:"edges"`
}

func loadDocumentProcessingFixture(t *testing.T) documentProcessingFixture {
	t.Helper()
	raw, err := os.ReadFile("../fixtures/definitions/document_processing.json")
	if err != nil {
		t.Fatalf("read document_processing.json: %v", err)
	}
	var fx documentProcessingFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("unmarshal document_processing.json: %v", err)
	}
	return fx
}

// cyclicDefinitionRequest is a two-node cycle using a registered Node Type
// (prompt_template), so Compile reaches the Graph stage and fails with DAG_HAS_CYCLE
// (a 422-family code) rather than an earlier UNKNOWN_NODE_TYPE.
func cyclicDefinitionRequest() map[string]any {
	return map[string]any{
		"name":        "Cyclic",
		"description": "",
		"nodes": []map[string]any{
			{"id": "a", "type": "prompt_template", "config": map[string]any{"template": "{{text}}"}},
			{"id": "b", "type": "prompt_template", "config": map[string]any{"template": "{{text}}"}},
		},
		"edges": []map[string]any{
			{"id": "e1", "source": "a", "sourceHandle": "text", "target": "b", "targetHandle": "text"},
			{"id": "e2", "source": "b", "sourceHandle": "text", "target": "a", "targetHandle": "text"},
		},
	}
}

// ---- small HTTP helpers shared by every test in this package ----

func (e *testEnv) doJSON(t *testing.T, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.server.URL+path, reqBody)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp, respBody
}

func decodeBody[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode response body: %v\nbody: %s", err, raw)
	}
	return v
}

// waitForTerminal polls GET /api/runs/{id} until Run.status is terminal or timeout
// elapses. This is ordinary HTTP-client polling of a real async server (the work Pool
// runs in its own goroutine, exactly as it would in production), not a manufactured race
// in the sense CLAUDE.md's testing standard warns against: there is no injectable barrier
// for "the Pool finished its drain loop" from outside the process.
func (e *testEnv) waitForTerminal(t *testing.T, runID string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, raw := e.doJSON(t, http.MethodGet, "/api/runs/"+runID, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/runs/%s: status %d body %s", runID, resp.StatusCode, raw)
		}
		snap := decodeBody[map[string]any](t, raw)
		run, _ := snap["run"].(map[string]any)
		if status, _ := run["status"].(string); status == "COMPLETED" || status == "FAILED" {
			return snap
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach a terminal status within %s", runID, timeout)
	return nil
}

func errorCode(t *testing.T, raw []byte) string {
	t.Helper()
	env := decodeBody[map[string]any](t, raw)
	errBody, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("response is not an error envelope: %s", raw)
	}
	code, _ := errBody["code"].(string)
	return code
}

// ---- SSE test support: barriers, a notifier that can drop, and frame reading ----

// barrier stalls the Nth mockmodel.Provider.BeforeReturn call so a test can create a Run,
// let it commit some but not all Events, observe that partial state (e.g. a Snapshot's
// lastSeq), and only then let execution continue — a genuine synchronization primitive
// (CLAUDE.md: "Concurrency and failure tests use explicit barriers or injected hooks. Do
// not use arbitrary sleeps to manufacture races.").
type barrier struct {
	triggerOnCall int32
	calls         int32
	entered       chan struct{}
	release       chan struct{}
}

// newBarrierAtCall builds a barrier that stalls the n-th call (1-indexed) into
// beforeReturn.
func newBarrierAtCall(n int) *barrier {
	return &barrier{
		triggerOnCall: int32(n),
		entered:       make(chan struct{}),
		release:       make(chan struct{}),
	}
}

// beforeReturn is installed as mockmodel.Provider.BeforeReturn. On its triggerOnCall-th
// invocation it closes entered (so the test knows execution has stalled at this call) and
// blocks until Release is called; every other call passes straight through.
func (b *barrier) beforeReturn(ctx context.Context) {
	n := atomic.AddInt32(&b.calls, 1)
	if n != b.triggerOnCall {
		return
	}
	close(b.entered)
	select {
	case <-b.release:
	case <-ctx.Done():
	}
}

// waitEntered blocks until beforeReturn's triggerOnCall-th invocation has stalled, or
// fails the test if that does not happen within timeout.
func (b *barrier) waitEntered(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(timeout):
		t.Fatalf("barrier: call %d did not arrive within %s", b.triggerOnCall, timeout)
	}
}

// Release unblocks the stalled call, letting execution continue.
func (b *barrier) Release() {
	close(b.release)
}

// dropNotifier wraps a *api.Hub (still the api.Deps.Notifier, so Subscribe/unsubscribe
// bookkeeping is real) and, when wired as service.Deps.Notifier instead of the raw Hub,
// silently swallows EventsCommitted for one target Run ID. This proves the SSE stream's
// PollInterval ticker is a genuine fallback path (CLAUDE.md: "PostgreSQL Event rows are the
// authority for SSE. In-process notification only wakes a cursor query."), not merely an
// optimization that happens to be exercised because the notifier always fires.
type dropNotifier struct {
	hub    *api.Hub
	dropID atomic.Value // string
}

func newDropNotifier(hub *api.Hub) *dropNotifier {
	return &dropNotifier{hub: hub}
}

// SetDropRunID marks runID's EventsCommitted calls to be swallowed from this point on.
func (d *dropNotifier) SetDropRunID(runID string) {
	d.dropID.Store(runID)
}

func (d *dropNotifier) EventsCommitted(runID string, lastSeq int64) {
	if id, ok := d.dropID.Load().(string); ok && id == runID {
		return
	}
	d.hub.EventsCommitted(runID, lastSeq)
}

// sseFrame is one parsed "id / event / data" SSE frame. Bare comment lines (the stream's
// ": keepalive" idle frame) and the blank lines that separate frames are consumed by
// readSSEFrame, not surfaced as a frame, so tests asserting on the sequence of genuine
// Events never have to special-case keepalives.
type sseFrame struct {
	id    int64
	event string
	data  string
}

// readSSEFrame reads lines from r until one complete SSE frame (terminated by a blank
// line) has been assembled, skipping over comment-only frames (lines starting with ':',
// e.g. the stream's keepalive) along the way. It returns io.EOF (or another read error)
// once the underlying connection closes without another frame, and a plain error (never
// t.Fatalf) for a malformed frame: at least one caller (waitForNextSSEFrame) invokes this
// from a goroutine other than the test's own, where testing.T.FailNow is documented as
// illegal to call.
func readSSEFrame(r *bufio.Reader) (sseFrame, error) {
	var frame sseFrame
	sawField := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return sseFrame{}, err
		}
		line = strings.TrimRight(line, "\r\n")

		switch {
		case line == "":
			if sawField {
				return frame, nil
			}
			// Blank line with no fields yet: either a stray separator or the tail of a
			// comment-only frame already skipped below. Keep reading.
			continue
		case strings.HasPrefix(line, ":"):
			// Comment frame (keepalive). Not a field; if one was already in progress,
			// this line does not belong to it, but the stream never interleaves a
			// comment mid-frame, so it is safe to just continue reading.
			continue
		case strings.HasPrefix(line, "id:"):
			sawField = true
			v := strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			id, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return sseFrame{}, fmt.Errorf("readSSEFrame: bad id field %q: %w", v, err)
			}
			frame.id = id
		case strings.HasPrefix(line, "event:"):
			sawField = true
			frame.event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			sawField = true
			frame.data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		default:
			return sseFrame{}, fmt.Errorf("readSSEFrame: unrecognized line %q", line)
		}
	}
}

// openSSE issues GET {server}/api/runs/{runID}/events with Accept: text/event-stream (and,
// optionally, afterSeq / Last-Event-ID), returning the live response and a *bufio.Reader
// over its body for readSSEFrame. The caller must close resp.Body (directly or via
// t.Cleanup) once done.
func (e *testEnv) openSSE(t *testing.T, runID string, afterSeq int64, lastEventID string) (*http.Response, *bufio.Reader) {
	t.Helper()
	query := ""
	if afterSeq > 0 {
		query = fmt.Sprintf("afterSeq=%d", afterSeq)
	}
	return e.openSSERaw(t, runID, query, lastEventID)
}

// openSSERaw is openSSE with the query string passed through verbatim, for tests that
// need to assert on a specific combination of query parameters (e.g. an afterSeq present
// alongside Last-Event-ID, to prove the header takes precedence) rather than the single
// afterSeq openSSE itself supports.
func (e *testEnv) openSSERaw(t *testing.T, runID string, rawQuery string, lastEventID string) (*http.Response, *bufio.Reader) {
	t.Helper()
	path := fmt.Sprintf("/api/runs/%s/events", runID)
	if rawQuery != "" {
		path += "?" + rawQuery
	}
	req, err := http.NewRequest(http.MethodGet, e.server.URL+path, nil)
	if err != nil {
		t.Fatalf("build SSE request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s (SSE): %v", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("GET %s (SSE) status = %d, body=%s", path, resp.StatusCode, body)
	}
	return resp, bufio.NewReader(resp.Body)
}
