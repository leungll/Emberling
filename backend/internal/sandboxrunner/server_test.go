package sandboxrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/mockcontrol"
)

const (
	testToken = "plaintext-callback-token-never-recorded"
	testPatch = "--- a/greet.go\r\n+++ b/greet.go\r\n@@ -1 +1 @@\r\n-old\r\n+new\r\n"
)

// delivery is one callback the receiver got.
type delivery struct {
	Token string
	Body  callbackBody
}

// receiver is a fake Emberling callback endpoint.
type receiver struct {
	server *httptest.Server
	got    chan delivery
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	rcv := &receiver{got: make(chan delivery, 8)}
	rcv.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body callbackBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode callback body: %v", err)
		}
		rcv.got <- delivery{Token: r.Header.Get(callbackTokenHeader), Body: body}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(rcv.server.Close)
	return rcv
}

func (r *receiver) url() string { return r.server.URL + "/api/callbacks?trace=secret-query" }

func (r *receiver) next(t *testing.T) delivery {
	t.Helper()
	select {
	case d := <-r.got:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("no callback delivered within 5s")
		return delivery{}
	}
}

// drained returns every callback still queued; call it only after Shutdown, which waits
// for every delivery goroutine.
func (r *receiver) drained() []delivery {
	var out []delivery
	for {
		select {
		case d := <-r.got:
			out = append(out, d)
		default:
			return out
		}
	}
}

// runner is a Server under test served over HTTP.
type runner struct {
	server     *Server
	http       *httptest.Server
	recordPath string
}

