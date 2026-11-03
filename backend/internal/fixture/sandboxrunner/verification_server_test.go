package sandboxrunner

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const verificationBaseCommit = "2706ca6fb4ddbe969e9acfa5464a347ff90c7774"

// newVerificationRunner serves tests on the mock Backend and verifications on the mock
// verification backend.
func newVerificationRunner(t *testing.T, controls bool) *runner {
	t.Helper()
	return newRunnerWith(t, NewMockBackend(), []Option{WithVerification(NewMockVerificationBackend())}, controls)
}

func verificationBody(callbackURL, mock string) map[string]any {
	body := map[string]any{
		"callbackUrl": callbackURL, "callbackToken": testToken, "repositoryId": "vuln-demo",
		"baseCommit": verificationBaseCommit, "patch": testPatch,
	}
	if mock != "" {
		body["mock"] = mock
	}
	return body
}

func (r *runner) postVerification(t *testing.T, body any) (int, map[string]any) {
	t.Helper()
	encoded, _ := json.Marshal(body)
	resp, err := http.Post(r.http.URL+verificationsPath, "application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("post verification: %v", err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded
}

func dispatchVerification(t *testing.T, r *runner, callbackURL, mock string) string {
	t.Helper()
	status, body := r.postVerification(t, verificationBody(callbackURL, mock))
	if status != http.StatusAccepted {
		t.Fatalf("dispatch status = %d (%v), want 202", status, body)
	}
	id, _ := body["verificationId"].(string)
	if !strings.HasPrefix(id, "verify_") || len(body) != 1 {
		t.Fatalf("dispatch body = %v, want exactly a verificationId", body)
	}
	return id
}

func TestVerification_PassMode_DeliversAllThreeChecksWithBindings(t *testing.T) {
	rcv := newReceiver(t)
	r := newVerificationRunner(t, false)

	id := dispatchVerification(t, r, rcv.url(), "")
	got := rcv.next(t)
	if got.Token != testToken || got.Body.ExternalTaskID != id {
		t.Fatalf("delivery = token %q task %q, want the dispatched token and %q", got.Token, got.Body.ExternalTaskID, id)
	}
	payload := payloadOf(t, got)
	digest := WorkspacePatchDigest(testPatch)
	for key, value := range map[string]any{
		"status": "SUCCEEDED", "passed": true, "testsPassed": true, "scanClean": true, "dependenciesClean": true,
		"baseCommit": verificationBaseCommit, "patchDigest": digest, "logTruncated": false,
	} {
		if payload[key] != value {
			t.Fatalf("payload[%q] = %v, want %v", key, payload[key], value)
		}
	}
	resultDigest, _ := payload["resultDigest"].(string)
	if !strings.HasPrefix(resultDigest, "sha256:") {
		t.Fatalf("resultDigest = %q", resultDigest)
	}
	if log, _ := payload["log"].(string); !strings.Contains(log, digest) {
		t.Fatalf("log = %q, want the mock log naming the patch digest", log)
	}
	summary, _ := payload["summary"].(map[string]any)
	if tests, _ := summary["tests"].(map[string]any); tests["total"] != float64(mockVerificationTests) || summary["scanner"] != scannerSnapshot {
		t.Fatalf("summary = %v", summary)
	}
}

func TestVerification_FailingModes_ReportEachCheckSeparately(t *testing.T) {
	for mock, want := range map[string]map[string]any{
		mockTestsFail:              {"testsPassed": false, "scanClean": true, "dependenciesClean": true},
		mockScanVulnerable:         {"testsPassed": true, "scanClean": false, "dependenciesClean": true},
		mockDependenciesVulnerable: {"testsPassed": true, "scanClean": true, "dependenciesClean": false},
	} {
		rcv := newReceiver(t)
		r := newVerificationRunner(t, false)
		dispatchVerification(t, r, rcv.url(), mock)
		payload := payloadOf(t, rcv.next(t))
		if payload["passed"] != false {
			t.Fatalf("%s: passed = %v, want false", mock, payload["passed"])
		}
		for key, value := range want {
			if payload[key] != value {
				t.Fatalf("%s: payload[%q] = %v, want %v", mock, key, payload[key], value)
			}
		}
	}
}

func TestVerification_ErrorMode_DeliversFailedCallbackWithCode(t *testing.T) {
	rcv := newReceiver(t)
	r := newVerificationRunner(t, false)
	dispatchVerification(t, r, rcv.url(), mockError)
	payload := payloadOf(t, rcv.next(t))
	errDetail, _ := payload["error"].(map[string]any)
	if payload["status"] != "FAILED" || errDetail["code"] != mockErrorCode || payload["log"] != nil {
		t.Fatalf("payload = %v, want FAILED with code %s and no log", payload, mockErrorCode)
	}
}

// A log over the bound is cut on a UTF-8 boundary, ends with the marker and is flagged, and
// the result digest does not depend on the log.
func TestVerification_LogOverLimit_TruncatedAndMarked(t *testing.T) {
	rcv := newReceiver(t)
	r := newVerificationRunner(t, false)
	dispatchVerification(t, r, rcv.url(), mockLogOverflow)
	payload := payloadOf(t, rcv.next(t))
	log, _ := payload["log"].(string)
	if payload["logTruncated"] != true {
		t.Fatalf("logTruncated = %v, want true", payload["logTruncated"])
	}
	marker := log[strings.LastIndex(log, "\n[log truncated"):]
	if !strings.HasPrefix(marker, "\n[log truncated by the sandbox runner: ") || !strings.HasSuffix(marker, " bytes omitted]\n") {
		t.Fatalf("log does not end with the truncation marker: %q", marker)
	}
	if body := strings.TrimSuffix(log, marker); len(body) != MaxVerificationLogBytes {
		t.Fatalf("kept log is %d bytes, want %d", len(body), MaxVerificationLogBytes)
	}
	if payload["passed"] != true {
		t.Fatalf("passed = %v, want true: truncation is not a verdict", payload["passed"])
	}
}

func TestBoundLog_CutsOnARuneBoundary(t *testing.T) {
	// Three-byte runes: the byte limit falls inside a rune, so the cut must move back.
	text := strings.Repeat("€", MaxVerificationLogBytes)
	bounded, truncated := boundLog([]byte(text))
	if !truncated || !strings.HasPrefix(bounded, "€€") {
		t.Fatalf("bounded = %q..., truncated = %t", bounded[:8], truncated)
	}
	kept := bounded[:strings.Index(bounded, "\n[log truncated")]
	want := MaxVerificationLogBytes - MaxVerificationLogBytes%3
	if len(kept) != want || strings.ContainsRune(kept, '�') {
		t.Fatalf("kept %d bytes, want %d with no replacement character", len(kept), want)
	}
	short, truncated := boundLog([]byte("fine\n"))
	if truncated || short != "fine\n" {
		t.Fatalf("short log = %q, truncated = %t", short, truncated)
	}
}

func TestVerification_GetStatus_AnswersOnlyTheVerificationRoute(t *testing.T) {
	rcv := newReceiver(t)
	r := newVerificationRunner(t, false)
	id := dispatchVerification(t, r, rcv.url(), "")
	rcv.next(t)
	r.shutdown(t)

	resp, err := http.Get(r.http.URL + verificationsPath + "/" + id)
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	defer resp.Body.Close()
	var status verificationStatusResponse
	_ = json.NewDecoder(resp.Body).Decode(&status)
	if resp.StatusCode != http.StatusOK || status.VerificationID != id || status.Status != statusSucceeded || !strings.Contains(string(status.Result), `"testsPassed":true`) {
		t.Fatalf("status = %d %+v, want 200 SUCCEEDED with the result", resp.StatusCode, status)
	}
	other, err := http.Get(r.http.URL + testsPath + "/" + id)
	if err != nil {
		t.Fatalf("get test status: %v", err)
	}
	_ = other.Body.Close()
	if other.StatusCode != http.StatusNotFound {
		t.Fatalf("test route answered %d for a verification id, want 404", other.StatusCode)
	}
	unknown, err := http.Get(r.http.URL + verificationsPath + "/verify_0000")
	if err != nil {
		t.Fatalf("get unknown status: %v", err)
	}
	_ = unknown.Body.Close()
	if unknown.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown verification answered %d, want 404", unknown.StatusCode)
	}
}

