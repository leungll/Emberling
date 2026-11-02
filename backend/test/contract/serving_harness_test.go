//go:build integration

// This file is the harness of the serving fault variants: many end users submit the
// photo effect serving template through the real asynchronous image path (the real
// image_generation Node, the real mocktask Adapter and the real Mock Provider with its
// test controls on), while a test injects one fault at a time. Every fault window is
// opened by a hook -- the Provider's barrier and held hook, a per-task callback gate, a
// stopped Backend, a Reconciler pass driven by the test over an injected clock -- never
// by sleeping.
//
// The ledger helpers compare, per external task id, what Emberling committed (Callback
// Bindings, Attempts and Events, read through the public API) with what the Provider
// itself recorded, which is the evidence the serving story is judged by: every task the
// Provider created is bound to exactly one NodeRun, no submission is lost and no image is
// generated twice.
package contract

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/fixture/mockprovider"
	"github.com/leungll/Emberling/backend/internal/reconciler"
)

// servingUsers is how many end users a serving variant submits for at once.
const servingUsers = 20

// servingInFlightLimit is the Provider's in-flight task limit in these tests. Nothing in
// the serving template caps concurrency, so the honest bound is one task per user.
const servingInFlightLimit = servingUsers

// servingWait bounds every wait for a fact a serving variant expects.
const servingWait = 30 * time.Second

// servingImageNode is the serving template's Image Generation node id.
const servingImageNode = "node_image"

// ---------------------------------------------------------------------------------------
// The Provider side
// ---------------------------------------------------------------------------------------

// servingCallbacks is the http.RoundTripper the Mock Provider delivers callbacks with. It
// points every delivery at the current Backend, so a delivery minted before a restart
// reaches the restarted one, and it holds each delivery until the test releases its
// task, or every task at once.
type servingCallbacks struct {
	inner http.RoundTripper
	done  chan struct{}

	mu       sync.Mutex
	target   string
	open     bool
	released map[string]bool
	changed  chan struct{}
}

func newServingCallbacks(open bool) *servingCallbacks {
	return &servingCallbacks{
		inner:    http.DefaultTransport,
		done:     make(chan struct{}),
		open:     open,
		released: map[string]bool{},
		changed:  make(chan struct{}),
	}
}

func (c *servingCallbacks) setTarget(origin string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.target = origin
}

// releaseAll lets every waiting and future delivery through.
func (c *servingCallbacks) releaseAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.open = true
	close(c.changed)
	c.changed = make(chan struct{})
}

// releaseTasks lets the deliveries of the named tasks through.
func (c *servingCallbacks) releaseTasks(ids ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		c.released[id] = true
	}
	close(c.changed)
	c.changed = make(chan struct{})
}

// gate reports whether a delivery for id may proceed, and otherwise the channel that is
// closed at the next release.
func (c *servingCallbacks) gate(id string) (bool, <-chan struct{}, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.open || c.released[id], c.changed, c.target
}

func (c *servingCallbacks) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := readAndRestoreRequestBody(req)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		ExternalTaskID string `json:"externalTaskId"`
	}
	_ = json.Unmarshal(body, &envelope)

	var target string
	for {
		var (
			pass    bool
			changed <-chan struct{}
		)
		pass, changed, target = c.gate(envelope.ExternalTaskID)
		if pass {
			break
		}
		select {
		case <-changed:
		case <-c.done:
			return nil, fmt.Errorf("servingCallbacks: fixture shut down before the callback was released")
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}

	origin, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("servingCallbacks: parse target %q: %w", target, err)
	}
	outbound := req.Clone(req.Context())
	outbound.URL.Scheme = origin.Scheme
	outbound.URL.Host = origin.Host
	outbound.Host = origin.Host
	return c.inner.RoundTrip(outbound)
}

// servingFixture is one in-process Mock Provider with its test controls on, plus the
// uploaded user photo every submission carries. The Provider outlives a Backend restart,
// as a real external Provider does.
type servingFixture struct {
	provider  *httptest.Server
	callbacks *servingCallbacks
	// held receives one label per request the Provider's barrier holds.
	held chan string
	// capacity, when set, limits the Backends this fixture starts to that many in-flight
	// tasks per Provider.
	capacity map[string]int
}

