package verifypatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const (
	testCallbackToken  = "plaintext-callback-token-never-logged"
	testCallbackURL    = "http://backend.invalid/api/callbacks"
	testPatch          = "--- a/go.mod\n+++ b/go.mod\n@@ -5 +5 @@\n-require github.com/golang-jwt/jwt v3.2.2+incompatible\n+require github.com/golang-jwt/jwt/v5 v5.3.1\n"
	testBaseCommit     = "2706ca6fb4ddbe969e9acfa5464a347ff90c7774"
	testVerificationID = "verify_0123456789abcdef"
)

// memoryArtifacts is an in-memory ArtifactTextStore that keeps every text it was handed.
type memoryArtifacts struct {
	mu     sync.Mutex
	texts  []string
	failed error
}

func (m *memoryArtifacts) WriteText(_ context.Context, r io.Reader) (domain.ArtifactRef, error) {
	if m.failed != nil {
		return domain.ArtifactRef{}, m.failed
	}
	content, err := io.ReadAll(r)
	if err != nil {
		return domain.ArtifactRef{}, err
	}
	m.mu.Lock()
	m.texts = append(m.texts, string(content))
	m.mu.Unlock()
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	id, _ := domain.ArtifactIDForSHA256(digest)
	return domain.ArtifactRef{ArtifactID: id, MediaType: domain.ArtifactMediaTypeTextPlain, SizeBytes: int64(len(content)), SHA256: digest}, nil
}

func workspace() *registry.WorkspaceView {
	return &registry.WorkspaceView{RepositoryID: "vuln-demo", BaseCommit: testBaseCommit, Patch: testPatch, PatchDigest: domain.WorkspacePatchDigest(testPatch), PatchSeq: 1}
}

func action(patchDigest string) registry.ToolAction {
	encoded, _ := json.Marshal(map[string]string{"patchDigest": patchDigest, "mock": "pass"})
	return registry.ToolAction{
		AgentRunID: "ar_1", TurnID: "turn_1", ActionID: "act_1", ToolName: ToolName, AttemptNo: 1,
		Arguments: encoded,
		Callback:  &registry.CallbackContext{URL: testCallbackURL, Token: testCallbackToken},
		Workspace: workspace(),
	}
}

func asyncState() registry.ToolAsyncState {
	return registry.ToolAsyncState{AgentRunID: "ar_1", ActionID: "act_1", ToolName: ToolName, AttemptNo: 1,
		ExternalTask: registry.ExternalTask{ProviderID: ProviderID, ExternalTaskID: testVerificationID}}
}

// runnerStub answers the verification routes with fixed responses and keeps each request.
type runnerStub struct {
	t          *testing.T
	postStatus int
	postBody   string
	getStatus  int
	getBody    string
	requests   chan map[string]any
}

func newRunnerStub(t *testing.T) (*runnerStub, *httptest.Server) {
	t.Helper()
	stub := &runnerStub{t: t, postStatus: http.StatusAccepted, postBody: `{"verificationId":"` + testVerificationID + `"}`, getStatus: http.StatusNotFound, getBody: `{"error":"unknown verificationId"}`, requests: make(chan map[string]any, 4)}
	server := httptest.NewServer(stub)
	t.Cleanup(server.Close)
	return stub, server
}

func (s *runnerStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == verificationsPath:
		var decoded map[string]any
		if err := json.NewDecoder(r.Body).Decode(&decoded); err != nil {
			s.t.Errorf("decode dispatch body: %v", err)
		}
		s.requests <- decoded
		w.WriteHeader(s.postStatus)
		_, _ = io.WriteString(w, s.postBody)
	case r.Method == http.MethodGet && r.URL.Path == verificationsPath+"/"+testVerificationID:
		w.WriteHeader(s.getStatus)
		_, _ = io.WriteString(w, s.getBody)
	default:
		http.NotFound(w, r)
	}
}

func succeededPayload(log string) map[string]any {
	return map[string]any{
		"status": "SUCCEEDED", "passed": true, "testsPassed": true, "scanClean": true, "dependenciesClean": true,
		"baseCommit": testBaseCommit, "patchDigest": domain.WorkspacePatchDigest(testPatch), "resultDigest": "sha256:abc",
		"summary": map[string]any{
			"tests": map[string]int{"total": 9, "failed": 0}, "scanRan": true, "scanner": "govulncheck",
			"scanFindings": []string{}, "vulnerableDependencies": []string{}, "snapshotDigest": "sha256:snap",
		},
		"log": log, "logTruncated": false,
	}
}

