//go:build integration

// This file is the AIGC Media Generation acceptance track (docs/09-testing-and-acceptance.md
// §1 items 2, 3 and 5), driven end to end through the production pipeline only: the real
// image_generation Node, the real mocktask Adapter, the real deterministic Mock Provider
// (internal/mockprovider, served in-process here), the real POST /api/callbacks intake, the
// real work Pool, and media_output as the sink. Nothing here uses the package's
// test_async_echo fixture Node or its fakeAsyncDispatcher: the point of these tests is that
// the shipped components -- not a stand-in -- dispatch, suspend, resume and recover.
//
// Ordering is controlled entirely by injected http.RoundTrippers (the Provider's callback
// client and the Adapter's dispatch client), which are explicit hooks in the sense
// CLAUDE.md's testing standard requires: no test here sleeps to manufacture a race.
package contract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leungll/Emberling/backend/internal/adapters/mocktask"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/mockprovider"
	"github.com/leungll/Emberling/backend/internal/store"
)

// e2eHTTPTimeout bounds one dispatch or one callback delivery. It is a safety net against a
// hung test binary, never a synchronization device: every ordering decision below is made
// by a channel, not by this duration elapsing.
const e2eHTTPTimeout = 30 * time.Second

// e2eWait bounds how long a test waits for a fact it expects to arrive (a NodeRun status, a
// callback delivery, a terminal Run).
const e2eWait = 20 * time.Second

// ---------------------------------------------------------------------------------------
// The Provider side: the real Mock Provider, served in-process, with two injected hooks
// ---------------------------------------------------------------------------------------

// callbackDelivery is one completed callback POST as the Provider's own HTTP client saw it:
// which task it named, which credential it carried, and how Emberling answered. Recording
// the answer here is what lets a test assert 08 §4's response rules (200 / 202 / 401) on
// deliveries it never issued itself.
type callbackDelivery struct {
	ExternalTaskID string
	Token          string
	StatusCode     int
	Body           []byte
}

// callbackTransport is the http.RoundTripper the Mock Provider's Dispatcher delivers
// callbacks with. It does three things production code cannot do for a test:
//
//  1. It gates delivery on a channel, so "the Provider has accepted the task but has not
//     reported back yet" is a state the test holds open deterministically.
//  2. It rewrites the scheme/host of the callback URL to whichever Emberling server is
//     current, which is what makes a restart test possible: the credential and the task id
//     were minted by the stopped Backend, the delivery arrives at the new one.
//  3. It records every delivery's outcome.
//
// The plaintext callback token is read here and nowhere else in the test: it never reaches
// an assertion input other than the "this must not appear anywhere" checks.
type callbackTransport struct {
	gate       chan struct{}
	gateOnce   sync.Once
	done       chan struct{}
	inner      http.RoundTripper
	deliveries chan callbackDelivery

	mu      sync.Mutex
	target  string
	rewrite func([]byte) []byte
}

func newCallbackTransport() *callbackTransport {
	return &callbackTransport{
		gate: make(chan struct{}),
		done: make(chan struct{}),
		// Deep enough for every delivery any test in this file produces (at most two),
		// so recording a delivery never blocks the Provider's own goroutine.
		deliveries: make(chan callbackDelivery, 8),
		inner:      http.DefaultTransport,
	}
}

// setTarget points subsequent deliveries at origin (an httptest.Server URL).
func (c *callbackTransport) setTarget(origin string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.target = origin
}

func (c *callbackTransport) currentTarget() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.target
}

// rewriteBody installs a hook that rewrites every callback body before it is forwarded to
// Emberling. It stands in for a Provider whose success payload does not speak Emberling's
// ImageRef contract -- a body the Mock Provider itself cannot be asked to produce, because
// the mocktask Adapter is the party that composes the success payload. Only the transport
// is bent here: the intake, the resume use case and the node see an ordinary authenticated
// delivery.
func (c *callbackTransport) rewriteBody(fn func([]byte) []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rewrite = fn
}

func (c *callbackTransport) currentRewrite() func([]byte) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rewrite
}

// release opens the gate, letting every waiting and future delivery through.
func (c *callbackTransport) release() {
	c.gateOnce.Do(func() { close(c.gate) })
}

func (c *callbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	select {
	case <-c.gate:
	case <-c.done:
		return nil, fmt.Errorf("callbackTransport: fixture shut down before the callback was released")
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}

	origin, err := url.Parse(c.currentTarget())
	if err != nil {
		return nil, fmt.Errorf("callbackTransport: parse target %q: %w", c.currentTarget(), err)
	}

	body, err := readAndRestoreRequestBody(req)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		ExternalTaskID string `json:"externalTaskId"`
	}
	_ = json.Unmarshal(body, &envelope)

	outbound := req.Clone(req.Context())
	outbound.URL.Scheme = origin.Scheme
	outbound.URL.Host = origin.Host
	outbound.Host = origin.Host
	if rewrite := c.currentRewrite(); rewrite != nil {
		rewritten := rewrite(body)
		outbound.Body = io.NopCloser(strings.NewReader(string(rewritten)))
		outbound.ContentLength = int64(len(rewritten))
		outbound.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(string(rewritten))), nil
		}
	}

	resp, err := c.inner.RoundTrip(outbound)
	if err != nil {
		return nil, err
	}
	respBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(strings.NewReader(string(respBody)))

	delivery := callbackDelivery{
		ExternalTaskID: envelope.ExternalTaskID,
		Token:          req.Header.Get(callbackTokenHeaderForTest),
		StatusCode:     resp.StatusCode,
		Body:           respBody,
	}
	select {
	case c.deliveries <- delivery:
	default:
	}
	return resp, nil
}

// readAndRestoreRequestBody drains req.Body and puts an equivalent reader back, so the
// request this transport forwards is byte-identical to the one the Provider built.
func readAndRestoreRequestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("callbackTransport: read callback body: %w", err)
	}
	req.Body = io.NopCloser(strings.NewReader(string(raw)))
	return raw, nil
}

// taskTransport is the http.RoundTripper the image_generation Node's mocktask Adapter
// dispatches with. Armed with hold(), it stalls one POST /v1/tasks *response* after the
// Provider has already handled the request -- the window in which the Attempt is STARTED,
// the NodeRun RUNNING, and no Callback Binding exists yet, even though the Provider has
// already sent (or is sending) its callback. That is the only way to reach 06 §1.6's "早到
// callback" branch through the real Provider rather than a hand-built request.
type taskTransport struct {
	inner http.RoundTripper

	mu   sync.Mutex
	hold chan struct{}
	held chan struct{}
	// dispatched holds every POST /v1/tasks body exactly as the Adapter sent it, so a
	// test can assert what did -- and did not -- cross the Provider boundary.
	dispatched [][]byte
}

func newTaskTransport() *taskTransport {
	return &taskTransport{inner: http.DefaultTransport}
}

// arm makes the next dispatch response wait for release before reaching the Adapter.
func (t *taskTransport) arm() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.hold = make(chan struct{})
	t.held = make(chan struct{})
}

// waitHeld blocks until an armed dispatch response is actually being held.
func (t *taskTransport) waitHeld(tb *testing.T, timeout time.Duration) {
	tb.Helper()
	t.mu.Lock()
	held := t.held
	t.mu.Unlock()
	if held == nil {
		tb.Fatal("taskTransport.waitHeld called without arm()")
	}
	select {
	case <-held:
	case <-time.After(timeout):
		tb.Fatalf("no dispatch response was held within %s", timeout)
	}
}

// release lets a held dispatch response reach the Adapter.
func (t *taskTransport) release() {
	t.mu.Lock()
	hold := t.hold
	t.hold = nil
	t.mu.Unlock()
	if hold != nil {
		close(hold)
	}
}