func newServingFixture(t *testing.T, callbacksOpen bool) *servingFixture {
	t.Helper()
	record, err := mockprovider.OpenRecord(filepath.Join(t.TempDir(), "dispatch-record.jsonl"))
	if err != nil {
		t.Fatalf("open mock provider record: %v", err)
	}
	callbacks := newServingCallbacks(callbacksOpen)
	held := make(chan string, 4*servingUsers)
	dispatcher := mockprovider.NewDispatcher(&http.Client{Transport: callbacks, Timeout: e2eHTTPTimeout})
	server := mockprovider.NewServer(dispatcher,
		mockprovider.WithTestControls(record),
		mockprovider.WithHeldHook(held),
		mockprovider.WithInFlightLimit(servingInFlightLimit),
	)
	provider := httptest.NewServer(server)

	t.Cleanup(func() {
		server.StopHolding()
		provider.Close()
		close(callbacks.done)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := dispatcher.Shutdown(ctx); err != nil {
			t.Logf("mock provider dispatcher shutdown: %v", err)
		}
		if err := record.Close(); err != nil {
			t.Logf("close mock provider record: %v", err)
		}
	})
	return &servingFixture{provider: provider, callbacks: callbacks, held: held}
}

// startBackend wires one Backend against this Provider. A non-nil previous Backend is
// the process image that was stopped: the new one reopens its database and Asset volume,
// which is all a restarted process has. clock may be nil for the system clock.
func (f *servingFixture) startBackend(t *testing.T, previous *testEnv, clock domain.Clock) *testEnv {
	t.Helper()
	opts := testEnvOptions{
		MockTaskBaseURL:  f.provider.URL,
		TaskClient:       &http.Client{Timeout: e2eHTTPTimeout},
		Clock:            clock,
		ProviderCapacity: f.capacity,
	}
	if previous != nil {
		opts.Pool = previous.pool
		opts.AssetStorageRoot = previous.assetRoot
	}
	env := newTestEnvWithOptions(t, opts)
	f.callbacks.setTarget(env.server.URL)
	return env
}

// control calls one of the Provider's /control routes and decodes its JSON answer.
func (f *servingFixture) control(t *testing.T, method, path string, body any) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal control body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.provider.URL+path, reader)
	if err != nil {
		t.Fatalf("build control request: %v", err)
	}
	resp, err := f.provider.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, path, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s status = %d, body=%s", method, path, resp.StatusCode, raw)
	}
	return decodeBody[map[string]any](t, raw)
}

// waitHeld blocks until the Provider's barrier has held n more requests.
func (f *servingFixture) waitHeld(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-f.held:
		case <-time.After(servingWait):
			t.Fatalf("the Provider held %d of %d dispatches within %s", i, n, servingWait)
		}
	}
}

// providerRecordLine is one line of the Provider's dispatch record, as GET /control/record
// serves it.
type providerRecordLine struct {
	Seq            int64  `json:"seq"`
	Event          string `json:"event"`
	Kind           string `json:"kind"`
	ExternalTaskID string `json:"externalTaskId"`
	Status         int    `json:"status"`
	TaskStatus     string `json:"taskStatus"`
	Replayed       bool   `json:"replayed"`
	OverLimit      bool   `json:"overLimit"`
}

// record reads every line of the Provider's dispatch record after seq.
func (f *servingFixture) record(t *testing.T, after int64) []providerRecordLine {
	t.Helper()
	var lines []providerRecordLine
	for {
		resp, err := f.provider.Client().Get(fmt.Sprintf("%s/control/record?after=%d", f.provider.URL, after))
		if err != nil {
			t.Fatalf("GET /control/record: %v", err)
		}
		truncated := resp.Header.Get("X-Mockprovider-Record-Truncated") == "true"
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 64<<10), 64<<10)
		for scanner.Scan() {
			var line providerRecordLine
			if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
				_ = resp.Body.Close()
				t.Fatalf("decode record line %q: %v", scanner.Text(), err)
			}
			lines = append(lines, line)
			after = line.Seq
		}
		err = scanner.Err()
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("read /control/record: %v", err)
		}
		if !truncated {
			return lines
		}
	}
}

