package mockprovider

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// controlledProvider is a Server with test controls enabled, served in-process, together
// with the barrier hook and callback hook that let a test wait on observable state.
type controlledProvider struct {
	srv        *httptest.Server
	server     *Server
	dispatcher *Dispatcher
	record     *Record
	recordPath string
	held       chan string
	notify     chan callbackOutcome
}

func newControlledProvider(t *testing.T, recordPath string) *controlledProvider {
	t.Helper()
	record, err := OpenRecord(recordPath)
	if err != nil {
		t.Fatalf("OpenRecord() error = %v", err)
	}
	dispatcher := NewDispatcher(nil)
	notify := make(chan callbackOutcome, 8)
	dispatcher.notify = notify
	server := NewServer(dispatcher, WithTestControls(record))
	held := make(chan string, 8)
	server.controls.heldHook = held
	p := &controlledProvider{
		srv:        httptest.NewServer(server),
		server:     server,
		dispatcher: dispatcher,
		record:     record,
		recordPath: recordPath,
		held:       held,
		notify:     notify,
	}
	t.Cleanup(p.close)
	return p
}

// close stops the provider. StopHolding runs first so a request a failed test left held
// cannot keep httptest.Server.Close waiting forever.
func (p *controlledProvider) close() {
	p.server.StopHolding()
	p.srv.Close()
	_ = p.record.Close()
}

// waitHeld blocks until the barrier reports one more held request and returns its label.
func (p *controlledProvider) waitHeld(t *testing.T) string {
	t.Helper()
	select {
	case label := <-p.held:
		return label
	case <-time.After(2 * time.Second):
		t.Fatal("waitHeld: timed out waiting for a request to be held")
		return ""
	}
}

func (p *controlledProvider) control(t *testing.T, path string, body string) barrierResponse {
	t.Helper()
	resp, err := http.Post(p.srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s error = %v", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s status = %d, want 200", path, resp.StatusCode)
	}
	var out barrierResponse
	decodeBody(t, resp, &out)
	return out
}

func (p *controlledProvider) barrierState(t *testing.T) barrierResponse {
	t.Helper()
	resp, err := http.Get(p.srv.URL + "/control/barrier")
	if err != nil {
		t.Fatalf("GET /control/barrier error = %v", err)
	}
	var out barrierResponse
	decodeBody(t, resp, &out)
	return out
}

// recordLines fetches the record over HTTP and decodes every line.
func (p *controlledProvider) recordLines(t *testing.T, query string) []recordEntry {
	t.Helper()
	resp, err := http.Get(p.srv.URL + "/control/record" + query)
	if err != nil {
		t.Fatalf("GET /control/record error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /control/record status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/x-ndjson" {
		t.Fatalf("Content-Type = %q, want application/x-ndjson", got)
	}
	var entries []recordEntry
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var entry recordEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("record line %q is not JSON: %v", scanner.Text(), err)
		}
		entries = append(entries, entry)
	}
	return entries
}