// dispatches returns a copy of the recorded dispatch bodies.
func (t *taskTransport) dispatches() [][]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([][]byte, len(t.dispatched))
	copy(out, t.dispatched)
	return out
}

func (t *taskTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if body, err := readAndRestoreRequestBody(req); err == nil && len(body) > 0 {
		t.mu.Lock()
		t.dispatched = append(t.dispatched, body)
		t.mu.Unlock()
	}

	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	t.mu.Lock()
	hold, held := t.hold, t.held
	t.mu.Unlock()
	if hold == nil {
		return resp, nil
	}

	close(held)
	select {
	case <-hold:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	return resp, nil
}

// providerFixture is one in-process Mock Provider plus the two transports that order a
// test's dispatch and callback around each other. It outlives an Emberling restart on
// purpose: a real external Provider does not restart because Emberling did.
type providerFixture struct {
	server    *httptest.Server
	callbacks *callbackTransport
	tasks     *taskTransport
}

func newProviderFixture(t *testing.T) *providerFixture {
	t.Helper()

	callbacks := newCallbackTransport()
	tasks := newTaskTransport()
	dispatcher := mockprovider.NewDispatcher(&http.Client{Transport: callbacks, Timeout: e2eHTTPTimeout})
	server := httptest.NewServer(mockprovider.NewServer(dispatcher))

	t.Cleanup(func() {
		server.Close()
		// done unblocks any delivery still waiting on a gate the test never released, so
		// Shutdown joins its goroutines immediately instead of waiting out its deadline.
		close(callbacks.done)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := dispatcher.Shutdown(ctx); err != nil {
			t.Logf("mock provider dispatcher shutdown: %v", err)
		}
	})

	return &providerFixture{server: server, callbacks: callbacks, tasks: tasks}
}

// startBackend wires one Emberling Backend against this Provider. pool == nil provisions a
// fresh database; passing a previous testEnv's pool starts a second Backend over the same
// persisted facts, which is what "restart" means here (invariant #6: recovery comes from
// PostgreSQL, never from process memory).
func (f *providerFixture) startBackend(t *testing.T, pool *pgxpool.Pool) *testEnv {
	t.Helper()
	env := newTestEnvWithOptions(t, testEnvOptions{
		Pool:            pool,
		MockTaskBaseURL: f.server.URL,
		TaskClient:      &http.Client{Transport: f.tasks, Timeout: e2eHTTPTimeout},
	})
	f.callbacks.setTarget(env.server.URL)
	return env
}

// waitDelivery returns the next recorded callback delivery.
func (f *providerFixture) waitDelivery(t *testing.T, timeout time.Duration) callbackDelivery {
	t.Helper()
	select {
	case d := <-f.callbacks.deliveries:
		return d
	case <-time.After(timeout):
		t.Fatalf("no callback delivery was recorded within %s", timeout)
		return callbackDelivery{}
	}
}

// ---------------------------------------------------------------------------------------
// The Definition under test
// ---------------------------------------------------------------------------------------

// aigcE2EGraphRequest is the AIGC Media Generation shape with the brief wired straight into
// image_generation.prompt:
//
//	brief ──▶ image ─────────────▶ output.image
//	  └────▶ prompt ──▶ caption ─▶ output.caption
//
// The direct brief → image edge exists because the Mock Model Provider owns its own `mock:`
// directives and answers any prompt with `echo: <prompt>`, so a directive routed through
// text_generation would never reach the Adapter verbatim. This graph keeps the scenario's
// two independent branches (that is what 09 §3.5's "Image Generation 为 WAITING_CALLBACK，
// Caption 为 READY" row needs) while letting a test select the Provider's scenario through
// the Run input. aigcMediaGraphRequest, which the Definition-level tests assert against, is
// deliberately left unchanged.
//
// Node ids also fix the execution order: the Compiler's stable order breaks ties by
// ascending node id (internal/runtime/plan.go), so once `brief` succeeds the single
// execution slot goes to `image` before `prompt`.
func aigcE2EGraphRequest() map[string]any {
	return map[string]any{
		"name":        "AIGC Media Generation (e2e)",
		"description": "",
		"nodes": []map[string]any{
			{"id": "brief", "type": "text_input", "config": map[string]any{"inputKey": "brief", "required": true}},
			{"id": "image", "type": "image_generation", "config": map[string]any{"modelId": "image-model-v1", "width": float64(1024)}},
			{"id": "prompt", "type": "prompt_template", "config": map[string]any{"template": "{{text}}"}},
			{"id": "caption", "type": "text_generation", "config": map[string]any{"modelId": "text-model-v1"}},
			{"id": "output", "type": "media_output", "config": map[string]any{}},
		},
		"edges": []map[string]any{
			{"id": "e1", "source": "brief", "sourceHandle": "text", "target": "image", "targetHandle": "prompt"},
			{"id": "e2", "source": "brief", "sourceHandle": "text", "target": "prompt", "targetHandle": "text"},
			{"id": "e3", "source": "prompt", "sourceHandle": "text", "target": "caption", "targetHandle": "prompt"},
			{"id": "e4", "source": "image", "sourceHandle": "image", "target": "output", "targetHandle": "image"},
			{"id": "e5", "source": "caption", "sourceHandle": "text", "target": "output", "targetHandle": "caption"},
		},
	}
}

// createAIGCRun POSTs the Definition and a Run against it, with brief as the Run input. A
// `mock:` brief reaches the mocktask Adapter unaltered and selects one of the Mock
// Provider's callback scenarios.
func (e *testEnv) createAIGCRun(t *testing.T, brief string) string {
	t.Helper()
	resp, body := e.doJSON(t, http.MethodPost, "/api/definitions", aigcE2EGraphRequest())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)

	resp, body = e.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": int(version), "input": map[string]any{"brief": brief},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)
	if runID == "" {
		t.Fatalf("POST /api/runs response carries no id: %s", body)
	}
	return runID
}

// ---------------------------------------------------------------------------------------
// Small read helpers over the HTTP contract
// ---------------------------------------------------------------------------------------

func (e *testEnv) runSnapshot(t *testing.T, runID string) map[string]any {
	t.Helper()
	resp, raw := e.doJSON(t, http.MethodGet, "/api/runs/"+runID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/runs/%s status = %d, body=%s", runID, resp.StatusCode, raw)
	}
	return decodeBody[map[string]any](t, raw)
}

func (e *testEnv) runStatus(t *testing.T, runID string) string {
	t.Helper()
	run, _ := e.runSnapshot(t, runID)["run"].(map[string]any)
	status, _ := run["status"].(string)
	return status
}

// nodeRunOf returns the NodeRun object for nodeID from the Run Snapshot, or nil.
func (e *testEnv) nodeRunOf(t *testing.T, runID, nodeID string) map[string]any {
	t.Helper()
	nodeRuns, _ := e.runSnapshot(t, runID)["nodeRuns"].([]any)
	for _, nr := range nodeRuns {
		m, _ := nr.(map[string]any)
		if id, _ := m["nodeId"].(string); id == nodeID {
			return m
		}
	}
	return nil
}

// nodeRunDetail returns GET /api/runs/{runId}/nodes/{nodeRunId} decoded, plus its raw bytes
// (which the secret-leak assertions scan verbatim).
func (e *testEnv) nodeRunDetail(t *testing.T, runID, nodeRunID string) (map[string]any, []byte) {
	t.Helper()
	resp, raw := e.doJSON(t, http.MethodGet, "/api/runs/"+runID+"/nodes/"+nodeRunID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/runs/%s/nodes/%s status = %d, body=%s", runID, nodeRunID, resp.StatusCode, raw)
	}
	return decodeBody[map[string]any](t, raw), raw
}