// lastSeq is the record's highest seq so far, the baseline a variant compares after.
func (f *servingFixture) lastSeq(t *testing.T) int64 {
	t.Helper()
	lines := f.record(t, 0)
	if len(lines) == 0 {
		return 0
	}
	return lines[len(lines)-1].Seq
}

// waitRecord waits until the record after seq holds at least n lines matching match.
func (f *servingFixture) waitRecord(t *testing.T, after int64, n int, what string, match func(providerRecordLine) bool) {
	t.Helper()
	deadline := time.Now().Add(servingWait)
	for {
		count := 0
		for _, line := range f.record(t, after) {
			if match(line) {
				count++
			}
		}
		if count >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Provider record shows %d of %d %s within %s", count, n, what, servingWait)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------------------
// The serving template and its submissions
// ---------------------------------------------------------------------------------------

// servingDefinition reads the serving template fixture as a Definition request body.
func servingDefinition(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../fixtures/definitions/serving_template.json")
	if err != nil {
		t.Fatalf("read serving_template.json: %v", err)
	}
	definition := decodeBody[map[string]any](t, raw)
	delete(definition, "workflowId")
	return definition
}

// setServingNodeConfig sets one config member of one serving template node.
func setServingNodeConfig(t *testing.T, definition map[string]any, nodeID, key string, value any) {
	t.Helper()
	nodes, _ := definition["nodes"].([]any)
	for _, n := range nodes {
		node, _ := n.(map[string]any)
		if id, _ := node["id"].(string); id == nodeID {
			config, _ := node["config"].(map[string]any)
			config[key] = value
			return
		}
	}
	t.Fatalf("serving template has no node %q", nodeID)
}

// servingTemplateWithDirective makes the Effect Prompt start with a Mock Provider
// directive, so every dispatch selects that Provider scenario while each user's request
// still follows it as ordinary prompt text.
func servingTemplateWithDirective(t *testing.T, directive string) map[string]any {
	t.Helper()
	definition := servingDefinition(t)
	setServingNodeConfig(t, definition, "node_prompt", "template", directive+":{{text}}")
	return definition
}

// saveServingDefinition creates the Definition and returns its workflow id and version.
func saveServingDefinition(t *testing.T, env *testEnv, definition map[string]any) (string, int) {
	t.Helper()
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", definition)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, body=%s", resp.StatusCode, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)
	return workflowID, int(version)
}

// servingPhoto uploads the user photo every serving submission carries.
func servingPhoto(t *testing.T, env *testEnv) map[string]any {
	t.Helper()
	return env.uploadPNG(t, bytes.Repeat([]byte("user photo pixels"), 16))
}

// servingSubmission is one end user's POST /api/runs.
type servingSubmission struct {
	WorkflowID string
	Version    int
	Photo      map[string]any
	Request    string
	// IdempotencyKey is sent as the Idempotency-Key header when non-empty.
	IdempotencyKey string
}

// servingSubmitResult is how the Backend answered one submission.
type servingSubmitResult struct {
	Status   int
	RunID    string
	Replayed bool
	Err      error
}

// submit sends one submission without touching testing.T, so it is safe from the
// goroutines a burst starts.
func (e *testEnv) submit(s servingSubmission) servingSubmitResult {
	raw, err := json.Marshal(map[string]any{
		"workflowId": s.WorkflowID, "definitionVersion": s.Version,
		"input": map[string]any{"photo": s.Photo, "request": s.Request},
	})
	if err != nil {
		return servingSubmitResult{Err: err}
	}
	req, err := http.NewRequest(http.MethodPost, e.server.URL+"/api/runs", bytes.NewReader(raw))
	if err != nil {
		return servingSubmitResult{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if s.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", s.IdempotencyKey)
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		return servingSubmitResult{Err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return servingSubmitResult{Err: err}
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &created)
	result := servingSubmitResult{
		Status:   resp.StatusCode,
		RunID:    created.ID,
		Replayed: resp.Header.Get("Idempotent-Replayed") == "true",
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		result.Err = fmt.Errorf("POST /api/runs status %d body %s", resp.StatusCode, body)
	}
	return result
}

// submitConcurrently sends every submission from its own goroutine. All goroutines wait
// on one start barrier, so the submissions reach the Backend together rather than in the
// order they were started.
func (e *testEnv) submitConcurrently(t *testing.T, submissions []servingSubmission) []servingSubmitResult {
	t.Helper()
	results := make([]servingSubmitResult, len(submissions))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, s := range submissions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i] = e.submit(s)
		}()
	}
	close(start)
	wg.Wait()
	for i, r := range results {
		if r.Err != nil {
			t.Fatalf("submission %d: %v", i, r.Err)
		}
	}
	return results
}

