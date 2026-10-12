//go:build integration

// Executor failure redaction against PostgreSQL. A failed Provider dispatch is persisted
// as an ExecutionError on the Attempt and copied into NODE_FAILED / NODE_RETRYING Events
// and the read projections. The rule proven here: the Provider host, the request URL and
// its query string (where a credential may travel) and raw transport text never reach any
// of those facts, while the error code and the uncertain/definite classification that
// decides a retry are unchanged. The raw text reaches only the backend log, with every
// URL's query string and userinfo stripped.
//
// The real image_generation Node Executor dispatches through the real mocktask Adapter;
// only the Adapter's http.RoundTripper is replaced, so each transport failure is produced
// deterministically without a listener, a DNS lookup or a sleep.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/adapters/mocktask"
	"github.com/leungll/Emberling/backend/internal/adapters/openaimodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/imagegeneration"
	"github.com/leungll/Emberling/backend/internal/nodes/mediaoutput"
	"github.com/leungll/Emberling/backend/internal/nodes/textinput"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// redactionSecretURL is the request URL the failing transport reports: an internal host
// and a query value standing for a credential.
const redactionSecretURL = "http://mockprovider:9101/v1/tasks?key=s3cr3t"

// redactionLeaks are the fragments no persisted fact may contain.
var redactionLeaks = []string{"s3cr3t", "key=", "mockprovider", "9101", `Post "`, "connection refused", "context deadline exceeded"}

// redactionTransport fails every request with err(req).
type redactionTransport func(*http.Request) error

func (f redactionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, f(req)
}

// lockedBuffer collects log output written by the service.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type redactionHarness struct {
	t     *testing.T
	ctx   context.Context
	uow   store.UnitOfWork
	svc   *service.ExecutionService
	query *service.QueryService
	def   domain.Definition
	logs  *lockedBuffer
}

// newRedactionHarness wires text_input -> image_generation -> media_output, with the
// image node dispatching through a mocktask Adapter whose transport fails with failure.
//
// The image registration's SideEffectPolicy is replaced by EXTERNAL+UNKNOWN so the
// Adapter's uncertain/definite classification becomes observable in PostgreSQL: an
// uncertain failure forbids the retry its ExecutionPolicy would otherwise allow, a
// definite one schedules it.
func newRedactionHarness(t *testing.T, clockStart time.Time, failure func(*http.Request) error) *redactionHarness {
	t.Helper()
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	models := registry.NewModelRegistry()
	if err := models.Register(ctx, mockmodel.NewProvider()); err != nil {
		t.Fatalf("register mock model provider: %v", err)
	}
	adapter := mocktask.New("http://mockprovider:9101", &http.Client{Transport: redactionTransport(failure)})
	image := imagegeneration.Registration(models, adapter)
	image.Metadata.SideEffect = domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown}

	nodes := registry.NewNodeRegistry()
	for _, reg := range []registry.NodeRegistration{textinput.Registration(), mediaoutput.Registration(), image} {
		if err := nodes.Register(reg); err != nil {
			t.Fatalf("register node type: %v", err)
		}
	}

	clock := newExecClock(clockStart)
	logs := &lockedBuffer{}
	deps := service.Deps{
		UoW:      uow,
		Nodes:    nodes,
		Models:   models,
		Compiler: runtime.NewCompiler(nodes, clock),
		Clock:    clock,
		IDs:      newExecForcedIDs(),
		Logger:   slog.New(slog.NewTextHandler(logs, nil)),
		Callback: service.CallbackConfig{
			BaseURL:       asyncBaseURL,
			SigningSecret: []byte(asyncSigningSecret),
			PendingTTL:    time.Hour,
		},
	}
	h := &redactionHarness{
		t: t, ctx: ctx, uow: uow, logs: logs,
		svc:   service.NewExecutionService(deps),
		query: service.NewQueryService(deps),
	}
	h.def = h.saveDefinition(runtime.NewCompiler(nodes, clock))
	return h
}