// attemptsOf returns the Attempts of a Node detail response, in the order served.
func attemptsOf(t *testing.T, detail map[string]any) []map[string]any {
	t.Helper()
	raw, _ := detail["attempts"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, a := range raw {
		m, _ := a.(map[string]any)
		out = append(out, m)
	}
	return out
}

// eventsForNodeRun returns runID's committed Events belonging to nodeRunID, in seq order.
func (e *testEnv) eventsForNodeRun(t *testing.T, runID, nodeRunID string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range e.listEvents(t, runID) {
		if id, _ := ev["nodeRunId"].(string); id == nodeRunID {
			out = append(out, ev)
		}
	}
	return out
}

func eventTypes(events []map[string]any) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		t, _ := ev["type"].(string)
		out = append(out, t)
	}
	return out
}

func eventsOfType(events []map[string]any, want string) []map[string]any {
	var out []map[string]any
	for _, ev := range events {
		if t, _ := ev["type"].(string); t == want {
			out = append(out, ev)
		}
	}
	return out
}

func payloadOf(t *testing.T, event map[string]any) map[string]any {
	t.Helper()
	payload, _ := event["payload"].(map[string]any)
	return payload
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// assertContiguousSeq fails when runID's Events are not numbered 1..N without a gap. A
// delivery that "consumed an Event seq" without writing an Event would show up here as a
// hole, which is the only externally observable evidence of that mistake.
func assertContiguousSeq(t *testing.T, events []map[string]any) {
	t.Helper()
	for i, ev := range events {
		seq, _ := ev["seq"].(float64)
		if int(seq) != i+1 {
			t.Fatalf("event[%d] seq = %v, want %d (a gap means an Event seq was consumed without an Event): %v",
				i, seq, i+1, eventTypes(events))
		}
	}
}

// pendingCallbackRow reads the whole Pending Callback row for externalTaskID, read-only,
// through the same store.UnitOfWork the services use. pendingCallbackExists answers "was
// anything persisted at all"; this answers the follow-up an early-callback test needs: was
// the stored delivery claimed, and claimed once. A consumed row is kept on purpose --
// consumption stamps consumed_at (store.PendingCallbackRepository.ConsumeOnce) and the row
// is removed later by the TTL sweep (DeleteExpired), per 05 §1.7's retention rule.
func (e *testEnv) pendingCallbackRow(t *testing.T, externalTaskID string) (domain.PendingCallback, bool) {
	t.Helper()
	var (
		row   domain.PendingCallback
		found bool
	)
	err := e.uow.WithinReadTx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		got, err := tx.PendingCallbacks().GetByExternalTaskID(ctx, externalTaskID)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		row, found = got, true
		return nil
	})
	if err != nil {
		t.Fatalf("query pending callback %s: %v", externalTaskID, err)
	}
	return row, found
}

// bindingOf returns the callbackBinding projection of one Attempt, or nil.
func bindingOf(attempt map[string]any) map[string]any {
	binding, _ := attempt["callbackBinding"].(map[string]any)
	return binding
}

// assertRunOutputImageRef checks that a Run output's `image` member is a domain.ImageRef and
// nothing more (08 §2.2). It re-encodes what the API served and parses it with the same
// domain parser the nodes use, so an unknown member -- a Provider-private field, a signed
// URL parameter carried in an extra key -- fails here exactly as it would inside the
// Runtime. The explicit key-set check is what makes the "no Provider-private field" rule
// visible in the contract rather than only implied by the parser.
//
// It returns the reference's `uri`, so a caller can prove it resolves.
func assertRunOutputImageRef(t *testing.T, output map[string]any) string {
	t.Helper()

	image, ok := output["image"].(map[string]any)
	if !ok {
		t.Fatalf("Run output %q = %v, want a JSON object (an ImageRef)", "image", output["image"])
	}
	if got, want := sortedKeys(image), []string{"mediaType", "source", "uri", "width"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Run output image members = %v, want exactly %v (08 §2.2: an `image` port carries an ImageRef and no Provider-private field)", got, want)
	}
	if source, _ := image["source"].(string); source != "EXTERNAL" {
		t.Errorf("Run output image.source = %v, want %q", image["source"], "EXTERNAL")
	}
	if mediaType, _ := image["mediaType"].(string); mediaType != "image/png" {
		t.Errorf("Run output image.mediaType = %v, want %q", image["mediaType"], "image/png")
	}
	uri, _ := image["uri"].(string)
	parsed, err := url.Parse(uri)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		t.Errorf("Run output image.uri = %q, want an absolute URI (05 §1.2: EXTERNAL carries an addressable uri)", uri)
	}

	// The same bytes the API served must satisfy the domain contract itself.
	encoded, err := json.Marshal(image)
	if err != nil {
		t.Fatalf("re-encode Run output image: %v", err)
	}
	if _, err := domain.ParseImageRef(encoded); err != nil {
		t.Errorf("Run output image is not a valid ImageRef: %v", err)
	}
	return uri
}

// ---------------------------------------------------------------------------------------
// 1. Dispatch, suspend, resume (09 §1 item 2; 09 §3.5)
// ---------------------------------------------------------------------------------------