func TestRegistration_DeclaresTheVerificationContract(t *testing.T) {
	reg := Registration("http://runner.invalid", nil, &memoryArtifacts{})
	m := reg.Metadata
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if m.ExecutionKind != domain.ToolExecutionAsync || m.SideEffect.Kind != domain.SideEffectExternal || m.SideEffect.Idempotency != domain.IdempotencySafe {
		t.Fatalf("metadata = %+v, want an ASYNC, EXTERNAL and SAFE Tool", m)
	}
	if m.Poll == nil || !m.UsesWorkspace || m.WorkspacePatchDigestPath != "/patchDigest" || m.WorkspaceWritePath != "" {
		t.Fatalf("metadata = %+v, want poll, workspace use and the patch digest pointer", m)
	}
	if m.Produces == nil || m.Produces.FactType != FactType || m.Produces.SubjectPointer.Pointer != "/patchDigest" || m.Produces.VerdictPointer == nil || m.Produces.VerdictPointer.Pointer != "/passed" {
		t.Fatalf("produces = %+v", m.Produces)
	}
	if _, ok := reg.Executor.(registry.PollableAsyncToolExecutor); !ok {
		t.Fatal("the executor is not pollable")
	}
}

func TestExecute_SendsTheWorkspacePatchAndReturnsTheExternalTask(t *testing.T) {
	stub, server := newRunnerStub(t)
	exec := New(server.URL, server.Client(), &memoryArtifacts{})
	out, err := exec.Execute(context.Background(), action(domain.WorkspacePatchDigest(testPatch)))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Kind != registry.ToolResultDispatched || out.ExternalTask == nil || out.ExternalTask.ProviderID != ProviderID || out.ExternalTask.ExternalTaskID != testVerificationID {
		t.Fatalf("result = %+v", out)
	}
	sent := <-stub.requests
	for key, want := range map[string]any{
		"callbackUrl": testCallbackURL, "callbackToken": testCallbackToken, "repositoryId": "vuln-demo",
		"baseCommit": testBaseCommit, "patch": testPatch, "mock": "pass",
	} {
		if sent[key] != want {
			t.Fatalf("request[%q] = %v, want %v", key, sent[key], want)
		}
	}
	if len(sent) != 6 {
		t.Fatalf("request = %v, want exactly the six members", sent)
	}
}

// The patch the Agent names must be the workspace's current one; the Tool never sends
// another patch than the one it was asked about, even if the Runtime's own check were
// bypassed.
func TestExecute_DigestNotTheCurrentPatch_SendsNothing(t *testing.T) {
	stub, server := newRunnerStub(t)
	exec := New(server.URL, server.Client(), &memoryArtifacts{})
	_, err := exec.Execute(context.Background(), action(domain.WorkspacePatchDigest("older patch")))
	if err == nil || !strings.Contains(err.Error(), "not the workspace's current patch") {
		t.Fatalf("Execute error = %v, want a stale digest refusal", err)
	}
	if len(stub.requests) != 0 {
		t.Fatal("a request was sent for a stale digest")
	}
}

func TestExecute_RejectedDispatch_ErrorCarriesNoSecret(t *testing.T) {
	stub, server := newRunnerStub(t)
	stub.postStatus, stub.postBody = http.StatusBadRequest, `{"error":"echo `+testCallbackToken+`"}`
	exec := New(server.URL, server.Client(), &memoryArtifacts{})
	_, err := exec.Execute(context.Background(), action(domain.WorkspacePatchDigest(testPatch)))
	if err == nil || !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("Execute error = %v, want the rejection with its status", err)
	}
	for _, secret := range []string{testCallbackToken, testPatch, server.URL} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error %q carries %q", err, secret)
		}
	}
	a := action(domain.WorkspacePatchDigest(testPatch))
	a.Workspace = nil
	if _, err := exec.Execute(context.Background(), a); err == nil {
		t.Fatal("Execute without a workspace view succeeded")
	}
}