// servingUsersSubmit submits one request for each of n users at once and returns the
// created Run ids in user order.
func (e *testEnv) servingUsersSubmit(t *testing.T, workflowID string, version int, photo map[string]any, n int) []string {
	t.Helper()
	submissions := make([]servingSubmission, n)
	for i := range submissions {
		submissions[i] = servingSubmission{
			WorkflowID: workflowID, Version: version, Photo: photo,
			Request: fmt.Sprintf("Portrait of user %d.", i+1),
		}
	}
	runIDs := make([]string, n)
	for i, r := range e.submitConcurrently(t, submissions) {
		if r.Status != http.StatusCreated {
			t.Fatalf("submission %d status = %d, want 201", i, r.Status)
		}
		runIDs[i] = r.RunID
	}
	return runIDs
}

// waitAllCompleted waits for every Run to end and fails unless each one COMPLETED.
func (e *testEnv) waitAllCompleted(t *testing.T, runIDs []string) {
	t.Helper()
	for _, runID := range runIDs {
		snap := e.waitForTerminal(t, runID, servingWait)
		run, _ := snap["run"].(map[string]any)
		if status, _ := run["status"].(string); status != "COMPLETED" {
			t.Fatalf("run %s status = %q, want COMPLETED\n%s", runID, status, runStateDump(t, e, runID))
		}
	}
}

// allTerminal reports whether every Run has ended.
func (e *testEnv) allTerminal(t *testing.T, runIDs []string) bool {
	t.Helper()
	for _, runID := range runIDs {
		if status := e.runStatus(t, runID); status != "COMPLETED" && status != "FAILED" {
			return false
		}
	}
	return true
}