// TestE2E_AIGC_ImageGenerationDispatchesThenCallbackCompletesRun is the AIGC acceptance
// item itself (09 §1 item 2): the Image Generation NodeRun walks RUNNING →
// WAITING_CALLBACK → SUCCEEDED, its Attempt walks STARTED → DISPATCHED → SUCCEEDED, the
// Events record NODE_DISPATCHED (attemptNo + callbackBindingId only), NODE_CALLBACK_RECEIVED
// and NODE_COMPLETED with completionSource CALLBACK, and the Run ends COMPLETED with
// media_output's image and caption.
//
// It also covers 09 §3.5's "Image Generation 为 WAITING_CALLBACK，Caption 为 READY →
// Runtime 释放执行槽并推进 Caption，Run 保持 RUNNING": the caption's model call is held at a
// barrier while the image Attempt waits, and the Run must read RUNNING -- not PAUSED, which
// is only correct when nothing is READY or RUNNING at all.
func TestE2E_AIGC_ImageGenerationDispatchesThenCallbackCompletesRun(t *testing.T) {
	fx := newProviderFixture(t)
	env := fx.startBackend(t, nil)

	captionBarrier := newBarrierAtCall(1)
	env.provider.BeforeReturn = captionBarrier.beforeReturn
	fx.tasks.arm()

	runID := env.createAIGCRun(t, "a neon skyline at dusk")

	// --- RUNNING / Attempt STARTED: the Provider has the task, the acceptance response is
	// still in flight, so no Callback Binding exists yet.
	fx.tasks.waitHeld(t, e2eWait)
	running := env.waitForNodeRunStatus(t, runID, "image", "RUNNING", e2eWait)
	imageNodeRunID, _ := running["id"].(string)
	if imageNodeRunID == "" {
		t.Fatalf("RUNNING image NodeRun carries no id: %v", running)
	}
	detail, _ := env.nodeRunDetail(t, runID, imageNodeRunID)
	attempts := attemptsOf(t, detail)
	if len(attempts) != 1 {
		t.Fatalf("image Attempts while RUNNING = %d, want 1", len(attempts))
	}
	if status, _ := attempts[0]["status"].(string); status != "STARTED" {
		t.Errorf("image Attempt status while RUNNING = %q, want %q", status, "STARTED")
	}
	if b := bindingOf(attempts[0]); b != nil {
		t.Errorf("image Attempt projects a Callback Binding before the dispatch committed: %v", b)
	}

	fx.tasks.release()

	// --- WAITING_CALLBACK / Attempt DISPATCHED, with the Binding projected.
	waiting := env.waitForNodeRunStatus(t, runID, "image", "WAITING_CALLBACK", e2eWait)
	if id, _ := waiting["id"].(string); id != imageNodeRunID {
		t.Fatalf("WAITING_CALLBACK NodeRun id = %q, want the same NodeRun %q", id, imageNodeRunID)
	}
	detail, _ = env.nodeRunDetail(t, runID, imageNodeRunID)
	attempts = attemptsOf(t, detail)
	if len(attempts) != 1 {
		t.Fatalf("image Attempts while WAITING_CALLBACK = %d, want 1 (a dispatch must not create a second Attempt)", len(attempts))
	}
	if status, _ := attempts[0]["status"].(string); status != "DISPATCHED" {
		t.Errorf("image Attempt status while WAITING_CALLBACK = %q, want %q", status, "DISPATCHED")
	}
	binding := bindingOf(attempts[0])
	if binding == nil {
		t.Fatalf("image Attempt projects no Callback Binding while WAITING_CALLBACK: %v", attempts[0])
	}
	if providerID, _ := binding["providerId"].(string); providerID != mocktask.ProviderID {
		t.Errorf("Callback Binding providerId = %q, want %q", providerID, mocktask.ProviderID)
	}
	externalTaskID, _ := binding["externalTaskId"].(string)
	if externalTaskID == "" {
		t.Errorf("Callback Binding externalTaskId is empty: %v", binding)
	}
	bindingID, _ := binding["id"].(string)

	// NODE_DISPATCHED carries the bounded dispatch record and nothing else.
	dispatched := eventsOfType(env.eventsForNodeRun(t, runID, imageNodeRunID), "NODE_DISPATCHED")
	if len(dispatched) != 1 {
		t.Fatalf("NODE_DISPATCHED events = %d, want 1", len(dispatched))
	}
	dispatchedPayload := payloadOf(t, dispatched[0])
	if got, want := sortedKeys(dispatchedPayload), []string{"attemptNo", "callbackBindingId"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("NODE_DISPATCHED payload keys = %v, want %v", got, want)
	}
	if got, _ := dispatchedPayload["attemptNo"].(float64); int(got) != 1 {
		t.Errorf("NODE_DISPATCHED attemptNo = %v, want 1", got)
	}
	if got, _ := dispatchedPayload["callbackBindingId"].(string); got != bindingID {
		t.Errorf("NODE_DISPATCHED callbackBindingId = %q, want the projected Binding id %q", got, bindingID)
	}

	// --- 09 §3.5: the execution slot was released, Caption is advancing, Run stays RUNNING.
	captionBarrier.waitEntered(t, e2eWait)
	if status := env.runStatus(t, runID); status != "RUNNING" {
		t.Fatalf("Run status while the image Attempt waits and Caption is in flight = %q, want %q (PAUSED is only correct when nothing is READY or RUNNING)", status, "RUNNING")
	}
	captionBarrier.Release()
	fx.callbacks.release()

	// --- the callback completes the original NodeRun and the Run.
	delivery := fx.waitDelivery(t, e2eWait)
	if delivery.StatusCode != http.StatusOK {
		t.Fatalf("callback delivery status = %d, want %d, body=%s", delivery.StatusCode, http.StatusOK, delivery.Body)
	}
	if delivery.ExternalTaskID != externalTaskID {
		t.Errorf("callback named externalTaskId %q, want the bound %q", delivery.ExternalTaskID, externalTaskID)
	}

	snap := env.waitForTerminal(t, runID, e2eWait)
	run, _ := snap["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("Run status = %q, want %q, snapshot=%v", status, "COMPLETED", run)
	}
	output, _ := run["output"].(map[string]any)
	imageURI := assertRunOutputImageRef(t, output)

	// The reference is readable with no credential of any kind: that is what distinguishes
	// a normalised EXTERNAL ImageRef from a signed URL the Runtime must never republish.
	imageResp, err := (&http.Client{Timeout: e2eHTTPTimeout}).Get(imageURI)
	if err != nil {
		t.Fatalf("GET the Run output image.uri %q: %v", imageURI, err)
	}
	imageBytes, _ := io.ReadAll(imageResp.Body)
	_ = imageResp.Body.Close()
	if imageResp.StatusCode != http.StatusOK {
		t.Errorf("GET %q status = %d, want %d", imageURI, imageResp.StatusCode, http.StatusOK)
	}
	if got := imageResp.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("GET %q Content-Type = %q, want %q (the declared mediaType)", imageURI, got, "image/png")
	}
	if !strings.HasPrefix(string(imageBytes), "\x89PNG\r\n\x1a\n") {
		t.Errorf("GET %q returned %d bytes that are not a PNG", imageURI, len(imageBytes))
	}
	if caption, _ := output["caption"].(string); !strings.Contains(caption, "echo:") {
		t.Errorf("Run output caption = %v, want the Mock Model's echo of the rendered prompt", output["caption"])
	}

	// --- the whole Attempt and Event trail of the async Node.
	detail, rawDetail := env.nodeRunDetail(t, runID, imageNodeRunID)
	nodeRun, _ := detail["nodeRun"].(map[string]any)
	if status, _ := nodeRun["status"].(string); status != "SUCCEEDED" {
		t.Errorf("image NodeRun final status = %q, want %q", status, "SUCCEEDED")
	}
	attempts = attemptsOf(t, detail)
	if len(attempts) != 1 {
		t.Fatalf("image Attempts after completion = %d, want 1", len(attempts))
	}
	if status, _ := attempts[0]["status"].(string); status != "SUCCEEDED" {
		t.Errorf("image Attempt final status = %q, want %q", status, "SUCCEEDED")
	}

	nodeEvents := env.eventsForNodeRun(t, runID, imageNodeRunID)
	if got, want := eventTypes(nodeEvents), []string{"NODE_READY", "NODE_STARTED", "NODE_DISPATCHED", "NODE_CALLBACK_RECEIVED", "NODE_COMPLETED"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("image NodeRun Events = %v, want %v", got, want)
	}
	completed := eventsOfType(nodeEvents, "NODE_COMPLETED")
	if len(completed) != 1 {
		t.Fatalf("NODE_COMPLETED events = %d, want 1", len(completed))
	}
	if source, _ := payloadOf(t, completed[0])["completionSource"].(string); source != "CALLBACK" {
		t.Errorf("NODE_COMPLETED completionSource = %q, want %q", source, "CALLBACK")
	}

	// --- the credential never leaves the Provider boundary (08 §4, 10 §2).
	if delivery.Token == "" {
		t.Fatal("the Provider received no callback token, so the leak assertions below would be vacuous")
	}
	hash := sha256.Sum256([]byte(delivery.Token))
	tokenHash := hex.EncodeToString(hash[:])
	for _, secret := range []struct{ name, value string }{
		{"plaintext callback token", delivery.Token},
		{"callback token sha256", tokenHash},
	} {
		if strings.Contains(string(rawDetail), secret.value) {
			t.Errorf("Node detail response contains the %s", secret.name)
		}
		eventsJSON, err := json.Marshal(env.listEvents(t, runID))
		if err != nil {
			t.Fatalf("marshal events: %v", err)
		}
		if strings.Contains(string(eventsJSON), secret.value) {
			t.Errorf("Event stream contains the %s", secret.name)
		}
	}
}

// ---------------------------------------------------------------------------------------
// 2. Restart while WAITING_CALLBACK (09 §1 item 3)
// ---------------------------------------------------------------------------------------

