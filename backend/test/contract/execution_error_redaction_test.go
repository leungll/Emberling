//go:build integration

package contract

import (
	"bytes"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// redactingTaskTransport fails every dispatch the way http.Transport reports a refused
// dial: a *url.Error whose text carries the full request URL, here with an internal host
// and a query value standing for a credential.
type redactingTaskTransport struct{}

func (redactingTaskTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, &url.Error{Op: "Post", URL: "http://mockprovider:9101/v1/tasks?key=s3cr3t", Err: &net.OpError{
		Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused"),
	}}
}

// TestE2E_AIGC_ProviderTransportFailure_APIBodiesCarryNoProviderURLOrTransportText runs
// the production image_generation Node and mocktask Adapter through the HTTP contract with
// a dispatch that fails at the transport. The image Attempt fails as EXECUTOR_ERROR with
// the Adapter's stable message, and no response body Studio reads - the Run Snapshot, the
// Event list or the NodeRun detail - nor the backend log names the URL query, and only the
// log names the Provider host or the raw transport text.
func TestE2E_AIGC_ProviderTransportFailure_APIBodiesCarryNoProviderURLOrTransportText(t *testing.T) {
	logs := &syncBuffer{}
	env := newTestEnvWithOptions(t, testEnvOptions{
		MockTaskBaseURL: "http://mockprovider:9101",
		TaskClient:      &http.Client{Transport: redactingTaskTransport{}},
		Logger:          slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})

	runID := env.createAIGCRun(t, "a small ember creature")
	env.waitForTerminal(t, runID, e2eWait)
	if status := env.runStatus(t, runID); status != "FAILED" {
		t.Fatalf("Run status = %q, want FAILED", status)
	}

	image := env.nodeRunOf(t, runID, "image")
	imageNodeRunID, _ := image["id"].(string)
	detail, detailBody := env.nodeRunDetail(t, runID, imageNodeRunID)
	attempts := attemptsOf(t, detail)
	if len(attempts) != 1 {
		t.Fatalf("image Attempts = %d, want 1", len(attempts))
	}
	attemptError, _ := attempts[0]["error"].(map[string]any)
	const want = `image_generation: dispatch: mocktask: dispatch to provider "mock-task-provider-v1": send request: provider request failed: transport error`
	if code, _ := attemptError["code"].(string); code != "EXECUTOR_ERROR" {
		t.Errorf("image Attempt error.code = %q, want EXECUTOR_ERROR", code)
	}
	if message, _ := attemptError["message"].(string); message != want {
		t.Errorf("image Attempt error.message = %q, want %q", message, want)
	}

	_, snapshotBody := env.doJSON(t, http.MethodGet, "/api/runs/"+runID, nil)
	_, eventsBody := env.doJSON(t, http.MethodGet, "/api/runs/"+runID+"/events", nil)
	for what, body := range map[string][]byte{
		"Run Snapshot": snapshotBody, "Event list": eventsBody, "image NodeRun detail": detailBody,
	} {
		for _, leak := range []string{"s3cr3t", "key=", "mockprovider", "9101", `Post \"`, "connection refused"} {
			if bytes.Contains(body, []byte(leak)) {
				t.Errorf("%s body carries %q: %s", what, leak, body)
			}
		}
	}

	logged := logs.String()
	if !strings.Contains(logged, "node executor failed") || !strings.Contains(logged, "connection refused") {
		t.Errorf("backend log lacks the redacted executor failure detail: %s", logged)
	}
	for _, leak := range []string{"s3cr3t", "key="} {
		if strings.Contains(logged, leak) {
			t.Errorf("backend log carries %q", leak)
		}
	}
}