func TestVerification_InvalidRequest_RejectedWithoutRunning(t *testing.T) {
	rcv := newReceiver(t)
	r := newVerificationRunner(t, false)
	for name, mutate := range map[string]func(map[string]any){
		"missing repositoryId": func(b map[string]any) { delete(b, "repositoryId") },
		"bad repositoryId":     func(b map[string]any) { b["repositoryId"] = "../etc" },
		"missing baseCommit":   func(b map[string]any) { delete(b, "baseCommit") },
		"unknown member":       func(b map[string]any) { b["image"] = "evil" },
		"unknown mock":         func(b map[string]any) { b["mock"] = "fail" },
		"missing token":        func(b map[string]any) { delete(b, "callbackToken") },
	} {
		body := verificationBody(rcv.url(), "")
		mutate(body)
		status, response := r.postVerification(t, body)
		if status != http.StatusBadRequest || response["error"] == "" {
			t.Fatalf("%s: status = %d %v, want 400 with an error", name, status, response)
		}
	}
	// An empty patch verifies the base commit itself.
	body := verificationBody(rcv.url(), "")
	body["patch"] = ""
	if status, _ := r.postVerification(t, body); status != http.StatusAccepted {
		t.Fatalf("empty patch answered %d, want 202", status)
	}
	rcv.next(t)
	r.shutdown(t)
	if extra := rcv.drained(); len(extra) != 0 {
		t.Fatalf("rejected requests delivered %d callbacks", len(extra))
	}
}