// TestE2E_AIGC_RestartWhileWaitingCallback_CallbackResumesOriginalRun covers 09 §1 item 3:
// with the callback held, the Backend is torn down (work Pool stopped, HTTP server closed)
// and a second Backend is started over the same database. The Provider -- unaware anything
// happened -- then delivers to the new server, and the ORIGINAL Run, NodeRun and Attempt
// complete. Nothing but PostgreSQL carries the waiting fact across the restart: the new
// process has a new registry, a new work Pool, a new queue and an empty memory.
func TestE2E_AIGC_RestartWhileWaitingCallback_CallbackResumesOriginalRun(t *testing.T) {
	fx := newProviderFixture(t)
	first := fx.startBackend(t, nil)

	runID := first.createAIGCRun(t, "a lighthouse in fog")
	waiting := first.waitForNodeRunStatus(t, runID, "image", "WAITING_CALLBACK", e2eWait)
	imageNodeRunID, _ := waiting["id"].(string)
	// The caption branch finishes before the restart, so what the second Backend must
	// recover is exactly the waiting Attempt and the work its completion unblocks.
	first.waitForNodeRunStatus(t, runID, "caption", "SUCCEEDED", e2eWait)

	detail, _ := first.nodeRunDetail(t, runID, imageNodeRunID)
	attempts := attemptsOf(t, detail)
	if len(attempts) != 1 {
		t.Fatalf("image Attempts before the restart = %d, want 1", len(attempts))
	}
	attemptID, _ := attempts[0]["id"].(string)
	binding := bindingOf(attempts[0])
	if binding == nil {
		t.Fatalf("image Attempt projects no Callback Binding before the restart: %v", attempts[0])
	}
	externalTaskID, _ := binding["externalTaskId"].(string)
	eventsBefore := first.listEvents(t, runID)

	// --- restart: same database, new everything else.
	first.stop()
	second := fx.startBackend(t, first.pool)

	fx.callbacks.release()
	delivery := fx.waitDelivery(t, e2eWait)
	if delivery.StatusCode != http.StatusOK {
		t.Fatalf("callback delivery after the restart: status = %d, want %d, body=%s", delivery.StatusCode, http.StatusOK, delivery.Body)
	}
	if delivery.ExternalTaskID != externalTaskID {
		t.Errorf("callback after the restart named externalTaskId %q, want %q", delivery.ExternalTaskID, externalTaskID)
	}

	snap := second.waitForTerminal(t, runID, e2eWait)
	run, _ := snap["run"].(map[string]any)
	if id, _ := run["id"].(string); id != runID {
		t.Fatalf("terminal Run id = %q, want the original %q", id, runID)
	}
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("Run status after a restart resume = %q, want %q", status, "COMPLETED")
	}

	detail, _ = second.nodeRunDetail(t, runID, imageNodeRunID)
	nodeRun, _ := detail["nodeRun"].(map[string]any)
	if status, _ := nodeRun["status"].(string); status != "SUCCEEDED" {
		t.Errorf("original image NodeRun after the restart = %q, want %q", status, "SUCCEEDED")
	}
	attempts = attemptsOf(t, detail)
	if len(attempts) != 1 {
		t.Fatalf("image Attempts after the restart = %d, want 1 (a resume must not create a new Attempt)", len(attempts))
	}
	if id, _ := attempts[0]["id"].(string); id != attemptID {
		t.Errorf("resumed Attempt id = %q, want the original %q", id, attemptID)
	}
	if status, _ := attempts[0]["status"].(string); status != "SUCCEEDED" {
		t.Errorf("resumed Attempt status = %q, want %q", status, "SUCCEEDED")
	}

	nodeEvents := second.eventsForNodeRun(t, runID, imageNodeRunID)
	if got := len(eventsOfType(nodeEvents, "NODE_COMPLETED")); got != 1 {
		t.Errorf("NODE_COMPLETED events for the image NodeRun = %d, want 1: %v", got, eventTypes(nodeEvents))
	}
	if got := len(eventsOfType(nodeEvents, "NODE_CALLBACK_RECEIVED")); got != 1 {
		t.Errorf("NODE_CALLBACK_RECEIVED events for the image NodeRun = %d, want 1: %v", got, eventTypes(nodeEvents))
	}
	eventsAfter := second.listEvents(t, runID)
	if len(eventsAfter) <= len(eventsBefore) {
		t.Errorf("Events after the restart = %d, want more than the %d committed before it", len(eventsAfter), len(eventsBefore))
	}
	assertContiguousSeq(t, eventsAfter)
}

// ---------------------------------------------------------------------------------------
// 3. Duplicate and wrong-token deliveries (09 §1 item 5)
// ---------------------------------------------------------------------------------------

// TestE2E_AIGC_DuplicateAndWrongTokenCallbacks_DoNotAdvanceTwice covers the two
// non-advancing delivery classes of 09 §1 item 5, both produced by the real Provider rather
// than by a hand-built request: `mock:duplicate` makes it deliver the same callback twice,
// and `mock:wrong-token` makes it deliver with a credential that is not the one Emberling
// minted.
func TestE2E_AIGC_DuplicateAndWrongTokenCallbacks_DoNotAdvanceTwice(t *testing.T) {
	t.Run("duplicate delivery advances once", func(t *testing.T) {
		fx := newProviderFixture(t)
		env := fx.startBackend(t, nil)

		runID := env.createAIGCRun(t, "mock:duplicate")
		waiting := env.waitForNodeRunStatus(t, runID, "image", "WAITING_CALLBACK", e2eWait)
		imageNodeRunID, _ := waiting["id"].(string)

		// Both deliveries were produced at dispatch time and are waiting at the gate, so
		// releasing it now is exactly "two deliveries of the same task, after the Binding
		// committed" -- not an early-callback race.
		fx.callbacks.release()
		first := fx.waitDelivery(t, e2eWait)
		second := fx.waitDelivery(t, e2eWait)

		for i, d := range []callbackDelivery{first, second} {
			if d.StatusCode != http.StatusOK {
				t.Fatalf("delivery %d status = %d, want %d (08 §4: a duplicate is idempotently accepted), body=%s", i+1, d.StatusCode, http.StatusOK, d.Body)
			}
		}
		outcomes := []callbackResponseDTO{
			decodeBody[callbackResponseDTO](t, first.Body),
			decodeBody[callbackResponseDTO](t, second.Body),
		}
		duplicates := 0
		for i, o := range outcomes {
			if !o.Accepted {
				t.Errorf("delivery %d accepted = false, want true: %+v", i+1, o)
			}
			if o.Pending {
				t.Errorf("delivery %d pending = true, want false (the Binding was committed): %+v", i+1, o)
			}
			if o.Duplicate {
				duplicates++
			}
		}
		if duplicates != 1 {
			t.Errorf("duplicate=true responses = %d, want exactly 1: %+v", duplicates, outcomes)
		}

		snap := env.waitForTerminal(t, runID, e2eWait)
		run, _ := snap["run"].(map[string]any)
		if status, _ := run["status"].(string); status != "COMPLETED" {
			t.Fatalf("Run status = %q, want %q", status, "COMPLETED")
		}

		nodeEvents := env.eventsForNodeRun(t, runID, imageNodeRunID)
		if got := len(eventsOfType(nodeEvents, "NODE_CALLBACK_RECEIVED")); got != 1 {
			t.Errorf("NODE_CALLBACK_RECEIVED events = %d, want 1: %v", got, eventTypes(nodeEvents))
		}
		if got := len(eventsOfType(nodeEvents, "NODE_COMPLETED")); got != 1 {
			t.Errorf("NODE_COMPLETED events = %d, want 1: %v", got, eventTypes(nodeEvents))
		}
		// The losing delivery wrote nothing at all, so it cannot have consumed a seq.
		assertContiguousSeq(t, env.listEvents(t, runID))
	})

	t.Run("wrong credential is rejected and persists nothing", func(t *testing.T) {
		fx := newProviderFixture(t)
		env := fx.startBackend(t, nil)

		runID := env.createAIGCRun(t, "mock:wrong-token")
		waiting := env.waitForNodeRunStatus(t, runID, "image", "WAITING_CALLBACK", e2eWait)
		imageNodeRunID, _ := waiting["id"].(string)
		detail, _ := env.nodeRunDetail(t, runID, imageNodeRunID)
		binding := bindingOf(attemptsOf(t, detail)[0])
		externalTaskID, _ := binding["externalTaskId"].(string)

		fx.callbacks.release()
		delivery := fx.waitDelivery(t, e2eWait)
		if delivery.StatusCode != http.StatusUnauthorized {
			t.Fatalf("delivery with a mismatched credential: status = %d, want %d, body=%s", delivery.StatusCode, http.StatusUnauthorized, delivery.Body)
		}
		if code := errorCode(t, delivery.Body); code != "INVALID_CALLBACK_CREDENTIAL" {
			t.Errorf("rejected delivery error.code = %q, want %q", code, "INVALID_CALLBACK_CREDENTIAL")
		}

		if status, _ := env.nodeRunOf(t, runID, "image")["status"].(string); status != "WAITING_CALLBACK" {
			t.Errorf("image NodeRun after a rejected credential = %q, want %q", status, "WAITING_CALLBACK")
		}
		nodeEvents := env.eventsForNodeRun(t, runID, imageNodeRunID)
		if got := len(eventsOfType(nodeEvents, "NODE_CALLBACK_RECEIVED")); got != 0 {
			t.Errorf("NODE_CALLBACK_RECEIVED events after a rejected credential = %d, want 0: %v", got, eventTypes(nodeEvents))
		}
		if env.pendingCallbackExists(t, externalTaskID) {
			t.Errorf("a rejected delivery persisted a Pending Callback row for %q, want none (08 §4: token 无效时不得保存 payload)", externalTaskID)
		}
	})
}

