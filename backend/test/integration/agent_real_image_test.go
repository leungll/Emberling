//go:build integration

// Real image model failure tests: generate_image calls a real image model through the
// OpenRouter Images API Adapter, here pointed at the Mock Provider's OpenRouter-shaped
// endpoint. The call is an uncertain external side effect, so whatever ends it -- the
// Agent deadline, the image client's own timeout, a crash or a shutdown while it is in
// flight -- the Provider receives exactly the one request the claim allowed and never a
// second one, and a call past the generation limit never reaches it at all.
//
// The Mock Provider's barrier holds the request at the Provider and its held hook says
// when, so every test acts while the call is provably in flight; its dispatch record is
// the external evidence of how many requests arrived. They share the agentHarness of
// agent_loop_test.go and the generation fixtures of agent_generation_limit_test.go.
package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/adapters/openrouterimage"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/fixture/mockprovider"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// realImageModelID is the stable Model ID the Agent selects; realImageUpstreamModel is
// the upstream model name only the deployment configuration knows.
const (
	realImageModelID       = "image-real-1"
	realImageUpstreamModel = "vendor/image-real"
	realImageKey           = "sk-real-image-integration-secret"
)

// realImageHeldWait bounds how long a test waits for the Provider to hold the call.
const realImageHeldWait = 10 * time.Second

// realImageUpstream is a Mock Provider with test controls whose Images API endpoint
// stands in for OpenRouter.
type realImageUpstream struct {
	url    string
	server *mockprovider.Server
	held   chan string
}

func newRealImageUpstream(t *testing.T) *realImageUpstream {
	t.Helper()
	record, err := mockprovider.OpenRecord(filepath.Join(t.TempDir(), "dispatch.jsonl"))
	if err != nil {
		t.Fatalf("open mock provider record: %v", err)
	}
	dispatcher := mockprovider.NewDispatcher(nil)
	held := make(chan string, 4)
	server := mockprovider.NewServer(dispatcher, mockprovider.WithTestControls(record), mockprovider.WithHeldHook(held))
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		// A request a test left held must not keep Close waiting forever.
		server.StopHolding()
		httpServer.Close()
		_ = dispatcher.Shutdown(context.Background())
		_ = record.Close()
	})
	return &realImageUpstream{url: httpServer.URL, server: server, held: held}
}

func (u *realImageUpstream) control(t *testing.T, path, body string) {
	t.Helper()
	resp, err := http.Post(u.url+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s status = %d, want 200", path, resp.StatusCode)
	}
}

// holdImages makes the Provider hold every image request until release.
func (u *realImageUpstream) holdImages(t *testing.T) {
	t.Helper()
	u.control(t, "/control/pause", `{"kinds":["image"]}`)
}

// waitHeld blocks until the Provider holds one image request.
func (u *realImageUpstream) waitHeld(t *testing.T) {
	t.Helper()
	select {
	case <-u.held:
	case <-time.After(realImageHeldWait):
		t.Fatal("the image request never reached the Provider")
	}
}

// imagesRecord is the Mock Provider record's account of Images API requests: how many
// arrived and how many it answered with a generated image.
type imagesRecord struct {
	arrived, generated int
	raw                string
}