func (h *redactionHarness) saveDefinition(compiler *runtime.Compiler) domain.Definition {
	h.t.Helper()
	def := domain.Definition{
		WorkflowID:  "wf_redaction_" + strings.ToLower(strings.ReplaceAll(h.t.Name(), "/", "_")),
		Version:     1,
		Name:        "Executor failure redaction",
		Description: "text_input -> image_generation -> media_output",
		Nodes: []domain.Node{
			{ID: "brief", Type: "text_input", Name: "Brief", Config: json.RawMessage(`{"inputKey":"brief","required":true}`)},
			{
				ID: "image", Type: "image_generation", Name: "Image",
				Config:          json.RawMessage(`{"modelId":"` + mockmodel.ImageModelID + `","width":1024}`),
				ExecutionPolicy: &domain.ExecutionPolicy{MaxAttempts: 3, Backoff: domain.BackoffFixed, TimeoutMs: 1000},
			},
			{ID: "output", Type: "media_output", Name: "Output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "brief", SourceHandle: "text", Target: "image", TargetHandle: "prompt"},
			{ID: "e2", Source: "image", SourceHandle: "image", Target: "output", TargetHandle: "image"},
			{ID: "e3", Source: "brief", SourceHandle: "text", Target: "output", TargetHandle: "caption"},
		},
		CreatedAt: fixtureTime,
	}
	plan, err := compiler.Compile(h.ctx, def)
	if err != nil {
		h.t.Fatalf("compile redaction definition: %v", err)
	}
	def.RunInputSchema = plan.RunInputSchema
	def.Validation = plan.Validation
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Definitions().Save(ctx, def)
	}); err != nil {
		h.t.Fatalf("save redaction definition: %v", err)
	}
	return def
}

// executeImageNode creates a Run and executes every Node up to and including the image
// node's first Attempt, which fails at the Provider transport.
func (h *redactionHarness) executeImageNode() (domain.Run, service.AdvanceOutcome) {
	h.t.Helper()
	run, err := h.svc.CreateRun(h.ctx, service.CreateRun{
		WorkflowID: h.def.WorkflowID, DefinitionVersion: h.def.Version,
		Input: json.RawMessage(`{"brief":"a small ember creature"}`),
	})
	if err != nil {
		h.t.Fatalf("create run: %v", err)
	}
	for i := 0; i < 5; i++ {
		outcome, err := h.svc.Advance(h.ctx, run.ID)
		if err != nil || !outcome.Claimed {
			h.t.Fatalf("advance: claimed=%v err=%v", outcome.Claimed, err)
		}
		if err := h.svc.Execute(h.ctx, outcome); err != nil {
			h.t.Fatalf("execute %s: %v", outcome.NodeType, err)
		}
		if outcome.NodeType == "image_generation" {
			return run, outcome
		}
	}
	h.t.Fatal("the image node was never claimed")
	return domain.Run{}, service.AdvanceOutcome{}
}

// assertNoLeak fails when any persisted or projected fact of run carries a leak fragment:
// the Attempt error, every Event payload, the Snapshot and the NodeRun detail projection.
func (h *redactionHarness) assertNoLeak(run domain.Run, outcome service.AdvanceOutcome) {
	h.t.Helper()
	check := func(what string, raw []byte) {
		h.t.Helper()
		for _, leak := range redactionLeaks {
			if bytes.Contains(raw, []byte(leak)) {
				h.t.Errorf("%s carries %q: %s", what, leak, raw)
			}
		}
	}

	attempt := getAttempt(h.ctx, h.t, h.uow, outcome.AttemptID)
	if attempt.Error == nil {
		h.t.Fatalf("Attempt %s has no error", outcome.AttemptID)
	}
	check("Attempt error message", []byte(attempt.Error.Message))

	events := listEvents(h.ctx, h.t, h.uow, run.ID)
	if len(events) == 0 {
		h.t.Fatal("Run has no Events")
	}
	for _, ev := range events {
		check(string(ev.Type)+" Event payload", ev.Payload)
	}

	snapshot, err := h.query.Snapshot(h.ctx, run.ID)
	if err != nil {
		h.t.Fatalf("snapshot: %v", err)
	}
	check("Run Snapshot", mustJSON(h.t, snapshot))
	detail, err := h.query.NodeRunDetail(h.ctx, run.ID, outcome.NodeRunID)
	if err != nil {
		h.t.Fatalf("node run detail: %v", err)
	}
	check("NodeRun detail", mustJSON(h.t, detail))
}