// ---------------------------------------------------------------------------------------
// 4. Provider-reported failure
// ---------------------------------------------------------------------------------------

// TestE2E_AIGC_ProviderReportsFailure_NodeAndRunFail covers the Provider-reported failure
// branch of 06 §1.6: an authenticated callback whose payload says the external task failed
// resolves the waiting Attempt and NodeRun as FAILED and fails the Run. NODE_FAILED carries
// the bounded error + attemptNo record only (05 §2.3): the failure source is a routing fact
// of the resume call, not something the Event persists.
func TestE2E_AIGC_ProviderReportsFailure_NodeAndRunFail(t *testing.T) {
	fx := newProviderFixture(t)
	env := fx.startBackend(t, nil)

	runID := env.createAIGCRun(t, "mock:failed")
	waiting := env.waitForNodeRunStatus(t, runID, "image", "WAITING_CALLBACK", e2eWait)
	imageNodeRunID, _ := waiting["id"].(string)

	fx.callbacks.release()
	delivery := fx.waitDelivery(t, e2eWait)
	if delivery.StatusCode != http.StatusOK {
		t.Fatalf("Provider failure callback: status = %d, want %d, body=%s", delivery.StatusCode, http.StatusOK, delivery.Body)
	}

	snap := env.waitForTerminal(t, runID, e2eWait)
	run, _ := snap["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "FAILED" {
		t.Fatalf("Run status = %q, want %q, run=%v", status, "FAILED", run)
	}

	detail, _ := env.nodeRunDetail(t, runID, imageNodeRunID)
	nodeRun, _ := detail["nodeRun"].(map[string]any)
	if status, _ := nodeRun["status"].(string); status != "FAILED" {
		t.Errorf("image NodeRun status = %q, want %q", status, "FAILED")
	}
	attempts := attemptsOf(t, detail)
	if len(attempts) != 1 {
		t.Fatalf("image Attempts = %d, want 1 (06 §2.2: a reported Provider failure is not re-dispatched)", len(attempts))
	}
	if status, _ := attempts[0]["status"].(string); status != "FAILED" {
		t.Errorf("image Attempt status = %q, want %q", status, "FAILED")
	}
	attemptError, _ := attempts[0]["error"].(map[string]any)
	if code, _ := attemptError["code"].(string); code != "PROVIDER_TASK_FAILED" {
		t.Errorf("image Attempt error.code = %q, want %q", code, "PROVIDER_TASK_FAILED")
	}

	nodeEvents := env.eventsForNodeRun(t, runID, imageNodeRunID)
	failed := eventsOfType(nodeEvents, "NODE_FAILED")
	if len(failed) != 1 {
		t.Fatalf("NODE_FAILED events = %d, want 1: %v", len(failed), eventTypes(nodeEvents))
	}
	failedPayload := payloadOf(t, failed[0])
	if got, want := sortedKeys(failedPayload), []string{"attemptNo", "error"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("NODE_FAILED payload keys = %v, want %v (05 §2.3 gives it error + attemptNo only)", got, want)
	}
	if got, _ := failedPayload["attemptNo"].(float64); int(got) != 1 {
		t.Errorf("NODE_FAILED attemptNo = %v, want 1", got)
	}
	if got := len(eventsOfType(nodeEvents, "NODE_COMPLETED")); got != 0 {
		t.Errorf("NODE_COMPLETED events = %d, want 0 for a failed Node: %v", got, eventTypes(nodeEvents))
	}
}