// asyncTask posts a task request on its own goroutine and delivers the status code.
func (p *controlledProvider) asyncTask(t *testing.T, req taskRequest) <-chan int {
	t.Helper()
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	statuses := make(chan int, 1)
	go func() {
		resp, err := http.Post(p.srv.URL+"/v1/tasks", "application/json", bytes.NewReader(encoded))
		if err != nil {
			statuses <- 0
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		statuses <- resp.StatusCode
	}()
	return statuses
}

func waitStatus(t *testing.T, statuses <-chan int) int {
	t.Helper()
	select {
	case status := <-statuses:
		return status
	case <-time.After(2 * time.Second):
		t.Fatal("waitStatus: timed out waiting for a response")
		return 0
	}
}

func eventsFor(entries []recordEntry, externalTaskID string) []string {
	var events []string
	for _, entry := range entries {
		if entry.ExternalTaskID == externalTaskID {
			events = append(events, entry.Event)
		}
	}
	return events
}

func TestBarrier_PausedTask_ArrivalRecordedBeforeResponseAndHeldUntilRelease(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))

	p.control(t, "/control/pause", `{"kinds":["task"]}`)
	statuses := p.asyncTask(t, taskRequest{ExternalTaskID: "task-held", CallbackURL: receiver.server.URL, CallbackToken: "tok-held"})

	if label := p.waitHeld(t); label != "task-held" {
		t.Fatalf("held label = %q, want task-held", label)
	}
	if got := eventsFor(p.recordLines(t, ""), "task-held"); len(got) != 1 || got[0] != recordArrived {
		t.Fatalf("record events while held = %v, want [arrived]", got)
	}
	state := p.barrierState(t)
	if !state.Paused || len(state.Held) != 1 || state.Held[0].ExternalTaskID != "task-held" {
		t.Fatalf("barrier while held = %+v, want paused with task-held held", state)
	}
	select {
	case status := <-statuses:
		t.Fatalf("response %d sent while the request was held", status)
	default:
	}
	if len(receiver.Calls()) != 0 {
		t.Fatal("callback delivered while the dispatch was held")
	}

	if released := p.control(t, "/control/release", ""); released.Released != 1 || released.Paused {
		t.Fatalf("release = %+v, want 1 released and not paused", released)
	}
	if status := waitStatus(t, statuses); status != http.StatusAccepted {
		t.Fatalf("status after release = %d, want 202", status)
	}
	waitForCalls(t, p.notify, 1)

	got := eventsFor(p.recordLines(t, ""), "task-held")
	want := []string{recordArrived, recordReleased}
	if len(got) < 4 || got[0] != want[0] || got[1] != want[1] || !contains(got[2:], recordResponded) || !contains(got[2:], recordCallback) {
		t.Fatalf("record events = %v, want arrived, released, then responded and callback", got)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestBarrier_TwoConcurrentRequests_BothRecordedAndReleasedByOneRelease(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))

	p.control(t, "/control/pause", "")
	first := p.asyncTask(t, taskRequest{ExternalTaskID: "task-a", CallbackURL: receiver.server.URL, CallbackToken: "tok-a", DelayMs: delaySpec{Mode: delayLost}})
	second := p.asyncTask(t, taskRequest{ExternalTaskID: "task-b", CallbackURL: receiver.server.URL, CallbackToken: "tok-b", DelayMs: delaySpec{Mode: delayLost}})
	labels := map[string]bool{p.waitHeld(t): true, p.waitHeld(t): true}
	if !labels["task-a"] || !labels["task-b"] {
		t.Fatalf("held labels = %v, want task-a and task-b", labels)
	}
	if held := p.barrierState(t).Held; len(held) != 2 {
		t.Fatalf("held = %+v, want two held requests", held)
	}

	if released := p.control(t, "/control/release", "").Released; released != 2 {
		t.Fatalf("released = %d, want 2", released)
	}
	if status := waitStatus(t, first); status != http.StatusAccepted {
		t.Fatalf("first status = %d, want 202", status)
	}
	if status := waitStatus(t, second); status != http.StatusAccepted {
		t.Fatalf("second status = %d, want 202", status)
	}
	entries := p.recordLines(t, "")
	for _, id := range []string{"task-a", "task-b"} {
		if got := eventsFor(entries, id); len(got) != 3 || got[0] != recordArrived || got[1] != recordReleased || got[2] != recordResponded {
			t.Fatalf("events for %s = %v, want [arrived released responded]", id, got)
		}
	}
	if held := p.barrierState(t).Held; len(held) != 0 {
		t.Fatalf("held after release = %+v, want none", held)
	}
}