func TestVerification_WithoutVerifier_RouteDoesNotExist(t *testing.T) {
	rcv := newReceiver(t)
	r := newRunner(t, NewMockBackend(), false)
	if status, _ := r.postVerification(t, verificationBody(rcv.url(), "")); status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		t.Fatalf("verification route answered %d without a verifier", status)
	}
}

func TestVerification_Record_ListsEveryStepWithoutTheToken(t *testing.T) {
	rcv := newReceiver(t)
	r := newVerificationRunner(t, true)
	id := dispatchVerification(t, r, rcv.url(), mockDuplicate)
	rcv.next(t)
	rcv.next(t)
	r.shutdown(t)

	lines := r.recordLines(t)
	counts := make(map[string]int)
	var completed map[string]any
	for _, line := range lines {
		event := line["kind"].(string) + "/" + line["event"].(string)
		counts[event]++
		if event == "verification/completed" {
			completed = line
		}
	}
	want := map[string]int{"verification/arrived": 1, "verification/responded": 1, "verification/started": 1, "verification/completed": 1, "callback/arrived": 2, "callback/callback": 2}
	for event, n := range want {
		if counts[event] != n {
			t.Fatalf("record has %d %s lines, want %d (all: %v)", counts[event], event, n, counts)
		}
	}
	if completed["verificationId"] != id || completed["passed"] != true || completed["testsPassed"] != true || completed["patchDigest"] != WorkspacePatchDigest(testPatch) {
		t.Fatalf("completed line = %v", completed)
	}
	raw := r.recordRaw(t)
	for _, secret := range []string{testToken, "secret-query", "+new"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("record contains %q", secret)
		}
	}
}

func TestVerification_Restart_RedeliversUnderTheVerificationRoute(t *testing.T) {
	rcv := newReceiver(t)
	first := newVerificationRunner(t, true)
	id := dispatchVerification(t, first, rcv.url(), "")
	rcv.next(t)
	first.shutdown(t)

	second := restartRunner(t, first, NewMockBackend(), []Option{WithVerification(NewMockVerificationBackend())})
	redelivered := rcv.next(t)
	if redelivered.Body.ExternalTaskID != id || redelivered.Token != testToken {
		t.Fatalf("redelivery = %+v", redelivered)
	}
	resp, err := http.Get(second.http.URL + verificationsPath + "/" + id)
	if err != nil {
		t.Fatalf("get status after restart: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status after restart = %d, want 200", resp.StatusCode)
	}
}

func TestVerificationResultDigest_IgnoresTheLog(t *testing.T) {
	outcome := VerificationOutcome{Verdict: true, TestsPassed: true, ScanClean: true, DependenciesClean: true, Log: []byte("a")}
	a := VerificationResultDigest("c", "d", outcome)
	outcome.Log = []byte("b")
	if VerificationResultDigest("c", "d", outcome) != a {
		t.Fatal("the result digest depends on the log")
	}
	outcome.ScanClean = false
	if VerificationResultDigest("c", "d", outcome) == a {
		t.Fatal("the result digest ignores a verdict")
	}
}
