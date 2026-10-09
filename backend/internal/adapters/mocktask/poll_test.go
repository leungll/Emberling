package mocktask

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/leungll/Emberling/backend/internal/registry"
)

const pollTaskID = "provider_task_789"

// pollServer answers every request with status and body and records what it received.
type pollServer struct {
	*httptest.Server
	requests atomic.Int32
	last     atomic.Pointer[receivedPoll]
}

// receivedPoll is what the server saw of one request.
type receivedPoll struct {
	method        string
	path          string
	rawQuery      string
	authorization string
	contentLength int64
}

func newPollServer(t *testing.T, status int, body string) *pollServer {
	t.Helper()
	s := &pollServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		s.last.Store(&receivedPoll{
			method:        r.Method,
			path:          r.URL.Path,
			rawQuery:      r.URL.RawQuery,
			authorization: r.Header.Get("Authorization"),
			contentLength: r.ContentLength,
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func TestMockTaskAdapter_Poll_NormalisesProviderAnswer(t *testing.T) {
	const succeededPayload = `{"status":"SUCCEEDED","image":{"source":"EXTERNAL","uri":"http://provider.test/v1/images/abc.png","mediaType":"image/png"}}`
	const failedPayload = `{"status":"FAILED","error":{"code":"PROVIDER_TASK_FAILED","message":"mock provider: task failed by scenario"}}`

	cases := map[string]struct {
		status      int
		body        string
		wantStatus  registry.PollStatus
		wantPayload string
	}{
		"running": {
			status: http.StatusOK, body: `{"externalTaskId":"` + pollTaskID + `","status":"RUNNING"}`,
			wantStatus: registry.PollRunning,
		},
		"succeeded with payload": {
			status: http.StatusOK, body: `{"externalTaskId":"` + pollTaskID + `","status":"SUCCEEDED","payload":` + succeededPayload + `}`,
			wantStatus: registry.PollSucceeded, wantPayload: succeededPayload,
		},
		"failed with payload": {
			status: http.StatusOK, body: `{"externalTaskId":"` + pollTaskID + `","status":"FAILED","payload":` + failedPayload + `}`,
			wantStatus: registry.PollFailed, wantPayload: failedPayload,
		},
		"404 unknown task": {
			status: http.StatusNotFound, body: `{"error":"unknown externalTaskId"}`,
			wantStatus: registry.PollStatusUnknown,
		},
		"500": {
			status: http.StatusInternalServerError, body: `{"error":"boom"}`,
			wantStatus: registry.PollStatusUnknown,
		},
		"malformed json": {
			status: http.StatusOK, body: `{"externalTaskId":`,
			wantStatus: registry.PollStatusUnknown,
		},
		"unrecognised status": {
			status: http.StatusOK, body: `{"externalTaskId":"` + pollTaskID + `","status":"QUEUED"}`,
			wantStatus: registry.PollStatusUnknown,
		},
		"terminal without payload": {
			status: http.StatusOK, body: `{"externalTaskId":"` + pollTaskID + `","status":"SUCCEEDED"}`,
			wantStatus: registry.PollStatusUnknown,
		},
		"answer about another task": {
			status: http.StatusOK, body: `{"externalTaskId":"other_task","status":"SUCCEEDED","payload":` + succeededPayload + `}`,
			wantStatus: registry.PollStatusUnknown,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server := newPollServer(t, tc.status, tc.body)
			adapter := New(server.URL, server.Client())

			status, payload, err := adapter.Poll(context.Background(), pollTaskID)
			if err != nil {
				t.Fatalf("Poll() error = %v, want nil", err)
			}
			if status != tc.wantStatus {
				t.Fatalf("Poll() status = %q, want %q", status, tc.wantStatus)
			}
			if string(payload) != tc.wantPayload {
				t.Fatalf("Poll() payload = %s, want %s", payload, tc.wantPayload)
			}
			if got := server.requests.Load(); got != 1 {
				t.Fatalf("Provider received %d requests, want exactly 1", got)
			}
		})
	}
}

// TestMockTaskAdapter_Poll_SendsOneCredentialFreeGet pins the wire shape of a status query:
// one GET on the task's own route, with no Authorization header, no body and no token in
// the query string. A poll is identified by the external task id alone.
func TestMockTaskAdapter_Poll_SendsOneCredentialFreeGet(t *testing.T) {
	server := newPollServer(t, http.StatusOK, `{"externalTaskId":"`+pollTaskID+`","status":"RUNNING"}`)
	adapter := New(server.URL, server.Client())

	if _, _, err := adapter.Poll(context.Background(), pollTaskID); err != nil {
		t.Fatalf("Poll() error = %v, want nil", err)
	}

	if got := server.requests.Load(); got != 1 {
		t.Fatalf("Provider received %d requests, want exactly 1", got)
	}
	request := server.last.Load()
	if request.method != http.MethodGet {
		t.Errorf("method = %s, want GET", request.method)
	}
	if request.path != "/v1/tasks/"+pollTaskID {
		t.Errorf("path = %s, want /v1/tasks/%s", request.path, pollTaskID)
	}
	if request.rawQuery != "" {
		t.Errorf("query = %q, want none", request.rawQuery)
	}
	if auth := request.authorization; auth != "" {
		t.Errorf("Authorization header = %q, want none", auth)
	}
	if request.contentLength > 0 {
		t.Errorf("request body length = %d, want none", request.contentLength)
	}
}

// TestMockTaskAdapter_Poll_TransportError_ReturnedWithoutRetry: a query that never got an
// HTTP response is the caller's to judge. The Adapter returns it as a *PollError after one
// attempt and does not retry.
func TestMockTaskAdapter_Poll_TransportError_ReturnedWithoutRetry(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response writer cannot be hijacked")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
	t.Cleanup(server.Close)
	adapter := New(server.URL, server.Client())

	status, payload, err := adapter.Poll(context.Background(), pollTaskID)
	if err == nil {
		t.Fatalf("Poll() error = nil, want a transport error; status = %q", status)
	}
	var pollErr *PollError
	if !errors.As(err, &pollErr) {
		t.Fatalf("Poll() error = %v (%T), want *PollError", err, err)
	}
	if pollErr.ProviderID != ProviderID || pollErr.ExternalTaskID != pollTaskID {
		t.Errorf("PollError = %+v, want ProviderID %q and ExternalTaskID %q", pollErr, ProviderID, pollTaskID)
	}
	if status != "" || payload != nil {
		t.Errorf("Poll() = (%q, %s), want no status and no payload with an error", status, payload)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("Provider received %d requests, want exactly 1 (no retry)", got)
	}
}

func TestMockTaskAdapter_Poll_DialFailure_ReturnsError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	baseURL := server.URL
	server.Close()

	_, _, err := New(baseURL, nil).Poll(context.Background(), pollTaskID)
	var pollErr *PollError
	if !errors.As(err, &pollErr) {
		t.Fatalf("Poll() error = %v (%T), want *PollError", err, err)
	}
}

func TestMockTaskAdapter_Poll_EmptyExternalTaskID_SendsNothing(t *testing.T) {
	server := newPollServer(t, http.StatusOK, `{}`)

	_, _, err := New(server.URL, server.Client()).Poll(context.Background(), " ")
	if err == nil {
		t.Fatal("Poll() error = nil, want an error for an empty externalTaskId")
	}
	if got := server.requests.Load(); got != 0 {
		t.Fatalf("Provider received %d requests, want none", got)
	}
}