func TestBarrier_ReleaseWithNothingHeld_IsNoOpAndPauseAfterReleaseRearms(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))

	if state := p.control(t, "/control/release", ""); state.Released != 0 || state.Paused {
		t.Fatalf("release while unpaused = %+v, want no-op", state)
	}
	p.control(t, "/control/pause", "")
	if state := p.control(t, "/control/release", ""); state.Released != 0 || state.Paused {
		t.Fatalf("release with nothing held = %+v, want 0 released and not paused", state)
	}
	if state := p.control(t, "/control/release", ""); state.Released != 0 || state.Paused {
		t.Fatalf("second release = %+v, want no-op", state)
	}

	// Unpaused: the request is answered without ever being held.
	if status := waitStatus(t, p.asyncTask(t, taskRequest{ExternalTaskID: "task-free", CallbackURL: receiver.server.URL, CallbackToken: "tok", DelayMs: delaySpec{Mode: delayLost}})); status != http.StatusAccepted {
		t.Fatalf("unpaused status = %d, want 202", status)
	}
	select {
	case label := <-p.held:
		t.Fatalf("request %q held while the barrier was released", label)
	default:
	}

	// Pausing again re-arms the barrier.
	p.control(t, "/control/pause", "")
	statuses := p.asyncTask(t, taskRequest{ExternalTaskID: "task-rearmed", CallbackURL: receiver.server.URL, CallbackToken: "tok", DelayMs: delaySpec{Mode: delayLost}})
	if label := p.waitHeld(t); label != "task-rearmed" {
		t.Fatalf("held label = %q, want task-rearmed", label)
	}
	if released := p.control(t, "/control/release", "").Released; released != 1 {
		t.Fatalf("released = %d, want 1", released)
	}
	if status := waitStatus(t, statuses); status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", status)
	}
}

func TestBarrier_KindFilter_HoldsOnlyMatchingRequests(t *testing.T) {
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))

	p.control(t, "/control/pause", `{"kinds":["task"]}`)
	resp := postJSON(t, p.srv.URL+"/v1/text/generate", generateRequest{Prompt: "hi"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("generate status = %d, want 200 while only tasks are paused", resp.StatusCode)
	}
	resp.Body.Close()
	if state := p.barrierState(t); len(state.Held) != 0 || len(state.Kinds) != 1 || state.Kinds[0] != kindTask {
		t.Fatalf("barrier = %+v, want task-only filter and nothing held", state)
	}

	reject, err := http.Post(p.srv.URL+"/control/pause", "application/json", strings.NewReader(`{"kinds":["image"]}`))
	if err != nil {
		t.Fatalf("POST /control/pause error = %v", err)
	}
	reject.Body.Close()
	if reject.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown kind status = %d, want 400", reject.StatusCode)
	}
}

func TestBarrier_StopHolding_RejectsHeldRequestAndRecordsIt(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))

	p.control(t, "/control/pause", "")
	statuses := p.asyncTask(t, taskRequest{ExternalTaskID: "task-stopped", CallbackURL: receiver.server.URL, CallbackToken: "tok"})
	p.waitHeld(t)

	p.server.StopHolding()
	if status := waitStatus(t, statuses); status != http.StatusServiceUnavailable {
		t.Fatalf("status after StopHolding = %d, want 503", status)
	}
	if got := eventsFor(p.recordLines(t, ""), "task-stopped"); len(got) != 3 || got[1] != recordRejected || got[2] != recordResponded {
		t.Fatalf("events = %v, want [arrived rejected responded]", got)
	}
	// Pausing after StopHolding cannot hold anything again.
	p.control(t, "/control/pause", "")
	if status := waitStatus(t, p.asyncTask(t, taskRequest{ExternalTaskID: "task-after-stop", CallbackURL: receiver.server.URL, CallbackToken: "tok", DelayMs: delaySpec{Mode: delayLost}})); status != http.StatusAccepted {
		t.Fatalf("status after stop = %d, want 202", status)
	}
	if len(receiver.Calls()) != 0 {
		t.Fatal("a rejected dispatch delivered a callback")
	}
}