func (u *realImageUpstream) imagesRecord(t *testing.T) imagesRecord {
	t.Helper()
	resp, err := http.Get(u.url + "/control/record?after=0")
	if err != nil {
		t.Fatalf("GET /control/record: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /control/record status = %d", resp.StatusCode)
	}
	out := imagesRecord{raw: string(body)}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		var line struct {
			Event    string `json:"event"`
			Kind     string `json:"kind"`
			Scenario string `json:"scenario"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("decode record line %q: %v", scanner.Text(), err)
		}
		switch {
		case line.Kind != "image":
		case line.Event == "arrived" && strings.HasPrefix(line.Scenario, "api=images"):
			out.arrived++
		case line.Event == "generated":
			out.generated++
		}
	}
	return out
}

// newRealImageModel is the OpenRouter Images API Adapter for realImageModelID against the
// Mock Provider, sending through client.
func newRealImageModel(t *testing.T, upstream *realImageUpstream, client *http.Client) *openrouterimage.Provider {
	t.Helper()
	provider, err := openrouterimage.New(openrouterimage.Config{
		ModelID:       realImageModelID,
		DisplayName:   "Real Image",
		BaseURL:       upstream.url + "/api/v1",
		APIKey:        realImageKey,
		Model:         realImageUpstreamModel,
		HTTPClient:    client,
		MaxImageBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("openrouterimage.New: %v", err)
	}
	return provider
}

// realImageGenerator bridges generate_image's generator port to the Images API Adapter
// the way the Backend process wires it: it reads the source photo through the photo
// port, makes exactly one call and hands back the decoded image with what the Provider
// reported about it.
type realImageGenerator struct {
	provider *openrouterimage.Provider
}

func (g realImageGenerator) Generate(ctx context.Context, request generateimage.ImageRequest) (generateimage.GeneratedImage, error) {
	photo, err := request.OpenPhoto(ctx)
	if err != nil {
		return generateimage.GeneratedImage{}, err
	}
	reference, err := io.ReadAll(io.LimitReader(photo.Content, 1<<20))
	_ = photo.Content.Close()
	if err != nil {
		return generateimage.GeneratedImage{}, fmt.Errorf("read photo: %w", err)
	}
	image, err := g.provider.GenerateImage(ctx, openrouterimage.ImageRequest{
		Prompt:             request.Settings.Prompt,
		AspectRatio:        request.Settings.AspectRatio,
		Resolution:         request.Settings.Resolution,
		Seed:               request.Settings.Seed,
		ReferenceImage:     reference,
		ReferenceMediaType: photo.MediaType,
	})
	if err != nil {
		return generateimage.GeneratedImage{}, fmt.Errorf("%s: image model %q: %w", generateimage.ToolName, realImageModelID, err)
	}
	return generateimage.GeneratedImage{Content: io.NopCloser(image.Content), Model: image.Model, Cost: image.Cost}, nil
}

// syntheticPhotos serves every photo id as a small synthetic PNG.
type syntheticPhotos struct{}

func (syntheticPhotos) OpenPhoto(_ context.Context, photoAssetID string) (generateimage.Photo, error) {
	return generateimage.Photo{
		MediaType: "image/png",
		Content:   io.NopCloser(strings.NewReader("\x89PNG\r\n\x1a\n" + photoAssetID)),
	}, nil
}

// realImageWiring is one Backend's real image model: the Adapter registered in the Model
// Registry and the counting generate_image Executor that calls it.
type realImageWiring struct {
	model    *openrouterimage.Provider
	executor *generationExecutor
}

// newRealImageWiring wires generate_image with the mock image model and the real one
// sending through client.
func newRealImageWiring(t *testing.T, upstream *realImageUpstream, client *http.Client) realImageWiring {
	t.Helper()
	model := newRealImageModel(t, upstream, client)
	artifactRoot := t.TempDir()
	artifacts := asset.NewArtifactStore(artifactRoot, 1<<20)
	generators := map[string]generateimage.ImageGenerator{
		mockmodel.ImageModelID: generateimage.NewMockGenerator("http://mock-provider.invalid", &http.Client{}),
		realImageModelID:       realImageGenerator{provider: model},
	}
	return realImageWiring{model: model, executor: &generationExecutor{
		delegate: generateimage.New(generators, syntheticPhotos{}, artifacts), artifacts: artifacts, artifactRoot: artifactRoot,
	}}
}

// options registers the wiring into a harness built from opts; the same wiring passed to
// a restarted harness keeps counting the calls of both.
func (w realImageWiring) options(opts agentHarnessOptions) agentHarnessOptions {
	registration := generateimage.Registration(nil, nil, w.executor.artifacts)
	registration.Executor = w.executor
	opts.Tools = []registry.ToolRegistration{registration}
	opts.ModelProviders = []registry.ModelProvider{w.model}
	return opts
}

// realImageDefinition is the generation fixture graph with the real image model selected.
func realImageDefinition(t *testing.T, workflowID string, limit *int) domain.Definition {
	t.Helper()
	return withAgentConfig(t, generationDefinition(workflowID, limit), "node_agent", map[string]any{
		"imageModelId": realImageModelID,
	})
}

// startRealImageRun scripts the TOOL_CALLs, saves the real image Definition and creates
// the Run.
func startRealImageRun(t *testing.T, h *agentHarness, workflowID string, limit *int, calls ...mockmodel.Scenario) domain.Run {
	t.Helper()
	factScriptToolCalls(h, calls...)
	def := h.saveDefinition(realImageDefinition(t, workflowID, limit))
	return h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
}

// readyRealImageGeneration commits the Run's first TOOL_CALL Decision for generate_image
// with the real image model and stops before its Action is claimed.
func readyRealImageGeneration(t *testing.T, h *agentHarness, stop *agentStopNotifier, workflowID string) (domain.Run, service.AdvanceOutcome, domain.AgentAction) {
	t.Helper()
	run := startRealImageRun(t, h, workflowID, nil, generationCall("photo-a"))
	outcome := stopAtModelCall(t, h, stop, run.ID, 1)
	action := generationActionOfTurn(t, h, factAgentRunID(t, h, outcome), 1)
	if action.Status != domain.AgentActionReady {
		t.Fatalf("generation action = %s, want READY before the claim", action.Status)
	}
	return run, outcome, action
}

// assertAttemptStillStarted proves the call's outcome is still unknown: the Action is
// RUNNING with exactly one STARTED Attempt and no result.
func assertAttemptStillStarted(t *testing.T, h *agentHarness, actionID string) {
	t.Helper()
	if got := getAgentAction(h.ctx, t, h.uow, actionID).Status; got != domain.AgentActionRunning {
		t.Errorf("action = %s, want RUNNING", got)
	}
	attempts := agentToolAttempts(h.ctx, t, h.uow, actionID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptStarted || len(attempts[0].Result) != 0 {
		t.Fatalf("tool attempts = %+v, want exactly one STARTED without a result", attempts)
	}
}

// assertRestartDoesNotResend restarts the Backend over the same database and Provider and
// drives the Reconciler: before the deadline it finds nothing to do, and once the
// deadline has passed it ends the Agent Run as TIMEOUT. Neither pass sends the image
// request again.
func assertRestartDoesNotResend(t *testing.T, h *agentHarness, wiring realImageWiring, upstream *realImageUpstream, runID, actionID string) {
	t.Helper()
	// The Provider stops holding, so a resent request would be answered at once and show
	// in the record instead of waiting at the barrier.
	upstream.control(t, "/control/release", "")
	restarted := newAgentHarness(t, wiring.options(agentHarnessOptions{Pool: h.pool, Clock: h.clock}))
	callsBefore := wiring.executor.calls.Load()

	if report := agentRunOnce(restarted, agentReconciler(restarted)); report.ExpiredAgentRunsFound != 0 {
		t.Fatalf("report = %+v, want no expired Agent Run before the deadline", report)
	}
	assertAttemptStillStarted(t, restarted, actionID)

	restarted.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
	if report := agentRunOnce(restarted, agentReconciler(restarted)); report.ExpiredAgentRunsFound != 1 {
		t.Fatalf("report = %+v, want the one expired Agent Run", report)
	}
	attempts := agentToolAttempts(restarted.ctx, t, restarted.uow, actionID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptFailed || attempts[0].Error == nil || attempts[0].Error.Code != "TIMEOUT" {
		t.Fatalf("tool attempts after the deadline = %+v, want the one Attempt FAILED with TIMEOUT", attempts)
	}
	if got := agentRunRow(restarted.ctx, t, restarted.uow, runID).Status; got != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", got)
	}

	if got := wiring.executor.calls.Load(); got != callsBefore {
		t.Errorf("generate_image executor calls after the restart = %d, want still %d", got, callsBefore)
	}
	if record := upstream.imagesRecord(t); record.arrived != 1 {
		t.Errorf("Images API requests = %d, want exactly 1: the restart must not resend\n%s", record.arrived, record.raw)
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestAgentAction_ImageCallPastDeadline_TimeoutNoSecondRequest covers the Agent deadline
// expiring while the image call waits on the Provider: the deadline ends the call, the
// Agent terminates TIMEOUT rather than TOOL_ERROR, and the Provider saw exactly one
// request, which the caller abandoned.
func TestAgentAction_ImageCallPastDeadline_TimeoutNoSecondRequest(t *testing.T) {
	upstream := newRealImageUpstream(t)
	upstream.holdImages(t)
	wiring := newRealImageWiring(t, upstream, &http.Client{Timeout: 5 * time.Minute})
	// The frozen deadline is a few real seconds away: the call context is bounded by real
	// time, so the deadline can only expire during the call if it lies in the near future
	// when the call starts. The held request then waits for nothing but that deadline.
	const deadlineMargin = 3 * time.Second
	clock := newExecClock(time.Now().UTC().Add(-agentTimeoutMs*time.Millisecond + deadlineMargin))
	h := newAgentHarness(t, wiring.options(agentHarnessOptions{Clock: clock}))
	run := startRealImageRun(t, h, "wf-real-image-deadline", nil, generationCall("photo-a"))
	outcome := h.claimAgentNode(run.ID)

	// Once the call is held at the Provider, the injected clock moves past the deadline
	// too, so the timeout transaction the expiry leads to may commit.
	advanced := make(chan struct{})
	go func() {
		defer close(advanced)
		select {
		case <-upstream.held:
			clock.Advance(agentTimeoutMs * time.Millisecond)
		case <-time.After(realImageHeldWait):
		}
	}()
	h.execute(outcome)
	<-advanced

	action, found := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if !found {
		t.Fatalf("no action was committed for turn %s", outcome.AgentTurnID)
	}
	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptFailed || attempts[0].Error == nil || attempts[0].Error.Code != "TIMEOUT" {
		t.Fatalf("tool attempts = %+v, want exactly one FAILED with TIMEOUT", attempts)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Fatalf("agent run termination = %s, want TIMEOUT, never TOOL_ERROR", agentTermination(agentRun))
	}

	// Nothing that runs later sends the request again.
	if report := agentRunOnce(h, agentReconciler(h)); report.ExpiredAgentRunsFound != 0 {
		t.Errorf("report = %+v, want nothing left to time out", report)
	}
	if got := wiring.executor.calls.Load(); got != 1 {
		t.Errorf("generate_image executor calls = %d, want 1", got)
	}
	if record := upstream.imagesRecord(t); record.arrived != 1 || record.generated != 0 {
		t.Errorf("Images API record = %d arrived %d generated, want exactly 1 request and no image\n%s", record.arrived, record.generated, record.raw)
	}
}

// TestAgentAction_ImageClientTimeout_ToolErrorNoSecondRequest covers the image client's
// own per-call timeout ending a call well inside the Agent deadline: it is the Tool's
// failure, so the Agent terminates TOOL_ERROR rather than TIMEOUT, and nothing resends
// the request.
func TestAgentAction_ImageClientTimeout_ToolErrorNoSecondRequest(t *testing.T) {
	upstream := newRealImageUpstream(t)
	upstream.holdImages(t)
	wiring := newRealImageWiring(t, upstream, &http.Client{Timeout: time.Second})
	h := newAgentHarness(t, wiring.options(agentHarnessOptions{}))
	run := startRealImageRun(t, h, "wf-real-image-client-timeout", nil, generationCall("photo-a"))
	outcome := h.claimAgentNode(run.ID)

	sawHeld := make(chan bool, 1)
	go func() {
		select {
		case <-upstream.held:
			sawHeld <- true
		case <-time.After(realImageHeldWait):
			sawHeld <- false
		}
	}()
	h.execute(outcome)
	if !<-sawHeld {
		t.Fatal("the image request never reached the Provider")
	}

	failed := assertAgentActionFailed(t, h, run.ID, outcome, domain.TerminationToolError, domain.FailureSyncExecution)
	if failed.Error == nil || failed.Error.Code != "TOOL_ERROR" || !strings.Contains(failed.Error.Message, "timed out") {
		t.Errorf("action error = %+v, want TOOL_ERROR naming the client timeout", failed.Error)
	}
	if strings.Contains(failed.Error.Message, realImageKey) {
		t.Errorf("action error repeats the API key: %q", failed.Error.Message)
	}

	if report := agentRunOnce(h, agentReconciler(h)); report.ExpiredAgentRunsFound != 0 {
		t.Errorf("report = %+v, want nothing left to time out", report)
	}
	if got := wiring.executor.calls.Load(); got != 1 {
		t.Errorf("generate_image executor calls = %d, want 1", got)
	}
	if record := upstream.imagesRecord(t); record.arrived != 1 || record.generated != 0 {
		t.Errorf("Images API record = %d arrived %d generated, want exactly 1 request and no image\n%s", record.arrived, record.generated, record.raw)
	}
}

// crashingTransport sends the first request and, once the Provider holds it, crashes the
// goroutine that made the call: the request is in flight and nothing after the call runs.
// Any later request passes straight through, so a resend after the restart reaches the
// Provider and shows in its record.
type crashingTransport struct {
	held    <-chan string
	crashed *atomic.Bool
}

func (c crashingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !c.crashed.CompareAndSwap(false, true) {
		return http.DefaultTransport.RoundTrip(r)
	}
	go func() {
		resp, err := http.DefaultTransport.RoundTrip(r)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	<-c.held
	panic("simulated crash while the image request is in flight")
}

// TestAgentAction_CrashDuringImageCall_AttemptStaysStartedNoResend covers the process
// dying while the image call is in flight: the Attempt stays STARTED, and the restarted
// Backend neither claims the Action again nor reaches the Provider; only the Agent
// deadline ends the Agent Run.
func TestAgentAction_CrashDuringImageCall_AttemptStaysStartedNoResend(t *testing.T) {
	upstream := newRealImageUpstream(t)
	upstream.holdImages(t)
	wiring := newRealImageWiring(t, upstream, &http.Client{Transport: crashingTransport{held: upstream.held, crashed: &atomic.Bool{}}})
	stop := &agentStopNotifier{}
	h := newAgentHarness(t, wiring.options(agentHarnessOptions{Notifier: stop}))
	run, _, action := readyRealImageGeneration(t, h, stop, "wf-real-image-crash")

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatalf("the simulated crash did not happen")
			}
		}()
		_ = h.svc.ExecuteAgentAction(h.ctx, action.ID, domain.ClaimImmediate)
	}()
	assertAttemptStillStarted(t, h, action.ID)

	assertRestartDoesNotResend(t, h, wiring, upstream, run.ID, action.ID)
}

// TestAgentAction_ShutdownDuringImageCall_NoResend covers a graceful shutdown while the
// image call is in flight: cancelling the worker's context abandons the call without
// committing an outcome, so the Attempt stays STARTED, and the restarted Backend never
// sends the request again.
func TestAgentAction_ShutdownDuringImageCall_NoResend(t *testing.T) {
	upstream := newRealImageUpstream(t)
	upstream.holdImages(t)
	wiring := newRealImageWiring(t, upstream, &http.Client{Timeout: 5 * time.Minute})
	stop := &agentStopNotifier{}
	h := newAgentHarness(t, wiring.options(agentHarnessOptions{Notifier: stop}))
	run, _, action := readyRealImageGeneration(t, h, stop, "wf-real-image-shutdown")

	workerCtx, shutdown := context.WithCancel(h.ctx)
	defer shutdown()
	executed := make(chan error, 1)
	go func() { executed <- h.svc.ExecuteAgentAction(workerCtx, action.ID, domain.ClaimImmediate) }()
	upstream.waitHeld(t)
	shutdown()
	if err := <-executed; !errors.Is(err, context.Canceled) {
		t.Fatalf("execute after shutdown = %v, want context.Canceled", err)
	}
	assertAttemptStillStarted(t, h, action.ID)
	events := listEvents(h.ctx, t, h.uow, run.ID)
	if failed := agentPayloadsFor(t, events, domain.EventAgentActionFailed, "actionId", action.ID); len(failed) != 0 {
		t.Errorf("AGENT_ACTION_FAILED after the shutdown = %v, want none", failed)
	}

	assertRestartDoesNotResend(t, h, wiring, upstream, run.ID, action.ID)
}

// TestAgentAction_GenerationLimitReached_NoImageRequest covers the generation limit with
// a real image model: the call within the limit reaches the Provider once, and the call
// past it is rejected in its claim transaction, so the Provider never receives it.
func TestAgentAction_GenerationLimitReached_NoImageRequest(t *testing.T) {
	upstream := newRealImageUpstream(t)
	wiring := newRealImageWiring(t, upstream, &http.Client{Timeout: 5 * time.Minute})
	h := newAgentHarness(t, wiring.options(agentHarnessOptions{}))
	run := startRealImageRun(t, h, "wf-real-image-limit", generationLimit(1),
		generationCall("photo-a"), generationCall("photo-b"))
	outcome := h.claimAgentNode(run.ID)
	h.execute(outcome)

	agentRunID := factAgentRunID(t, h, outcome)
	first := generationActionOfTurn(t, h, agentRunID, 1)
	if first.Status != domain.AgentActionSucceeded {
		t.Fatalf("first generation = %s, want SUCCEEDED", first.Status)
	}
	assertGenerationLimitRejected(t, h, run.ID, generationActionOfTurn(t, h, agentRunID, 2), 1, 1)
	assertAgentInvalidAction(t, h, run.ID, outcome)

	if got := wiring.executor.calls.Load(); got != 1 {
		t.Errorf("generate_image executor calls = %d, want 1", got)
	}
	if record := upstream.imagesRecord(t); record.arrived != 1 || record.generated != 1 {
		t.Errorf("Images API record = %d arrived %d generated, want the one call within the limit\n%s", record.arrived, record.generated, record.raw)
	}
}