func newRunner(t *testing.T, backend Backend, controls bool) *runner {
	t.Helper()
	var opts []Option
	r := &runner{}
	if controls {
		r.recordPath = filepath.Join(t.TempDir(), "record.jsonl")
		record, err := mockcontrol.OpenRecord(r.recordPath)
		if err != nil {
			t.Fatalf("open record: %v", err)
		}
		t.Cleanup(func() { _ = record.Close() })
		store, _, err := OpenRedeliveryStore(RedeliveryPath(r.recordPath))
		if err != nil {
			t.Fatalf("open redelivery store: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		opts = append(opts, WithTestControls(record, store))
	}
	r.server = NewServer(backend, opts...)
	if controls {
		r.server.controls.heldHook = make(chan string, 4)
	}
	r.http = httptest.NewServer(r.server)
	t.Cleanup(func() {
		r.server.StopHolding()
		r.http.Close()
		_ = r.server.Shutdown(context.Background())
	})
	return r
}

func (r *runner) shutdown(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.server.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func (r *runner) post(t *testing.T, body any) (int, map[string]any) {
	t.Helper()
	encoded, _ := json.Marshal(body)
	resp, err := http.Post(r.http.URL+testsPath, "application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("post test: %v", err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded
}

func (r *runner) control(t *testing.T, method, path, body string) {
	t.Helper()
	req, _ := http.NewRequest(method, r.http.URL+"/control"+path, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("control %s: %v", path, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("control %s answered %d", path, resp.StatusCode)
	}
}

// recordLines reads the record over its control route.
func (r *runner) recordLines(t *testing.T) []map[string]any {
	t.Helper()
	resp, err := http.Get(r.http.URL + "/control/record")
	if err != nil {
		t.Fatalf("get record: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("record line %q: %v", line, err)
		}
		lines = append(lines, decoded)
	}
	return lines
}

func request(callbackURL, mock string) map[string]any {
	body := map[string]any{"callbackUrl": callbackURL, "callbackToken": testToken, "baseCommit": "fixture-v1", "patch": testPatch}
	if mock != "" {
		body["mock"] = mock
	}
	return body
}

func dispatch(t *testing.T, r *runner, callbackURL, mock string) string {
	t.Helper()
	status, body := r.post(t, request(callbackURL, mock))
	if status != http.StatusAccepted {
		t.Fatalf("dispatch status = %d (%v), want 202", status, body)
	}
	testID, _ := body["testId"].(string)
	if !strings.HasPrefix(testID, "test_") || len(body) != 1 {
		t.Fatalf("dispatch body = %v, want exactly a testId", body)
	}
	return testID
}

func payloadOf(t *testing.T, d delivery) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(d.Body.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return payload
}

func TestPatchDigest_CRLFAndLF_HaveTheSameDigest(t *testing.T) {
	lf := "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n"
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	cr := strings.ReplaceAll(lf, "\n", "\r")
	if PatchDigest(lf) != PatchDigest(crlf) || PatchDigest(lf) != PatchDigest(cr) {
		t.Fatalf("digests differ: LF %s CRLF %s CR %s", PatchDigest(lf), PatchDigest(crlf), PatchDigest(cr))
	}
	// sha256 of "a\n".
	if got := PatchDigest("a\r\n"); got != "sha256:87428fc522803d31065e7bce3cf03fe475096631e5e07bbd7a0fde60c4cf25c7" {
		t.Fatalf("PatchDigest(a CRLF) = %s", got)
	}
	if PatchDigest(lf) == PatchDigest(lf+"\n") {
		t.Fatal("different patches share a digest")
	}
}

func TestServer_PassMode_DeliversSucceededCallbackOnceWithToken(t *testing.T) {
	rcv := newReceiver(t)
	r := newRunner(t, NewMockBackend(), false)

	testID := dispatch(t, r, rcv.url(), "")
	got := rcv.next(t)
	if got.Token != testToken {
		t.Fatalf("callback token header = %q, want the dispatched token", got.Token)
	}
	if got.Body.ExternalTaskID != testID {
		t.Fatalf("externalTaskId = %q, want %q", got.Body.ExternalTaskID, testID)
	}
	patchDigest := PatchDigest(testPatch)
	want := map[string]any{
		"status": "SUCCEEDED", "passed": true, "baseCommit": "fixture-v1",
		"patchDigest":  patchDigest,
		"resultDigest": ResultDigest("fixture-v1", patchDigest, true, 12, 0),
	}
	payload := payloadOf(t, got)
	for key, value := range want {
		if payload[key] != value {
			t.Fatalf("payload[%q] = %v, want %v", key, payload[key], value)
		}
	}
	if summary, _ := payload["summary"].(map[string]any); summary["total"] != float64(12) || summary["failed"] != float64(0) {
		t.Fatalf("summary = %v, want total 12 failed 0", payload["summary"])
	}

	r.shutdown(t)
	if extra := rcv.drained(); len(extra) != 0 {
		t.Fatalf("pass mode delivered %d extra callbacks", len(extra))
	}
}

func TestServer_FailMode_DeliversFailingVerdict(t *testing.T) {
	rcv := newReceiver(t)
	r := newRunner(t, NewMockBackend(), false)

	dispatch(t, r, rcv.url(), mockFail)
	payload := payloadOf(t, rcv.next(t))
	summary, _ := payload["summary"].(map[string]any)
	if payload["status"] != "SUCCEEDED" || payload["passed"] != false || summary["failed"].(float64) <= 0 {
		t.Fatalf("payload = %v, want SUCCEEDED, passed false and failed > 0", payload)
	}
	if payload["resultDigest"] == ResultDigest("fixture-v1", PatchDigest(testPatch), true, 12, 0) {
		t.Fatal("a failing result has the passing result's digest")
	}
}

func TestServer_ErrorMode_DeliversFailedCallbackWithCode(t *testing.T) {
	rcv := newReceiver(t)
	r := newRunner(t, NewMockBackend(), false)

	dispatch(t, r, rcv.url(), mockError)
	payload := payloadOf(t, rcv.next(t))
	failure, _ := payload["error"].(map[string]any)
	if payload["status"] != "FAILED" || failure["code"] != mockErrorCode || len(payload) != 2 {
		t.Fatalf("payload = %v, want exactly status FAILED and error with code %s", payload, mockErrorCode)
	}
}

func TestServer_DuplicateMode_DeliversTheSameCallbackTwice(t *testing.T) {
	rcv := newReceiver(t)
	r := newRunner(t, NewMockBackend(), false)

	testID := dispatch(t, r, rcv.url(), mockDuplicate)
	first, second := rcv.next(t), rcv.next(t)
	if first.Body.ExternalTaskID != testID || second.Body.ExternalTaskID != testID {
		t.Fatalf("deliveries for %q and %q, want both for %q", first.Body.ExternalTaskID, second.Body.ExternalTaskID, testID)
	}
	if !bytes.Equal(first.Body.Payload, second.Body.Payload) || second.Token != testToken {
		t.Fatal("the duplicate delivery differs from the first")
	}
	r.shutdown(t)
	if extra := rcv.drained(); len(extra) != 0 {
		t.Fatalf("duplicate mode delivered %d callbacks beyond two", len(extra))
	}
}

func TestServer_LostMode_DeliversNothingAndRecordsLost(t *testing.T) {
	rcv := newReceiver(t)
	r := newRunner(t, NewMockBackend(), true)

	testID := dispatch(t, r, rcv.url(), mockLost)
	r.shutdown(t)
	if got := rcv.drained(); len(got) != 0 {
		t.Fatalf("lost mode delivered %d callbacks, want none", len(got))
	}
	if !hasLine(r.recordLines(t), eventLost, kindCallback, testID) {
		t.Fatal("record has no lost line for the test")
	}
	stored, err := readRedeliveryEntries(RedeliveryPath(r.recordPath))
	if err != nil || len(stored) != 0 {
		t.Fatalf("redelivery store = %d entries (%v), want none for a lost result", len(stored), err)
	}
}

func TestServer_GetStatus_ReportsTheFinishedResult(t *testing.T) {
	rcv := newReceiver(t)
	r := newRunner(t, NewMockBackend(), false)

	testID := dispatch(t, r, rcv.url(), "")
	delivered := rcv.next(t)

	resp, err := http.Get(r.http.URL + testsPath + "/" + testID)
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	defer resp.Body.Close()
	var status statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.TestID != testID || status.Status != statusSucceeded || !bytes.Equal(status.Result, delivered.Body.Payload) {
		t.Fatalf("status = %+v, want SUCCEEDED with the delivered payload", status)
	}

	missing, err := http.Get(r.http.URL + testsPath + "/test_unknown")
	if err != nil {
		t.Fatalf("get unknown status: %v", err)
	}
	_ = missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown testId answered %d, want 404", missing.StatusCode)
	}
}

// countingBackend counts runs so a test can prove a rejected dispatch ran nothing.
type countingBackend struct {
	MockBackend
	mu   sync.Mutex
	runs int
}

func (c *countingBackend) Run(ctx context.Context, job Job) Outcome {
	c.mu.Lock()
	c.runs++
	c.mu.Unlock()
	return c.MockBackend.Run(ctx, job)
}

func TestServer_InvalidRequest_RejectedWithoutRunning(t *testing.T) {
	rcv := newReceiver(t)
	backend := &countingBackend{}
	r := newRunner(t, backend, true)

	withImage := request(rcv.url(), "")
	withImage["image"] = "attacker/image:latest"
	withMounts := request(rcv.url(), "")
	withMounts["mounts"] = []string{"/:/host"}
	withArgs := request(rcv.url(), "")
	withArgs["args"] = []string{"--privileged"}
	noToken := request(rcv.url(), "")
	delete(noToken, "callbackToken")
	noPatch := request(rcv.url(), "")
	delete(noPatch, "patch")
	relativeURL := request("/api/callbacks", "")
	unknownMock := request(rcv.url(), "explode")
	hugePatch := request(rcv.url(), "")
	hugePatch["patch"] = strings.Repeat("x", maxPatchBytes+1)

	for name, body := range map[string]map[string]any{
		"image": withImage, "mounts": withMounts, "args": withArgs, "no token": noToken,
		"no patch": noPatch, "relative callback": relativeURL, "unknown mock": unknownMock, "huge patch": hugePatch,
	} {
		status, response := r.post(t, body)
		if status != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", name, status)
		}
		if message, _ := response["error"].(string); strings.Contains(message, testToken) || strings.Contains(message, "attacker") {
			t.Fatalf("%s: error %q echoes the request", name, message)
		}
	}
	r.shutdown(t)
	if backend.runs != 0 || len(rcv.drained()) != 0 {
		t.Fatalf("rejected dispatches ran %d tests, want none", backend.runs)
	}
}

func TestServer_DockerBackend_RejectsMockMode(t *testing.T) {
	r := newRunner(t, NewDockerBackend(DefaultImage, time.Second, &fakeDocker{}), false)
	if status, _ := r.post(t, request("http://backend.invalid/api/callbacks", mockPass)); status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a mock mode on the docker backend", status)
	}
}

func hasLine(lines []map[string]any, event, kind, testID string) bool {
	for _, line := range lines {
		if line["event"] == event && line["kind"] == kind && line["testId"] == testID {
			return true
		}
	}
	return false
}

func TestServer_BarrierOnTest_HoldsDispatchBeforeAccepting(t *testing.T) {
	rcv := newReceiver(t)
	backend := &countingBackend{}
	r := newRunner(t, backend, true)
	r.control(t, http.MethodPost, "/pause", `{"kinds":["test"]}`)

	answered := make(chan int, 1)
	go func() {
		status, _ := r.post(t, request(rcv.url(), ""))
		answered <- status
	}()
	if kind := <-r.server.controls.heldHook; kind != kindTest {
		t.Fatalf("held kind = %q, want %q", kind, kindTest)
	}
	select {
	case status := <-answered:
		t.Fatalf("held dispatch answered %d before release", status)
	default:
	}
	backend.mu.Lock()
	runs := backend.runs
	backend.mu.Unlock()
	if runs != 0 {
		t.Fatal("a held dispatch started a test")
	}

	r.control(t, http.MethodPost, "/release", "")
	if status := <-answered; status != http.StatusAccepted {
		t.Fatalf("released dispatch answered %d, want 202", status)
	}
	rcv.next(t)
}

func TestServer_BarrierOnCallback_HoldsDeliveryUntilRelease(t *testing.T) {
	rcv := newReceiver(t)
	r := newRunner(t, NewMockBackend(), true)
	r.control(t, http.MethodPost, "/pause", `{"kinds":["callback"]}`)

	testID := dispatch(t, r, rcv.url(), "")
	if kind := <-r.server.controls.heldHook; kind != kindCallback {
		t.Fatalf("held kind = %q, want %q", kind, kindCallback)
	}
	select {
	case d := <-rcv.got:
		t.Fatalf("callback for %s delivered while held", d.Body.ExternalTaskID)
	default:
	}
	r.control(t, http.MethodPost, "/release", "")
	if got := rcv.next(t); got.Body.ExternalTaskID != testID {
		t.Fatalf("released callback for %q, want %q", got.Body.ExternalTaskID, testID)
	}
}

func TestServer_Record_ListsEveryStepWithoutTheToken(t *testing.T) {
	rcv := newReceiver(t)
	r := newRunner(t, NewMockBackend(), true)

	testID := dispatch(t, r, rcv.url(), "")
	rcv.next(t)
	r.shutdown(t)

	lines := r.recordLines(t)
	var events []string
	for _, line := range lines {
		events = append(events, line["kind"].(string)+"/"+line["event"].(string))
	}
	want := []string{"test/arrived", "test/responded", "test/started", "test/completed", "callback/arrived", "callback/callback"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("record events = %v, want %v", events, want)
	}
	patchDigest := PatchDigest(testPatch)
	arrived, completed, callback := lines[0], lines[3], lines[5]
	if arrived["testId"] != testID || arrived["patchDigest"] != patchDigest || arrived["callbackTarget"] != rcv.server.URL+"/api/callbacks" {
		t.Fatalf("arrived line = %v", arrived)
	}
	if completed["passed"] != true || completed["patchDigest"] != patchDigest || completed["resultDigest"] != ResultDigest("fixture-v1", patchDigest, true, 12, 0) {
		t.Fatalf("completed line = %v", completed)
	}
	if callback["status"] != "SUCCEEDED" || callback["attempt"] != float64(1) || callback["httpStatus"] != float64(http.StatusOK) {
		t.Fatalf("callback line = %v", callback)
	}

	raw, err := os.ReadFile(r.recordPath)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	for _, secret := range []string{testToken, "secret-query", "+new"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("record contains %q", secret)
		}
	}
	info, err := os.Stat(RedeliveryPath(r.recordPath))
	if err != nil {
		t.Fatalf("stat redelivery store: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("redelivery store mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestServer_Restart_RedeliversEachStoredResultOnce(t *testing.T) {
	rcv := newReceiver(t)
	first := newRunner(t, NewMockBackend(), true)
	testID := dispatch(t, first, rcv.url(), mockDuplicate)
	original := rcv.next(t)
	rcv.next(t)
	first.shutdown(t)

	// A second process opens the same record and store.
	record, err := mockcontrol.OpenRecord(first.recordPath)
	if err != nil {
		t.Fatalf("reopen record: %v", err)
	}
	t.Cleanup(func() { _ = record.Close() })
	store, stored, err := OpenRedeliveryStore(RedeliveryPath(first.recordPath))
	if err != nil {
		t.Fatalf("reopen redelivery store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if len(stored) != 1 {
		t.Fatalf("stored results = %d, want 1", len(stored))
	}
	restarted := NewServer(NewMockBackend(), WithTestControls(record, store))
	restarted.Redeliver(stored)
	redelivered := rcv.next(t)
	if err := restarted.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if extra := rcv.drained(); len(extra) != 0 {
		t.Fatalf("restart redelivered %d extra callbacks", len(extra))
	}
	if redelivered.Token != testToken || redelivered.Body.ExternalTaskID != testID || !bytes.Equal(redelivered.Body.Payload, original.Body.Payload) {
		t.Fatal("the redelivered callback differs from the original delivery")
	}

	server := httptest.NewServer(restarted)
	defer server.Close()
	reader := &runner{server: restarted, http: server}
	lines := reader.recordLines(t)
	if !hasLine(lines, eventRedelivered, kindCallback, testID) {
		t.Fatal("record has no redelivered line")
	}
	last := lines[len(lines)-1]
	if last["event"] != eventCallback || last["testId"] != testID || last["attempt"] != float64(3) {
		t.Fatalf("last record line = %v, want the third callback attempt", last)
	}
}

func TestOpenRedeliveryStore_TornLine_KeepsTheOtherResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record.jsonl.redelivery")
	content := `{"testId":"test_a","callbackUrl":"http://x/cb","callbackToken":"t","payload":{"status":"SUCCEEDED"},"deliveries":1}
{"testId":"test_b","callbackUrl":"http://x/cb","callbackToken":"t","payload":{"status":"FAILED"},"deliveries":1}
{"testId":"test_a","callbackUrl":"http://x/cb","callbackToken":"t","payload":{"status":"SUCCEEDED"},"deliveries":2}
{"testId":"test_c","callba`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write store: %v", err)
	}
	store, stored, err := OpenRedeliveryStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	if len(stored) != 2 || stored[0].entry.TestID != "test_a" || stored[0].entry.Deliveries != 2 || stored[1].entry.TestID != "test_b" {
		t.Fatalf("stored = %+v, want test_a (latest entry) then test_b", stored)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("store mode = %v (%v), want 0600", info.Mode().Perm(), err)
	}
}

// blockingBackend blocks every run until its context ends.
type blockingBackend struct {
	MockBackend
	started chan struct{}
}

func (b blockingBackend) Run(ctx context.Context, _ Job) Outcome {
	b.started <- struct{}{}
	<-ctx.Done()
	return Outcome{FailureCode: codeSandboxError}
}

func TestServer_ShutdownDeadline_CancelsRunningTestsAndDeliversNothing(t *testing.T) {
	rcv := newReceiver(t)
	backend := blockingBackend{started: make(chan struct{}, 1)}
	r := newRunner(t, backend, false)

	dispatch(t, r, rcv.url(), "")
	<-backend.started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.server.Shutdown(ctx); err == nil {
		t.Fatal("shutdown with an expired context reported no error")
	}
	if got := rcv.drained(); len(got) != 0 {
		t.Fatalf("a cancelled test delivered %d callbacks", len(got))
	}
	if status, _ := r.post(t, request(rcv.url(), "")); status != http.StatusServiceUnavailable {
		t.Fatalf("dispatch after shutdown answered %d, want 503", status)
	}
}