func TestServer_ControlsDisabled_ControlRoutesAreAbsentAndTasksBehaveAsBefore(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()

	dispatcher := NewDispatcher(nil)
	notify := make(chan callbackOutcome, 1)
	dispatcher.notify = notify
	server := NewServer(dispatcher)
	srv := httptest.NewServer(server)
	defer srv.Close()

	if dispatcher.observe != nil {
		t.Fatal("dispatcher observes deliveries without test controls")
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/control/pause"},
		{http.MethodPost, "/control/release"},
		{http.MethodGet, "/control/barrier"},
		{http.MethodGet, "/control/record"},
		{http.MethodPost, "/control/tasks/task-x/callback"},
	} {
		req, err := http.NewRequest(route.method, srv.URL+route.path, nil)
		if err != nil {
			t.Fatalf("NewRequest() error = %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s error = %v", route.method, route.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404", route.method, route.path, resp.StatusCode)
		}
	}

	resp := postJSON(t, srv.URL+"/v1/tasks", taskRequest{ExternalTaskID: "task-plain", CallbackURL: receiver.server.URL, CallbackToken: "tok-plain"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	var body taskResponse
	decodeBody(t, resp, &body)
	if body.ExternalTaskID != "task-plain" {
		t.Fatalf("ExternalTaskID = %q, want task-plain", body.ExternalTaskID)
	}
	waitForCalls(t, notify, 1)
	if calls := receiver.Calls(); len(calls) != 1 || calls[0].Token != "tok-plain" {
		t.Fatalf("Calls() = %+v, want one delivery with the original token", calls)
	}
}

func TestRecord_Restart_AppendsAfterEarlierLinesAndServesThem(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	path := filepath.Join(t.TempDir(), "record.jsonl")

	first := newControlledProvider(t, path)
	if status := waitStatus(t, first.asyncTask(t, taskRequest{ExternalTaskID: "task-before", CallbackURL: receiver.server.URL, CallbackToken: "tok", DelayMs: delaySpec{Mode: delayLost}})); status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", status)
	}
	first.close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if len(before) == 0 {
		t.Fatal("record is empty after the first instance served a request")
	}

	// A new instance over the same file is the restarted Provider.
	second := newControlledProvider(t, path)
	if state := second.barrierState(t); state.Paused {
		t.Fatal("restarted Provider starts paused, want un-paused")
	}
	if status := waitStatus(t, second.asyncTask(t, taskRequest{ExternalTaskID: "task-after", CallbackURL: receiver.server.URL, CallbackToken: "tok", DelayMs: delaySpec{Mode: delayLost}})); status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", status)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.HasPrefix(after, before) || len(after) <= len(before) {
		t.Fatal("record was rewritten instead of appended to")
	}

	entries := second.recordLines(t, "")
	if got := eventsFor(entries, "task-before"); len(got) != 2 {
		t.Fatalf("events for task-before after restart = %v, want [arrived responded]", got)
	}
	if got := eventsFor(entries, "task-after"); len(got) != 2 {
		t.Fatalf("events for task-after = %v, want [arrived responded]", got)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].Seq != entries[i-1].Seq+1 {
			t.Fatalf("seq %d follows %d, want consecutive numbering across the restart", entries[i].Seq, entries[i-1].Seq)
		}
	}

	lastBefore := entries[1].Seq
	if later := second.recordLines(t, "?after="+strconv.FormatInt(lastBefore, 10)); len(later) != 2 || later[0].ExternalTaskID != "task-after" {
		t.Fatalf("record after seq %d = %+v, want only task-after lines", lastBefore, later)
	}
}