// reconcileUntilTerminal stands in for the restarted process's Reconciler ticker: it
// drives one pass at a time, moving the injected clock past the persisted deadline or
// backoff each pass waits for, until every Run has ended. The number of passes is bounded.
func (e *testEnv) reconcileUntilTerminal(t *testing.T, clock *takeoverClock, step time.Duration, runIDs []string) {
	t.Helper()
	rec := reconciler.New(reconciler.Config{UoW: e.uow, Executor: e.execution, Clock: clock, BatchLimit: 4 * servingUsers})
	deadline := time.Now().Add(servingWait)
	for pass := 1; ; pass++ {
		clock.advance(step)
		report, err := rec.RunOnce(context.Background())
		if err != nil || len(report.Errors) != 0 {
			t.Fatalf("reconciler pass %d: err=%v errors=%v", pass, err, report.Errors)
		}
		if e.allTerminal(t, runIDs) {
			return
		}
		if pass >= 50 || time.Now().After(deadline) {
			t.Fatalf("runs did not end after %d reconciler passes", pass)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// imageNodeRun returns the Run's Image Generation NodeRun id and its Node detail.
func (e *testEnv) imageNodeRun(t *testing.T, runID string) (string, map[string]any) {
	t.Helper()
	nodeRun := e.nodeRunOf(t, runID, servingImageNode)
	if nodeRun == nil {
		t.Fatalf("run %s has no %s NodeRun", runID, servingImageNode)
	}
	nodeRunID, _ := nodeRun["id"].(string)
	detail, _ := e.nodeRunDetail(t, runID, nodeRunID)
	return nodeRunID, detail
}

// boundTaskOf returns the external task id the Run's image NodeRun is bound to; it fails
// unless exactly one task is bound.
func (e *testEnv) boundTaskOf(t *testing.T, runID string) string {
	t.Helper()
	_, detail := e.imageNodeRun(t, runID)
	tasks := map[string]bool{}
	for _, attempt := range attemptsOf(t, detail) {
		if binding := bindingOf(attempt); binding != nil {
			id, _ := binding["externalTaskId"].(string)
			tasks[id] = true
		}
	}
	if len(tasks) != 1 {
		t.Fatalf("run %s image NodeRun is bound to %d external tasks, want 1: %v", runID, len(tasks), tasks)
	}
	for id := range tasks {
		return id
	}
	return ""
}

// waitAllImagesWaiting waits until every Run's image NodeRun is WAITING_CALLBACK.
func (e *testEnv) waitAllImagesWaiting(t *testing.T, runIDs []string) {
	t.Helper()
	for _, runID := range runIDs {
		e.waitForNodeRunStatus(t, runID, servingImageNode, "WAITING_CALLBACK", servingWait)
	}
}

// ---------------------------------------------------------------------------------------
// The per-task ledger
// ---------------------------------------------------------------------------------------

// servingLedger is the per-external-task comparison of Emberling's committed facts with
// the Provider's record, for a set of Runs and the record lines written after a baseline.
type servingLedger struct {
	Runs      int
	Completed int
	// Bound maps each external task id to the image NodeRuns bound to it.
	Bound map[string][]string
	// Created counts, per external task id, the record lines on which the Provider
	// accepted a new task: responded 202, or responseDropped when the answer was lost.
	Created map[string]int
	// NodeRunsWithSeveralTasks lists image NodeRuns bound to more than one task.
	NodeRunsWithSeveralTasks []string
	// ReplayedDispatches counts dispatches the Provider answered with an existing task.
	ReplayedDispatches int
	// Takeovers counts NODE_DISPATCHED Events that report a replayed task.
	Takeovers int
	// CallbackCompleted and PollCompleted count image NODE_COMPLETED Events by source.
	CallbackCompleted int
	PollCompleted     int
	// CallbackDeliveries counts the Provider's callback delivery attempts per task.
	CallbackDeliveries map[string]int
	// ImageEvents counts image NodeRun Events by type, across the Runs.
	ImageEvents map[string]int
	// OverLimitLines counts acceptances the Provider marked above its in-flight limit.
	OverLimitLines int
	InFlightPeak   int
	InFlightLimit  int
}

// readServingLedger builds the ledger for runIDs from the Backend's API and the Provider's
// record after recordAfter.
func readServingLedger(t *testing.T, env *testEnv, fx *servingFixture, runIDs []string, recordAfter int64) servingLedger {
	t.Helper()
	ledger := servingLedger{
		Runs:               len(runIDs),
		Bound:              map[string][]string{},
		Created:            map[string]int{},
		CallbackDeliveries: map[string]int{},
		ImageEvents:        map[string]int{},
	}
	for _, runID := range runIDs {
		if env.runStatus(t, runID) == "COMPLETED" {
			ledger.Completed++
		}
		nodeRunID, detail := env.imageNodeRun(t, runID)
		tasks := map[string]bool{}
		for _, attempt := range attemptsOf(t, detail) {
			if binding := bindingOf(attempt); binding != nil {
				id, _ := binding["externalTaskId"].(string)
				tasks[id] = true
			}
		}
		for id := range tasks {
			ledger.Bound[id] = append(ledger.Bound[id], nodeRunID)
		}
		if len(tasks) > 1 {
			ledger.NodeRunsWithSeveralTasks = append(ledger.NodeRunsWithSeveralTasks, nodeRunID)
		}
		for _, event := range env.eventsForNodeRun(t, runID, nodeRunID) {
			eventType, _ := event["type"].(string)
			ledger.ImageEvents[eventType]++
			payload := payloadOf(t, event)
			switch eventType {
			case "NODE_DISPATCHED":
				if replayed, _ := payload["replayed"].(bool); replayed {
					ledger.Takeovers++
				}
			case "NODE_COMPLETED":
				switch payload["completionSource"] {
				case "CALLBACK":
					ledger.CallbackCompleted++
				case "PROVIDER_POLL":
					ledger.PollCompleted++
				}
			}
		}
	}
	for _, line := range fx.record(t, recordAfter) {
		if line.Kind != "task" {
			continue
		}
		switch {
		case line.Event == "responded" && line.Status == http.StatusAccepted && !line.Replayed,
			line.Event == "responseDropped":
			ledger.Created[line.ExternalTaskID]++
		case line.Event == "responded" && line.Replayed:
			ledger.ReplayedDispatches++
		case line.Event == "callback":
			ledger.CallbackDeliveries[line.ExternalTaskID]++
		}
		if line.OverLimit {
			ledger.OverLimitLines++
		}
	}
	inFlight := fx.control(t, http.MethodGet, "/control/inflight", nil)
	peak, _ := inFlight["peak"].(float64)
	limit, _ := inFlight["limit"].(float64)
	ledger.InFlightPeak, ledger.InFlightLimit = int(peak), int(limit)
	return ledger
}

// assertOneToOne fails unless the ledger shows no lost submission, no duplicate
// generation and a Provider that stayed within its in-flight limit: every Run COMPLETED,
// every created task was created once and is bound to exactly one image NodeRun, every
// bound task is one the Provider created, and no NodeRun is bound to two tasks.
func (l servingLedger) assertOneToOne(t *testing.T) {
	t.Helper()
	if lost := l.Runs - l.Completed; lost != 0 {
		t.Errorf("lost submissions = %d, want 0 (%d of %d Runs COMPLETED)", lost, l.Completed, l.Runs)
	}
	for id, n := range l.Created {
		if n != 1 {
			t.Errorf("task %s was created %d times, want 1", id, n)
		}
		if len(l.Bound[id]) != 1 {
			t.Errorf("created task %s is bound to %d NodeRuns, want 1 (a task no Run owns is a duplicate generation)", id, len(l.Bound[id]))
		}
	}
	for id, nodeRuns := range l.Bound {
		if l.Created[id] == 0 {
			t.Errorf("bound task %s (NodeRuns %v) is not a task the Provider created", id, nodeRuns)
		}
	}
	if len(l.NodeRunsWithSeveralTasks) != 0 {
		t.Errorf("image NodeRuns bound to several tasks = %v, want none", l.NodeRunsWithSeveralTasks)
	}
	if len(l.Created) != l.Runs {
		t.Errorf("tasks created = %d, want one per Run (%d)", len(l.Created), l.Runs)
	}
	if got := l.ImageEvents["NODE_COMPLETED"]; got != l.Runs {
		t.Errorf("image NODE_COMPLETED Events = %d, want one per Run (%d)", got, l.Runs)
	}
	if l.InFlightLimit > 0 && l.InFlightPeak > l.InFlightLimit {
		t.Errorf("Provider in-flight peak = %d, above its limit %d", l.InFlightPeak, l.InFlightLimit)
	}
	if l.OverLimitLines != 0 {
		t.Errorf("Provider acceptances over the in-flight limit = %d, want 0", l.OverLimitLines)
	}
	t.Logf("ledger: runs=%d completed=%d tasks created=%d bound=%d replayed dispatches=%d takeovers=%d callback-completed=%d poll-completed=%d in-flight peak=%d/%d",
		l.Runs, l.Completed, len(l.Created), len(l.Bound), l.ReplayedDispatches, l.Takeovers,
		l.CallbackCompleted, l.PollCompleted, l.InFlightPeak, l.InFlightLimit)
}
