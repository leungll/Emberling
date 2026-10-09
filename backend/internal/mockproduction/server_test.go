package mockproduction

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// recordLine is one record line as a test decodes it.
type recordLine struct {
	Seq         int64  `json:"seq"`
	Event       string `json:"event"`
	Kind        string `json:"kind"`
	OperationID string `json:"operationId"`
	ApprovalID  string `json:"approvalId"`
	Service     string `json:"service"`
	Environment string `json:"environment"`
	BaseCommit  string `json:"baseCommit"`
	PatchDigest string `json:"patchDigest"`
	Version     string `json:"version"`
	Status      int    `json:"status"`
	Replayed    *bool  `json:"replayed"`
}

// production is a Server with test controls served in-process.
type production struct {
	srv        *httptest.Server
	server     *Server
	record     *Record
	recordPath string
	held       chan string
}

func newProduction(t *testing.T) *production {
	t.Helper()
	recordPath := filepath.Join(t.TempDir(), "record.jsonl")
	record, err := OpenRecord(recordPath)
	if err != nil {
		t.Fatalf("OpenRecord() error = %v", err)
	}
	held := make(chan string, 8)
	server := NewServer(WithTestControls(record), WithHeldNotify(held))
	server.now = func() time.Time { return fixedNow }
	p := &production{srv: httptest.NewServer(server), server: server, record: record, recordPath: recordPath, held: held}
	t.Cleanup(func() {
		// StopHolding first, so a request a failed test left held cannot keep Close waiting.
		server.StopHolding()
		p.srv.Close()
		_ = record.Close()
	})
	return p
}

func deployBody(operationID, service, environment string, parameters string) string {
	body := `{"operationId":"` + operationID + `","approvalId":"apr_1","target":{"service":"` + service +
		`","environment":"` + environment + `"},"baseCommit":"abc123","patchDigest":"sha256:feed"`
	if parameters != "" {
		body += `,"parameters":` + parameters
	}
	return body + "}"
}

func post(t *testing.T, url, body string) (int, []byte) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s error = %v", url, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body error = %v", err)
	}
	return resp.StatusCode, out
}

func get(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s error = %v", url, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body error = %v", err)
	}
	return resp.StatusCode, out
}

func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return out
}

func (p *production) lines(t *testing.T) []recordLine {
	t.Helper()
	status, body := get(t, p.srv.URL+"/control/record")
	if status != http.StatusOK {
		t.Fatalf("GET /control/record status = %d", status)
	}
	return parseLines(t, body)
}

func parseLines(t *testing.T, body []byte) []recordLine {
	t.Helper()
	var out []recordLine
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		out = append(out, decode[recordLine](t, scanner.Bytes()))
	}
	return out
}