func TestRecord_Line_NeverContainsCallbackTokenOrAuthorizationHeader(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))

	const (
		token     = "cbtoken-SECRET-1"
		auth      = "Bearer auth-SECRET-2"
		userInfo  = "user:pass-SECRET-3"
		querySig  = "sig-SECRET-4"
		fragment  = "frag-SECRET-5"
		payloadIn = "payload-SECRET-6"
	)
	callbackURL := strings.Replace(receiver.server.URL, "http://", "http://"+userInfo+"@", 1) + "/api/callbacks?sig=" + querySig + "#" + fragment
	encoded, err := json.Marshal(taskRequest{
		ExternalTaskID: "task-secret",
		CallbackURL:    callbackURL,
		CallbackToken:  token,
		Payload:        json.RawMessage(`{"value":"` + payloadIn + `"}`),
		Outcome:        outcomeWrongToken,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, p.srv.URL+"/v1/tasks", bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/tasks error = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	waitForCalls(t, p.notify, 1)

	// A redelivered callback is recorded through the same path and must be just as clean.
	redeliver, err := http.Post(p.srv.URL+"/control/tasks/task-secret/callback", "application/json", nil)
	if err != nil {
		t.Fatalf("POST redeliver error = %v", err)
	}
	redeliverBody, _ := io.ReadAll(redeliver.Body)
	redeliver.Body.Close()
	if strings.Contains(string(redeliverBody), token) {
		t.Fatal("redeliver response echoes the callback token")
	}

	raw, err := os.ReadFile(p.recordPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 4 {
		t.Fatalf("record has %d lines, want arrived, responded and two callbacks", len(lines))
	}
	for _, line := range lines {
		for _, secret := range []string{token, "SECRET", auth, "Bearer"} {
			if strings.Contains(line, secret) {
				t.Fatalf("record line %q contains %q", line, secret)
			}
		}
	}
	wantTarget := receiver.server.URL + "/api/callbacks"
	for _, entry := range eventsWithTarget(t, lines) {
		if entry != wantTarget {
			t.Fatalf("callbackTarget = %q, want %q", entry, wantTarget)
		}
	}
}

func eventsWithTarget(t *testing.T, lines []string) []string {
	t.Helper()
	var targets []string
	for _, line := range lines {
		var entry recordEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("record line %q is not JSON: %v", line, err)
		}
		if entry.CallbackTarget != "" {
			targets = append(targets, entry.CallbackTarget)
		}
	}
	return targets
}

func TestRedeliver_AcceptedTask_SendsCallbackAgainAndCountsDeliveries(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))

	if status := waitStatus(t, p.asyncTask(t, taskRequest{ExternalTaskID: "task-late", CallbackURL: receiver.server.URL, CallbackToken: "tok-late", DelayMs: delaySpec{Mode: delayLost}})); status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", status)
	}
	if len(receiver.Calls()) != 0 {
		t.Fatal("lost scenario delivered a callback before redeliver")
	}

	for want := 1; want <= 2; want++ {
		resp, err := http.Post(p.srv.URL+"/control/tasks/task-late/callback", "application/json", nil)
		if err != nil {
			t.Fatalf("POST redeliver error = %v", err)
		}
		var out redeliverResponse
		decodeBody(t, resp, &out)
		if !out.Delivered || out.Status != http.StatusOK {
			t.Fatalf("redeliver = %+v, want delivered with receiver status 200", out)
		}
		calls := receiver.Calls()
		if len(calls) != want || calls[want-1].Token != "tok-late" || calls[want-1].Body.ExternalTaskID != "task-late" {
			t.Fatalf("Calls() = %+v, want %d deliveries of task-late with its original token", calls, want)
		}
	}

	var deliveries []int
	for _, entry := range p.recordLines(t, "") {
		if entry.Event == recordCallback && entry.ExternalTaskID == "task-late" {
			deliveries = append(deliveries, entry.Delivery)
		}
	}
	if len(deliveries) != 2 || deliveries[0] != 1 || deliveries[1] != 2 {
		t.Fatalf("recorded deliveries = %v, want [1 2]", deliveries)
	}

	resp, err := http.Post(p.srv.URL+"/control/tasks/task-unknown/callback", "application/json", nil)
	if err != nil {
		t.Fatalf("POST redeliver error = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown task status = %d, want 404", resp.StatusCode)
	}
}

func TestRecord_InvalidRequest_RecordsArrivalAndRejection(t *testing.T) {
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))

	resp := postJSON(t, p.srv.URL+"/v1/tasks", taskRequest{ExternalTaskID: "task-invalid", CallbackToken: "tok"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	entries := p.recordLines(t, "")
	if len(entries) != 2 || entries[0].Event != recordArrived || entries[1].Event != recordResponded || entries[1].Status != http.StatusBadRequest {
		t.Fatalf("record = %+v, want arrived then responded 400", entries)
	}
}