// TestE2E_AIGC_ProviderImageNotImageRef_CallbackRejectedNodeStaysWaiting is the other half
// of 08 §2.2: an `image` port carries a domain.ImageRef, so a Provider reporting SUCCEEDED
// with a reference shape of its own is a payload the node cannot interpret -- not a
// Provider-reported failure and not a completion.
//
// 06 §1.6 fixes what that means end to end: the delivery is authenticated, so the intake
// answers 200 {accepted:true} rather than a retry signal, but the Executor rejected the
// payload, so nothing is persisted at all -- no NODE_CALLBACK_RECEIVED, no NODE_COMPLETED,
// no Event seq consumed -- and the NodeRun stays WAITING_CALLBACK until its Attempt
// deadline decides the outcome. The Provider's private object must never reach Run.output.
func TestE2E_AIGC_ProviderImageNotImageRef_CallbackRejectedNodeStaysWaiting(t *testing.T) {
	fx := newProviderFixture(t)
	env := fx.startBackend(t, nil)

	// The Provider answers SUCCEEDED with its own reference dialect plus a private job
	// handle: a well-formed JSON object that is not an ImageRef.
	fx.callbacks.rewriteBody(func(body []byte) []byte {
		var envelope struct {
			ExternalTaskID string          `json:"externalTaskId"`
			Payload        json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return body
		}
		envelope.Payload = json.RawMessage(
			`{"status":"SUCCEEDED","image":{"url":"https://provider.invalid/i.png?sig=s3cr3t","providerJobId":"job_1"}}`,
		)
		rewritten, err := json.Marshal(envelope)
		if err != nil {
			return body
		}
		return rewritten
	})

	runID := env.createAIGCRun(t, "a provider that speaks its own dialect")
	waiting := env.waitForNodeRunStatus(t, runID, "image", "WAITING_CALLBACK", e2eWait)
	imageNodeRunID, _ := waiting["id"].(string)
	// The caption branch reaches its own end, so the only thing left undecided when the
	// assertions below run is the rejected image Attempt.
	env.waitForNodeRunStatus(t, runID, "caption", "SUCCEEDED", e2eWait)

	fx.callbacks.release()
	delivery := fx.waitDelivery(t, e2eWait)

	// The delivery's HTTP response is the barrier: HandleCallback has already returned by
	// the time it is written, so every fact asserted below is settled, with no sleep.
	if delivery.StatusCode != http.StatusOK {
		t.Fatalf("rejected-payload callback status = %d, want %d (08 §4: the credential was valid), body=%s",
			delivery.StatusCode, http.StatusOK, delivery.Body)
	}
	outcome := decodeBody[callbackResponseDTO](t, delivery.Body)
	if !outcome.Accepted || outcome.Pending || outcome.Duplicate {
		t.Errorf("rejected-payload callback outcome = %+v, want {Accepted:true Pending:false Duplicate:false}", outcome)
	}

	detail, rawDetail := env.nodeRunDetail(t, runID, imageNodeRunID)
	nodeRun, _ := detail["nodeRun"].(map[string]any)
	if status, _ := nodeRun["status"].(string); status != "WAITING_CALLBACK" {
		t.Errorf("image NodeRun status after an uninterpretable payload = %q, want %q (06 §1.6: a malformed delivery is not evidence the external task failed)", status, "WAITING_CALLBACK")
	}
	attempts := attemptsOf(t, detail)
	if len(attempts) != 1 {
		t.Fatalf("image Attempts = %d, want 1 (a rejected payload creates no Attempt)", len(attempts))
	}
	if status, _ := attempts[0]["status"].(string); status != "DISPATCHED" {
		t.Errorf("image Attempt status = %q, want %q", status, "DISPATCHED")
	}

	nodeEvents := env.eventsForNodeRun(t, runID, imageNodeRunID)
	if got, want := eventTypes(nodeEvents), []string{"NODE_READY", "NODE_STARTED", "NODE_DISPATCHED"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("image NodeRun Events = %v, want %v (a rejected payload persists nothing)", got, want)
	}
	// No Event seq was consumed by the rejected delivery either.
	assertContiguousSeq(t, env.listEvents(t, runID))

	if status := env.runStatus(t, runID); status == "COMPLETED" || status == "FAILED" {
		t.Errorf("Run status = %q, want a non-terminal Run: the image Attempt is still waiting", status)
	}
	snap := env.runSnapshot(t, runID)
	run, _ := snap["run"].(map[string]any)
	if output, ok := run["output"]; ok && output != nil {
		t.Errorf("Run output = %v, want none: the Provider's private object must not reach Run.output", output)
	}

	// The Provider's private members are nowhere in what Emberling serves.
	snapshotJSON, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal run snapshot: %v", err)
	}
	eventsJSON, err := json.Marshal(env.listEvents(t, runID))
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}
	for _, private := range []string{"providerJobId", "s3cr3t", "provider.invalid"} {
		for name, served := range map[string][]byte{
			"Node detail response": rawDetail,
			"Run Snapshot":         snapshotJSON,
			"Event stream":         eventsJSON,
		} {
			if strings.Contains(string(served), private) {
				t.Errorf("%s contains the Provider-private value %q", name, private)
			}
		}
	}
}

// ---------------------------------------------------------------------------------------
// 5. Callback earlier than its own Callback Binding (09 §1 item 5, 06 §3)
// ---------------------------------------------------------------------------------------

// TestE2E_AIGC_EarlyCallback_RecordedPendingThenConsumed covers "callback 早于
// WAITING_CALLBACK 提交" (06 §3) with the real Provider: the dispatch response is held in
// the Adapter's own HTTP client, so the Provider's immediate callback reaches
// POST /api/callbacks while dispatchNode is still blocked and no Callback Binding exists.
// The intake stores it and answers 202; once the Binding commits, the stored delivery is
// consumed exactly once and the Run completes.
func TestE2E_AIGC_EarlyCallback_RecordedPendingThenConsumed(t *testing.T) {
	fx := newProviderFixture(t)
	env := fx.startBackend(t, nil)

	// Callbacks flow freely; it is the *dispatch response* that is held here.
	fx.callbacks.release()
	fx.tasks.arm()

	runID := env.createAIGCRun(t, "an early bird")
	fx.tasks.waitHeld(t, e2eWait)

	early := fx.waitDelivery(t, e2eWait)
	if early.StatusCode != http.StatusAccepted {
		t.Fatalf("early callback status = %d, want %d (08 §4: binding 尚未创建且 token 有效), body=%s", early.StatusCode, http.StatusAccepted, early.Body)
	}
	outcome := decodeBody[callbackResponseDTO](t, early.Body)
	if !outcome.Accepted || !outcome.Pending || outcome.Duplicate {
		t.Errorf("early callback outcome = %+v, want {Accepted:true Pending:true Duplicate:false}", outcome)
	}
	if !env.pendingCallbackExists(t, early.ExternalTaskID) {
		t.Fatalf("early callback persisted no Pending Callback row for %q", early.ExternalTaskID)
	}

	fx.tasks.release()

	snap := env.waitForTerminal(t, runID, e2eWait)
	run, _ := snap["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("Run status = %q, want %q, run=%v", status, "COMPLETED", run)
	}

	imageNodeRun := env.nodeRunOf(t, runID, "image")
	imageNodeRunID, _ := imageNodeRun["id"].(string)
	nodeEvents := env.eventsForNodeRun(t, runID, imageNodeRunID)
	if got := len(eventsOfType(nodeEvents, "NODE_CALLBACK_RECEIVED")); got != 1 {
		t.Errorf("NODE_CALLBACK_RECEIVED events = %d, want 1: %v", got, eventTypes(nodeEvents))
	}
	if got := len(eventsOfType(nodeEvents, "NODE_COMPLETED")); got != 1 {
		t.Errorf("NODE_COMPLETED events = %d, want 1: %v", got, eventTypes(nodeEvents))
	}
	// The stored delivery was claimed, and claimed once. The row itself outlives its
	// consumption by design (05 §1.7: a consumed record is retained until the TTL sweep),
	// so "consumed exactly once" -- not "deleted" -- is the fact that proves the early
	// callback advanced the Attempt and cannot advance it again.
	pending, found := env.pendingCallbackRow(t, early.ExternalTaskID)
	if !found {
		t.Fatalf("the Pending Callback row for %q disappeared instead of being stamped consumed", early.ExternalTaskID)
	}
	if pending.ConsumedAt == nil {
		t.Errorf("the Pending Callback row for %q was never stamped consumed, so the resume did not come from it", early.ExternalTaskID)
	}
	if pending.DuplicateCount != 0 {
		t.Errorf("Pending Callback duplicateCount = %d, want 0 (the Provider delivered once)", pending.DuplicateCount)
	}
	assertContiguousSeq(t, env.listEvents(t, runID))
}

// ---------------------------------------------------------------------------------------
// 6. Reference Image: Asset -> image_input -> image_generation (09 §1 item 5)
// ---------------------------------------------------------------------------------------

// aigcReferenceE2EGraphRequest is aigcE2EGraphRequest plus the Reference Image branch the
// AIGC scenario draws (test/fixtures/definitions/aigc_media.json's `edge_reference_image`):
//
//	reference ──────────────────▶ image.reference
//	brief ──▶ image ─────────────▶ output.image
//	  └────▶ prompt ──▶ caption ─▶ output.caption
//
// The Image Input's key is required here, so every Run against this Definition must carry
// an AssetRef and the branch cannot be silently skipped.
func aigcReferenceE2EGraphRequest() map[string]any {
	graph := aigcE2EGraphRequest()
	graph["name"] = "AIGC Media Generation with a Reference Image (e2e)"
	nodes, _ := graph["nodes"].([]map[string]any)
	graph["nodes"] = append(nodes, map[string]any{
		"id": "reference", "type": "image_input", "config": map[string]any{
			"inputKey": "reference", "required": true,
			"acceptedMediaTypes": []string{"image/png", "image/jpeg"},
			"maxSizeBytes":       float64(5242880),
		},
	})
	edges, _ := graph["edges"].([]map[string]any)
	graph["edges"] = append(edges, map[string]any{
		"id": "e6", "source": "reference", "sourceHandle": "image", "target": "image", "targetHandle": "reference",
	})
	return graph
}