func events(lines []recordLine) []string {
	var out []string
	for _, line := range lines {
		out = append(out, line.Event)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDeploy_NewOperation_DeploysAndIsQueryable(t *testing.T) {
	p := newProduction(t)

	status, body := post(t, p.srv.URL+"/v1/deployments", deployBody("op_1", "checkout", "prod", ""))
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %s; want 200", status, body)
	}
	got := decode[deployResponse](t, body)
	want := deployResponse{OperationID: "op_1", Version: "v1", DeployedAt: "2026-03-04T05:06:07Z"}
	if got != want {
		t.Errorf("response = %+v, want %+v", got, want)
	}

	status, body = get(t, p.srv.URL+"/v1/deployments/op_1")
	if status != http.StatusOK {
		t.Fatalf("GET deployment status = %d", status)
	}
	stored := decode[deployment](t, body)
	wantStored := deployment{
		OperationID: "op_1", ApprovalID: "apr_1", Target: target{Service: "checkout", Environment: "prod"},
		BaseCommit: "abc123", PatchDigest: "sha256:feed", Version: "v1", DeployedAt: "2026-03-04T05:06:07Z",
	}
	if stored != wantStored {
		t.Errorf("deployment = %+v, want %+v", stored, wantStored)
	}

	lines := p.lines(t)
	if want := []string{"arrived", "deployed", "responded"}; !equalStrings(events(lines), want) {
		t.Fatalf("record events = %v, want %v", events(lines), want)
	}
	deployed := lines[1]
	if deployed.OperationID != "op_1" || deployed.ApprovalID != "apr_1" || deployed.Service != "checkout" ||
		deployed.Environment != "prod" || deployed.BaseCommit != "abc123" || deployed.PatchDigest != "sha256:feed" ||
		deployed.Version != "v1" || deployed.Replayed == nil || *deployed.Replayed {
		t.Errorf("deployed line = %+v, want every field with replayed=false", deployed)
	}
	if lines[2].Status != http.StatusOK {
		t.Errorf("responded status = %d, want 200", lines[2].Status)
	}
}

func TestDeploy_SameOperationID_ReplaysStoredResultWithoutNewVersion(t *testing.T) {
	p := newProduction(t)
	_, first := post(t, p.srv.URL+"/v1/deployments", deployBody("op_1", "checkout", "prod", ""))

	// A replay answers with the stored result even when its body differs.
	status, second := post(t, p.srv.URL+"/v1/deployments", deployBody("op_1", "checkout", "prod", `{"mock":"reject"}`))
	if status != http.StatusOK || !bytes.Equal(first, second) {
		t.Fatalf("replay = %d %s, want 200 with %s", status, second, first)
	}
	_, third := post(t, p.srv.URL+"/v1/deployments", deployBody("op_2", "checkout", "prod", ""))
	if got := decode[deployResponse](t, third).Version; got != "v2" {
		t.Errorf("next operation version = %q, want v2", got)
	}

	var deployed []recordLine
	for _, line := range p.lines(t) {
		if line.Event == recordDeployed {
			deployed = append(deployed, line)
		}
	}
	if len(deployed) != 3 {
		t.Fatalf("deployed lines = %d, want 3", len(deployed))
	}
	for i, want := range []struct {
		op       string
		version  string
		replayed bool
	}{{"op_1", "v1", false}, {"op_1", "v1", true}, {"op_2", "v2", false}} {
		line := deployed[i]
		if line.OperationID != want.op || line.Version != want.version || line.Replayed == nil || *line.Replayed != want.replayed {
			t.Errorf("deployed[%d] = %+v, want %+v", i, line, want)
		}
	}
}

func TestDeploy_VersionsArePerTarget(t *testing.T) {
	p := newProduction(t)
	post(t, p.srv.URL+"/v1/deployments", deployBody("op_1", "checkout", "prod", ""))
	_, body := post(t, p.srv.URL+"/v1/deployments", deployBody("op_2", "checkout", "staging", ""))
	if got := decode[deployResponse](t, body).Version; got != "v1" {
		t.Errorf("first deployment to another target = %q, want v1", got)
	}
}

func TestDeploy_MockReject_Answers409AndChangesNothing(t *testing.T) {
	p := newProduction(t)

	status, body := post(t, p.srv.URL+"/v1/deployments", deployBody("op_1", "checkout", "prod", `{"mock":"reject","replicas":3}`))
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", status)
	}
	if got := decode[errorResponse](t, body); got.Code != codeRejected || got.Message == "" {
		t.Errorf("body = %+v, want code REJECTED with a message", got)
	}
	if status, _ := get(t, p.srv.URL+"/v1/deployments/op_1"); status != http.StatusNotFound {
		t.Errorf("GET rejected deployment = %d, want 404", status)
	}
	if status, _ := get(t, p.srv.URL+"/v1/targets/checkout/prod"); status != http.StatusNotFound {
		t.Errorf("GET target after rejection = %d, want 404", status)
	}
	lines := p.lines(t)
	if want := []string{"arrived", "responded"}; !equalStrings(events(lines), want) {
		t.Fatalf("record events = %v, want %v", events(lines), want)
	}
	if lines[1].Status != http.StatusConflict {
		t.Errorf("responded status = %d, want 409", lines[1].Status)
	}
}