func TestOnCallback_Succeeded_StoresTheLogAndNamesItInTheResult(t *testing.T) {
	store := &memoryArtifacts{}
	exec := New("http://runner.invalid", nil, store)
	raw, _ := json.Marshal(succeededPayload("$ go test\nok\n"))
	got, err := exec.OnCallback(context.Background(), asyncState(), raw)
	if err != nil {
		t.Fatalf("OnCallback: %v", err)
	}
	if len(got.Artifacts) != 1 || got.Artifacts[0].MediaType != domain.ArtifactMediaTypeTextPlain {
		t.Fatalf("artifacts = %+v, want the text log", got.Artifacts)
	}
	var out map[string]any
	if err := json.Unmarshal(got.Output, &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if out["logArtifactId"] != got.Artifacts[0].ArtifactID || out["passed"] != true || out["testsPassed"] != true || out["patchDigest"] != domain.WorkspacePatchDigest(testPatch) {
		t.Fatalf("output = %v", out)
	}
	if _, hasLog := out["log"]; hasLog {
		t.Fatal("the result republishes the log text instead of the artifact reference")
	}
	if len(store.texts) != 1 || store.texts[0] != "$ go test\nok\n" {
		t.Fatalf("stored texts = %q", store.texts)
	}
	empty, _ := json.Marshal(succeededPayload(""))
	if _, err := exec.OnCallback(context.Background(), asyncState(), empty); err != nil || store.texts[1] != emptyLogText {
		t.Fatalf("empty log: err = %v, stored %q", err, store.texts[len(store.texts)-1])
	}
}

func TestOnCallback_Failed_IsAProviderFailureWithTheCode(t *testing.T) {
	exec := New("http://runner.invalid", nil, &memoryArtifacts{})
	raw := []byte(`{"status":"FAILED","error":{"code":"MODULE_UNAVAILABLE","message":"module github.com/golang-jwt/jwt/v4@v4.5.2 is not available in the offline module cache"}}`)
	_, err := exec.OnCallback(context.Background(), asyncState(), raw)
	var failure *registry.ProviderFailure
	if !errors.As(err, &failure) || failure.Err.Code != "MODULE_UNAVAILABLE" || !strings.Contains(failure.Err.Message, "jwt/v4@v4.5.2") {
		t.Fatalf("OnCallback error = %v, want a ProviderFailure naming the module", err)
	}
}

func TestOnCallback_IncompletePayload_IsNotInterpreted(t *testing.T) {
	store := &memoryArtifacts{}
	exec := New("http://runner.invalid", nil, store)
	body := succeededPayload("log")
	delete(body, "scanClean")
	raw, _ := json.Marshal(body)
	_, err := exec.OnCallback(context.Background(), asyncState(), raw)
	var failure *registry.ProviderFailure
	if err == nil || errors.As(err, &failure) || len(store.texts) != 0 {
		t.Fatalf("OnCallback error = %v with %d stored logs, want a plain error and nothing stored", err, len(store.texts))
	}
	if _, err := exec.OnCallback(context.Background(), asyncState(), []byte(`not json`)); err == nil {
		t.Fatal("garbage payload was interpreted")
	}
}

func TestOnCallback_LogStoreFails_ReturnsAnErrorWithoutTheLog(t *testing.T) {
	exec := New("http://runner.invalid", nil, &memoryArtifacts{failed: errors.New("disk full")})
	raw, _ := json.Marshal(succeededPayload("secret-ish log line"))
	_, err := exec.OnCallback(context.Background(), asyncState(), raw)
	if err == nil || strings.Contains(err.Error(), "secret-ish") {
		t.Fatalf("OnCallback error = %v, want an error that does not quote the log", err)
	}
}

func TestPoll_MapsRunnerStatuses(t *testing.T) {
	stub, server := newRunnerStub(t)
	exec := New(server.URL, server.Client(), &memoryArtifacts{})

	got, err := exec.Poll(context.Background(), asyncState())
	if err != nil || got.Status != registry.PollUnknownTask {
		t.Fatalf("404 poll = %+v, %v, want UNKNOWN_TASK", got, err)
	}
	stub.getStatus, stub.getBody = http.StatusOK, `{"verificationId":"`+testVerificationID+`","status":"RUNNING"}`
	if got, _ = exec.Poll(context.Background(), asyncState()); got.Status != registry.PollRunning {
		t.Fatalf("running poll = %+v, want RUNNING", got)
	}
	stub.getStatus = http.StatusInternalServerError
	if got, _ = exec.Poll(context.Background(), asyncState()); got.Status != registry.PollStatusUnknown {
		t.Fatalf("500 poll = %+v, want UNKNOWN", got)
	}
	result, _ := json.Marshal(succeededPayload("log"))
	stub.getStatus, stub.getBody = http.StatusOK, `{"verificationId":"`+testVerificationID+`","status":"SUCCEEDED","result":`+string(result)+`}`
	got, err = exec.Poll(context.Background(), asyncState())
	if err != nil || got.Status != registry.PollSucceeded || got.ToolResult == nil || len(got.ToolResult.Artifacts) != 1 {
		t.Fatalf("succeeded poll = %+v, %v, want SUCCEEDED with the result and its log artifact", got, err)
	}
	stub.getBody = `{"verificationId":"` + testVerificationID + `","status":"FAILED","result":{"status":"FAILED","error":{"code":"TIMEOUT","message":"verification exceeded 5m0s"}}}`
	got, err = exec.Poll(context.Background(), asyncState())
	if err != nil || got.Status != registry.PollFailed || got.Error == nil || got.Error.Code != "TIMEOUT" {
		t.Fatalf("failed poll = %+v, %v, want FAILED with the code", got, err)
	}
}