// assertLogged proves the developer log recorded the failure once, with stable
// identifiers and the transport detail, but with the URL's query string stripped.
func (h *redactionHarness) assertLogged(outcome service.AdvanceOutcome) {
	h.t.Helper()
	logs := h.logs.String()
	if got := strings.Count(logs, "node executor failed"); got != 1 {
		h.t.Errorf("executor failure log lines = %d, want 1: %s", got, logs)
	}
	for _, want := range []string{"attempt_id=" + outcome.AttemptID, "code=EXECUTOR_ERROR", "http://mockprovider:9101/v1/tasks"} {
		if !strings.Contains(logs, want) {
			h.t.Errorf("log lacks %q: %s", want, logs)
		}
	}
	for _, leak := range []string{"s3cr3t", "key="} {
		if strings.Contains(logs, leak) {
			h.t.Errorf("log carries %q: %s", leak, logs)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return raw
}

// TestNodeExecute_ProviderDeadlineWithSecretURL_PersistsStableMessageAndStaysUncertain
// reproduces a dispatch cut off by the Attempt deadline. The Attempt's deadline is
// already in the past when the Executor runs, so the HTTP client fails at once with the
// caller's deadline; the failure is persisted as EXECUTOR_ERROR with a stable message,
// still classified uncertain (no retry for EXTERNAL+UNKNOWN), and nothing persisted
// names the Provider host, URL, query or transport text.
func TestNodeExecute_ProviderDeadlineWithSecretURL_PersistsStableMessageAndStaysUncertain(t *testing.T) {
	// The Attempt deadline is the Runtime clock plus TimeoutMs; a clock an hour behind
	// real time puts it in the past for the real context the HTTP client honours.
	h := newRedactionHarness(t, time.Now().UTC().Add(-time.Hour), func(r *http.Request) error {
		return &url.Error{Op: "Post", URL: redactionSecretURL, Err: r.Context().Err()}
	})

	run, outcome := h.executeImageNode()

	attempt := getAttempt(h.ctx, t, h.uow, outcome.AttemptID)
	if attempt.Status != domain.NodeAttemptFailed || attempt.Error == nil || attempt.Error.Code != "EXECUTOR_ERROR" {
		t.Fatalf("Attempt after a deadline-cut dispatch: want FAILED EXECUTOR_ERROR, got %s %+v", attempt.Status, attempt.Error)
	}
	const want = `image_generation: dispatch: mocktask: dispatch to provider "mock-task-provider-v1": send request: provider request timed out before the deadline`
	if attempt.Error.Message != want {
		t.Errorf("Attempt error message = %q, want %q", attempt.Error.Message, want)
	}
	nodeRun := getNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if nodeRun.Status != domain.NodeRunFailed || nodeRun.NextAttemptAt != nil {
		t.Errorf("NodeRun after an uncertain EXTERNAL+UNKNOWN failure: want FAILED with no retry, got %s next=%v", nodeRun.Status, nodeRun.NextAttemptAt)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	if got := asyncNodeEventsOfType(events, outcome.NodeRunID, domain.EventNodeRetrying); len(got) != 0 {
		t.Errorf("NODE_RETRYING after an uncertain failure: want none, got %v", execEventTypes(events))
	}
	if got := asyncNodeEventsOfType(events, outcome.NodeRunID, domain.EventNodeFailed); len(got) != 1 {
		t.Errorf("NODE_FAILED after an uncertain failure: want 1, got %v", execEventTypes(events))
	}

	h.assertNoLeak(run, outcome)
	h.assertLogged(outcome)
}

// TestNodeExecute_ProviderDialFailureWithSecretURL_PersistsStableMessageAndStaysDefinite
// is the definite half: a dial failure proves the request never reached the Provider, so
// the retry its ExecutionPolicy allows is scheduled, and again nothing persisted names the
// Provider host, URL, query or transport text.
func TestNodeExecute_ProviderDialFailureWithSecretURL_PersistsStableMessageAndStaysDefinite(t *testing.T) {
	h := newRedactionHarness(t, time.Now().UTC(), func(*http.Request) error {
		return &url.Error{Op: "Post", URL: redactionSecretURL, Err: &net.OpError{
			Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9101},
			Err: errors.New("connect: connection refused"),
		}}
	})

	run, outcome := h.executeImageNode()

	attempt := getAttempt(h.ctx, t, h.uow, outcome.AttemptID)
	if attempt.Status != domain.NodeAttemptFailed || attempt.Error == nil || attempt.Error.Code != "EXECUTOR_ERROR" {
		t.Fatalf("Attempt after a refused dial: want FAILED EXECUTOR_ERROR, got %s %+v", attempt.Status, attempt.Error)
	}
	const want = `image_generation: dispatch: mocktask: dispatch to provider "mock-task-provider-v1": send request: provider request failed: transport error`
	if attempt.Error.Message != want {
		t.Errorf("Attempt error message = %q, want %q", attempt.Error.Message, want)
	}
	nodeRun := getNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if nodeRun.Status != domain.NodeRunRunning || nodeRun.NextAttemptAt == nil {
		t.Errorf("NodeRun after a definite failure with attempts left: want RUNNING with a retry scheduled, got %s next=%v", nodeRun.Status, nodeRun.NextAttemptAt)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	if got := asyncNodeEventsOfType(events, outcome.NodeRunID, domain.EventNodeRetrying); len(got) != 1 {
		t.Errorf("NODE_RETRYING after a definite failure: want 1, got %v", execEventTypes(events))
	}

	h.assertNoLeak(run, outcome)
	h.assertLogged(outcome)
	if logs := h.logs.String(); !strings.Contains(logs, "connection refused") {
		t.Errorf("log lacks the raw transport detail: %s", logs)
	}
}

// TestAgentTurn_ModelTransportFailureWithSecretURL_PersistsStableModelError drives a real
// openaimodel Adapter whose transport fails with a dial error naming an internal host and
// a credential-carrying query. The Agent Run terminates as MODEL_ERROR with the Adapter's
// stable message, and neither the Turn, the Agent Run nor any Event of the Run names the
// host, the URL, its query or the raw transport text.
func TestAgentTurn_ModelTransportFailureWithSecretURL_PersistsStableModelError(t *testing.T) {
	const modelID = "redaction-model-1.0"
	provider, err := openaimodel.New(openaimodel.Config{
		ModelID:     modelID,
		DisplayName: modelID,
		BaseURL:     "http://llm.internal:8443/v1",
		APIKey:      "sk-redaction-test",
		Model:       "vendor/redaction",
		HTTPClient: &http.Client{Transport: redactionTransport(func(*http.Request) error {
			return &url.Error{Op: "Post", URL: "http://llm.internal:8443/v1/chat/completions?key=s3cr3t", Err: &net.OpError{
				Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused"),
			}}
		})},
		DecisionSchema: registry.DecisionSchema(),
	})
	if err != nil {
		t.Fatalf("openaimodel.New: %v", err)
	}
	h := newAgentHarness(t, agentHarnessOptions{ModelProviders: []registry.ModelProvider{provider}})
	def := h.saveDefinition(withAgentModel(t, agentLoopDefinition("wf-agent-model-redaction"), "node_agent", modelID))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	h.execute(outcome)

	assertAgentTerminated(t, h, run.ID, outcome, domain.TerminationModelError)
	const want = "openaimodel: send request failed: transport error"
	turn := getAgentTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if turn.Error == nil || turn.Error.Code != "MODEL_ERROR" || turn.Error.Message != want {
		t.Errorf("turn error = %+v, want MODEL_ERROR %q", turn.Error, want)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	leaks := []string{"s3cr3t", "key=", "llm.internal", "8443", `Post "`, "connection refused", "sk-redaction-test"}
	facts := map[string][]byte{"Agent Run error": mustJSON(t, agentRun.Error), "turn error": mustJSON(t, turn.Error)}
	for _, ev := range listEvents(h.ctx, t, h.uow, run.ID) {
		facts[string(ev.Type)+" Event payload (seq "+strconv.FormatInt(ev.Seq, 10)+")"] = ev.Payload
	}
	for what, raw := range facts {
		for _, leak := range leaks {
			if bytes.Contains(raw, []byte(leak)) {
				t.Errorf("%s carries %q: %s", what, leak, raw)
			}
		}
	}
}