func TestDeploy_InvalidRequest_Answers400AndRecordsArrival(t *testing.T) {
	cases := map[string]string{
		"not json":            `{`,
		"unknown field":       `{"operationId":"op_1","target":{"service":"s","environment":"e"},"baseCommit":"c","patchDigest":"d","extra":1}`,
		"missing operationId": `{"target":{"service":"s","environment":"e"},"baseCommit":"c","patchDigest":"d"}`,
		"bad operationId":     `{"operationId":"op 1","target":{"service":"s","environment":"e"},"baseCommit":"c","patchDigest":"d"}`,
		"missing target":      `{"operationId":"op_1","baseCommit":"c","patchDigest":"d"}`,
		"missing service":     `{"operationId":"op_1","target":{"environment":"e"},"baseCommit":"c","patchDigest":"d"}`,
		"bad environment":     `{"operationId":"op_1","target":{"service":"s","environment":"../e"},"baseCommit":"c","patchDigest":"d"}`,
		"missing baseCommit":  `{"operationId":"op_1","target":{"service":"s","environment":"e"},"patchDigest":"d"}`,
		"missing patchDigest": `{"operationId":"op_1","target":{"service":"s","environment":"e"},"baseCommit":"c"}`,
		"array parameters":    `{"operationId":"op_1","target":{"service":"s","environment":"e"},"baseCommit":"c","patchDigest":"d","parameters":[]}`,
		"unknown mock":        `{"operationId":"op_1","target":{"service":"s","environment":"e"},"baseCommit":"c","patchDigest":"d","parameters":{"mock":"lost"}}`,
		"trailing data":       `{"operationId":"op_1","target":{"service":"s","environment":"e"},"baseCommit":"c","patchDigest":"d"} {}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			p := newProduction(t)
			status, out := post(t, p.srv.URL+"/v1/deployments", body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", status)
			}
			if got := decode[errorResponse](t, out); got.Code != codeInvalidRequest {
				t.Errorf("code = %q, want %s", got.Code, codeInvalidRequest)
			}
			lines := p.lines(t)
			if want := []string{"arrived", "responded"}; !equalStrings(events(lines), want) || lines[1].Status != http.StatusBadRequest {
				t.Fatalf("record = %+v, want arrived then responded 400", lines)
			}
		})
	}
}

func TestDeploy_NullParameters_AreAccepted(t *testing.T) {
	p := newProduction(t)
	if status, body := post(t, p.srv.URL+"/v1/deployments", deployBody("op_1", "s", "e", "null")); status != http.StatusOK {
		t.Fatalf("status = %d, body %s; want 200", status, body)
	}
}

func TestTarget_ReportsLatestDeployment(t *testing.T) {
	p := newProduction(t)
	if status, body := get(t, p.srv.URL+"/v1/targets/checkout/prod"); status != http.StatusNotFound ||
		decode[errorResponse](t, body).Code != codeNotFound {
		t.Fatalf("unknown target = %d %s, want 404 NOT_FOUND", status, body)
	}
	post(t, p.srv.URL+"/v1/deployments", deployBody("op_1", "checkout", "prod", ""))
	post(t, p.srv.URL+"/v1/deployments", strings.Replace(deployBody("op_2", "checkout", "prod", ""), "sha256:feed", "sha256:beef", 1))

	status, body := get(t, p.srv.URL+"/v1/targets/checkout/prod")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	want := targetResponse{Version: "v2", PatchDigest: "sha256:beef", LastOperationID: "op_2"}
	if got := decode[targetResponse](t, body); got != want {
		t.Errorf("target = %+v, want %+v", got, want)
	}
	if status, _ := get(t, p.srv.URL+"/v1/deployments/op_unknown"); status != http.StatusNotFound {
		t.Errorf("unknown deployment = %d, want 404", status)
	}
}

func TestBarrier_PausedDeploy_HeldUntilReleaseThenDeploys(t *testing.T) {
	p := newProduction(t)
	if status, body := post(t, p.srv.URL+"/control/pause", `{"kinds":["deploy"]}`); status != http.StatusOK {
		t.Fatalf("pause = %d %s", status, body)
	}
	statuses := make(chan int, 1)
	go func() {
		resp, err := http.Post(p.srv.URL+"/v1/deployments", "application/json", strings.NewReader(deployBody("op_held", "s", "e", "")))
		if err != nil {
			statuses <- 0
			return
		}
		_ = resp.Body.Close()
		statuses <- resp.StatusCode
	}()
	waitHeld(t, p.held, "op_held")

	if got := events(p.lines(t)); !equalStrings(got, []string{"arrived"}) {
		t.Fatalf("record while held = %v, want [arrived]", got)
	}
	if status, _ := get(t, p.srv.URL+"/v1/targets/s/e"); status != http.StatusNotFound {
		t.Fatalf("target while held = %d, want 404", status)
	}
	if status, _ := post(t, p.srv.URL+"/control/release", ""); status != http.StatusOK {
		t.Fatalf("release = %d", status)
	}
	select {
	case status := <-statuses:
		if status != http.StatusOK {
			t.Fatalf("status after release = %d, want 200", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no response after release")
	}
	if got, want := events(p.lines(t)), []string{"arrived", "released", "deployed", "responded"}; !equalStrings(got, want) {
		t.Fatalf("record = %v, want %v", got, want)
	}
}

func TestBarrier_CallerDisconnectsWhileHeld_RecordsAbandonedAndNeverDeploys(t *testing.T) {
	p := newProduction(t)
	if status, _ := post(t, p.srv.URL+"/control/pause", `{"kinds":["deploy"]}`); status != http.StatusOK {
		t.Fatal("pause failed")
	}

	// The handler is served directly so the caller's disconnect is the request context
	// ending, and the test can wait for the handler to return instead of polling.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/deployments", strings.NewReader(deployBody("op_gone", "s", "e", "")))
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.server.ServeHTTP(recorder, request)
	}()
	waitHeld(t, p.held, "op_gone")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return after the caller left")
	}

	// A later release finds nothing to let through.
	if status, body := post(t, p.srv.URL+"/control/release", ""); status != http.StatusOK || decode[map[string]any](t, body)["released"] != nil {
		t.Fatalf("release = %d %s, want nothing released", status, body)
	}
	lines := p.lines(t)
	if got, want := events(lines), []string{"arrived", "abandoned"}; !equalStrings(got, want) {
		t.Fatalf("record = %v, want %v", got, want)
	}
	if lines[1].OperationID != "op_gone" {
		t.Errorf("abandoned line = %+v, want operationId op_gone", lines[1])
	}
	if status, _ := get(t, p.srv.URL+"/v1/deployments/op_gone"); status != http.StatusNotFound {
		t.Errorf("abandoned deployment = %d, want 404", status)
	}
}

func TestBarrier_StopHolding_RejectsHeldDeployWith503(t *testing.T) {
	p := newProduction(t)
	post(t, p.srv.URL+"/control/pause", "")
	statuses := make(chan int, 1)
	go func() {
		resp, err := http.Post(p.srv.URL+"/v1/deployments", "application/json", strings.NewReader(deployBody("op_stop", "s", "e", "")))
		if err != nil {
			statuses <- 0
			return
		}
		_ = resp.Body.Close()
		statuses <- resp.StatusCode
	}()
	waitHeld(t, p.held, "op_stop")
	p.server.StopHolding()
	select {
	case status := <-statuses:
		if status != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no response after StopHolding")
	}
	if got, want := events(p.lines(t)), []string{"arrived", "rejected", "responded"}; !equalStrings(got, want) {
		t.Fatalf("record = %v, want %v", got, want)
	}
}

func TestRecord_FileLines_HoldIdentifiersButNeverParameters(t *testing.T) {
	p := newProduction(t)
	post(t, p.srv.URL+"/v1/deployments", deployBody("op_1", "checkout", "prod", `{"dbPassword":"hunter2"}`))

	raw, err := os.ReadFile(p.recordPath)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	if bytes.Contains(raw, []byte("hunter2")) || bytes.Contains(raw, []byte("dbPassword")) {
		t.Fatalf("record file carries parameters: %s", raw)
	}
	lines := parseLines(t, raw)
	if got, want := events(lines), []string{"arrived", "deployed", "responded"}; !equalStrings(got, want) {
		t.Fatalf("record file events = %v, want %v", got, want)
	}
	for i, line := range lines {
		if line.Seq != int64(i+1) || line.Kind != kindDeploy || line.OperationID != "op_1" {
			t.Errorf("line %d = %+v, want seq %d, kind deploy, operationId op_1", i, line, i+1)
		}
	}
	if arrived := lines[0]; arrived.ApprovalID != "apr_1" || arrived.Service != "checkout" || arrived.Environment != "prod" {
		t.Errorf("arrived line = %+v, want approvalId and target", arrived)
	}
}

func TestServer_ControlsDisabled_NoControlRoutesAndDeploysStillWork(t *testing.T) {
	srv := httptest.NewServer(NewServer())
	defer srv.Close()
	if status, _ := get(t, srv.URL+"/control/record"); status != http.StatusNotFound {
		t.Errorf("GET /control/record = %d, want 404", status)
	}
	if status, _ := post(t, srv.URL+"/control/pause", ""); status != http.StatusNotFound {
		t.Errorf("POST /control/pause = %d, want 404", status)
	}
	if status, body := post(t, srv.URL+"/v1/deployments", deployBody("op_1", "s", "e", "")); status != http.StatusOK {
		t.Errorf("deploy = %d %s, want 200", status, body)
	}
}

func waitHeld(t *testing.T, held <-chan string, want string) {
	t.Helper()
	select {
	case got := <-held:
		if got != want {
			t.Fatalf("held operationId = %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s to be held", want)
	}
}