// createAIGCReferenceRun POSTs the reference-image Definition and one Run carrying both the
// brief and the uploaded AssetRef.
func (e *testEnv) createAIGCReferenceRun(t *testing.T, brief string, reference map[string]any) string {
	t.Helper()
	resp, body := e.doJSON(t, http.MethodPost, "/api/definitions", aigcReferenceE2EGraphRequest())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)

	resp, body = e.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": int(version),
		"input": map[string]any{"brief": brief, "reference": reference},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)
	if runID == "" {
		t.Fatalf("POST /api/runs response carries no id: %s", body)
	}
	return runID
}

// TestE2E_AIGC_ReferenceImage_ImageInputOutputsAssetImageRefAndRunCompletes walks the whole
// Asset track in one Run: an uploaded Asset becomes an AssetRef, the AssetRef becomes the
// Image Input's `source: ASSET` ImageRef, that ImageRef reaches image_generation's
// `reference` port and is forwarded to the Provider verbatim, and the Run still completes
// with the normalised EXTERNAL image the callback reported.
//
// What must NOT happen is asserted just as explicitly: the Asset's internal storage key
// never appears in the dispatch body, the Attempt input, the Node detail or the Events
// (10-ops §4), and the Provider is handed a reference it does not resolve -- the Mock
// Provider issues no request for the Asset's content at all.
func TestE2E_AIGC_ReferenceImage_ImageInputOutputsAssetImageRefAndRunCompletes(t *testing.T) {
	fx := newProviderFixture(t)
	env := fx.startBackend(t, nil)

	content := bytes.Repeat([]byte("reference pixels"), 20)
	ref := env.uploadPNG(t, content)
	assetID, _ := ref["assetId"].(string)
	if assetID == "" {
		t.Fatalf("upload returned no assetId: %v", ref)
	}

	runID := env.createAIGCReferenceRun(t, "a neon skyline at dusk", ref)

	// The callback gate is closed until the Attempt is observably WAITING_CALLBACK, so this
	// test exercises the ordinary "Binding committed, then callback" path rather than
	// racing the early-callback branch its own siblings already cover. It is a barrier, not
	// a delay: nothing here sleeps.
	env.waitForNodeRunStatus(t, runID, "image", "WAITING_CALLBACK", e2eWait)
	fx.callbacks.release()

	snap := env.waitForTerminal(t, runID, e2eWait)
	run, _ := snap["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("Run status = %q, want %q, snapshot=%v", status, "COMPLETED", run)
	}

	// --- the Image Input published the canonical `source: ASSET` ImageRef.
	imageInputRun := env.nodeRunOf(t, runID, "reference")
	if imageInputRun == nil {
		t.Fatalf("Run Snapshot carries no image_input NodeRun: %v", snap)
	}
	if status, _ := imageInputRun["status"].(string); status != "SUCCEEDED" {
		t.Errorf("image_input NodeRun status = %q, want %q", status, "SUCCEEDED")
	}
	imageInputRunID, _ := imageInputRun["id"].(string)
	_, imageInputRaw := env.nodeRunDetail(t, runID, imageInputRunID)
	assertAssetImageRef(t, imageInputRaw, assetID, ref)

	// --- that ImageRef is what image_generation was executed with.
	imageRun := env.nodeRunOf(t, runID, "image")
	if imageRun == nil {
		t.Fatalf("Run Snapshot carries no image_generation NodeRun: %v", snap)
	}
	imageRunID, _ := imageRun["id"].(string)
	imageDetail, imageRaw := env.nodeRunDetail(t, runID, imageRunID)
	attempts := attemptsOf(t, imageDetail)
	if len(attempts) != 1 {
		t.Fatalf("image_generation Attempts = %d, want 1", len(attempts))
	}
	assertReferencePort(t, attempts[0]["input"], assetID, ref)

	// --- the Adapter forwarded it to the Provider, credential-free and storage-free.
	dispatches := fx.tasks.dispatches()
	if len(dispatches) != 1 {
		t.Fatalf("dispatches to the Mock Provider = %d, want 1", len(dispatches))
	}
	var dispatched struct {
		Reference json.RawMessage `json:"reference"`
	}
	if err := json.Unmarshal(dispatches[0], &dispatched); err != nil {
		t.Fatalf("decode the dispatch body: %v", err)
	}
	if len(dispatched.Reference) == 0 {
		t.Fatalf("the dispatch body carries no `reference`, so the Reference Image never reached the Provider: %s", dispatches[0])
	}
	reference, err := domain.ParseImageRef(dispatched.Reference)
	if err != nil {
		t.Fatalf("dispatched `reference` is not a valid ImageRef: %v", err)
	}
	if reference.Source != domain.ImageSourceAsset || reference.Asset == nil || reference.Asset.AssetID != assetID {
		t.Errorf("dispatched reference = %+v, want the `source: ASSET` ref of %q", reference, assetID)
	}

	// --- the Run still ends with the Provider's own EXTERNAL image, not the reference.
	output, _ := run["output"].(map[string]any)
	assertRunOutputImageRef(t, output)

	// --- nothing about how Emberling stores the Asset crossed any boundary (10-ops §4).
	eventsJSON, err := json.Marshal(env.listEvents(t, runID))
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}
	storageKey := "/" + assetID
	for _, surface := range []struct {
		name string
		body []byte
	}{
		{"the Provider dispatch body", dispatches[0]},
		{"the image_generation Node detail", imageRaw},
		{"the image_input Node detail", imageInputRaw},
		{"the Event stream", eventsJSON},
	} {
		if strings.Contains(string(surface.body), storageKey) || strings.Contains(strings.ToLower(string(surface.body)), "storagekey") {
			t.Errorf("%s carries the Asset's internal storage key: %s", surface.name, surface.body)
		}
	}

	// --- the Provider was given nothing to fetch with: the reference it received is the
	// AssetRef alone, with no resolved URL and no credential, so a Provider that wanted the
	// bytes could not have retrieved them. Forwarding is the Adapter's whole job here
	// (CLAUDE.md: an Adapter normalises one Provider interaction and selects nothing).
	var dispatchedReference map[string]any
	if err := json.Unmarshal(dispatched.Reference, &dispatchedReference); err != nil {
		t.Fatalf("decode the dispatched reference: %v", err)
	}
	if got, want := sortedKeys(dispatchedReference), []string{"asset", "source"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("dispatched reference members = %v, want exactly %v (no resolved uri, no signed URL)", got, want)
	}
}

// assertReferencePort checks that the Attempt input image_generation actually ran with
// carries the Image Input's `source: ASSET` ImageRef on its `reference` port.
func assertReferencePort(t *testing.T, rawInput any, assetID string, want map[string]any) {
	t.Helper()
	input, ok := rawInput.(map[string]any)
	if !ok {
		t.Fatalf("image_generation Attempt input = %v, want a JSON object", rawInput)
	}
	reference, ok := input["reference"].(map[string]any)
	if !ok {
		t.Fatalf("image_generation Attempt input carries no `reference` port: %v", input)
	}
	if source, _ := reference["source"].(string); source != "ASSET" {
		t.Errorf("reference.source = %v, want %q", reference["source"], "ASSET")
	}
	asset, _ := reference["asset"].(map[string]any)
	if asset == nil {
		t.Fatalf("`source: ASSET` reference carries no asset: %v", reference)
	}
	for _, field := range []string{"assetId", "mediaType", "sizeBytes", "sha256"} {
		if asset[field] != want[field] {
			t.Errorf("reference.asset.%s = %v, want the uploaded AssetRef's %v", field, asset[field], want[field])
		}
	}
	if asset["assetId"] != assetID {
		t.Errorf("reference.asset.assetId = %v, want %q", asset["assetId"], assetID)
	}
}
