package externaltask_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/externaltask"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const (
	taskID   = "ext_0123456789abcdef0123456789abcdef"
	token    = "one-time-callback-token"
	callback = "http://emberling.internal/v1/callbacks"
)

// service is a scripted task service: it records the submit body and answers with the
// scripted status codes and bodies.
type service struct {
	mu           sync.Mutex
	submitStatus int
	submits      []map[string]any
	getStatus    int
	getBody      string
	getPaths     []string
}

func (s *service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/tasks":
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.submits = append(s.submits, body)
		s.mu.Unlock()
		w.WriteHeader(s.submitStatus)
		_, _ = io.WriteString(w, `{"taskId":"`+taskID+`","callbackToken":"`+token+`"}`)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/tasks/"):
		s.mu.Lock()
		s.getPaths = append(s.getPaths, r.URL.Path)
		s.mu.Unlock()
		w.WriteHeader(s.getStatus)
		_, _ = io.WriteString(w, s.getBody)
	default:
		http.NotFound(w, r)
	}
}

func newClient(t *testing.T, s *service) *externaltask.Client {
	t.Helper()
	server := httptest.NewServer(s)
	t.Cleanup(server.Close)
	return externaltask.New(server.URL+"/", server.Client())
}

func TestSubmit_AcceptedOrAlreadyHeld_IsSuccessWithProtocolBody(t *testing.T) {
	for name, status := range map[string]int{"created": http.StatusAccepted, "already held": http.StatusOK} {
		t.Run(name, func(t *testing.T) {
			s := &service{submitStatus: status}
			client := newClient(t, s)
			input := json.RawMessage(`{"text":"hello"}`)
			if err := client.Submit(context.Background(), taskID, "ECHO", input, registry.CallbackContext{URL: callback, Token: token}); err != nil {
				t.Fatalf("Submit: %v", err)
			}
			if len(s.submits) != 1 {
				t.Fatalf("submits = %d, want one", len(s.submits))
			}
			got := s.submits[0]
			if got["taskId"] != taskID || got["type"] != "ECHO" || got["callbackUrl"] != callback || got["callbackToken"] != token {
				t.Errorf("submit body = %v, want the task id, type, callback URL and token", got)
			}
			if in, _ := got["input"].(map[string]any); in["text"] != "hello" {
				t.Errorf("submit input = %v, want the task input verbatim", got["input"])
			}
		})
	}
}

func TestSubmit_RejectedOrUnreachable_ErrorCarriesNoSecret(t *testing.T) {
	s := &service{submitStatus: http.StatusServiceUnavailable}
	client := newClient(t, s)
	err := client.Submit(context.Background(), taskID, "ECHO", json.RawMessage(`{"text":"secret-input"}`), registry.CallbackContext{URL: callback, Token: token})
	var reqErr *externaltask.RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != http.StatusServiceUnavailable || reqErr.TaskID != taskID {
		t.Fatalf("error = %v, want a RequestError with status 503 for the task", err)
	}
	for _, leak := range []string{token, callback, "secret-input", "127.0.0.1"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error %q leaks %q", err, leak)
		}
	}

	closed := httptest.NewServer(s)
	closed.Close()
	unreachable := externaltask.New(closed.URL, closed.Client())
	err = unreachable.Submit(context.Background(), taskID, "ECHO", nil, registry.CallbackContext{URL: callback, Token: token})
	if !errors.As(err, &reqErr) || reqErr.StatusCode != 0 {
		t.Fatalf("error = %v, want a RequestError without a status", err)
	}
	if strings.Contains(err.Error(), closed.URL) || strings.Contains(err.Error(), token) {
		t.Errorf("error %q leaks the service URL or the token", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = client.Submit(ctx, taskID, "ECHO", nil, registry.CallbackContext{URL: callback, Token: token})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error after cancellation = %v, want it to wrap context.Canceled", err)
	}
}

func TestSubmit_IncompleteCall_SendsNothing(t *testing.T) {
	s := &service{submitStatus: http.StatusAccepted}
	client := newClient(t, s)
	for name, call := range map[string]func() error{
		"no task id": func() error {
			return client.Submit(context.Background(), "", "ECHO", nil, registry.CallbackContext{URL: callback, Token: token})
		},
		"no type": func() error {
			return client.Submit(context.Background(), taskID, "", nil, registry.CallbackContext{URL: callback, Token: token})
		},
		"no callback": func() error {
			return client.Submit(context.Background(), taskID, "ECHO", nil, registry.CallbackContext{})
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s: Submit error = nil, want a request error", name)
		}
	}
	if len(s.submits) != 0 {
		t.Errorf("submits = %d, want none", len(s.submits))
	}
}

func TestGet_NormalisesEveryAnswer(t *testing.T) {
	succeeded := `{"taskId":"` + taskID + `","status":"SUCCEEDED","result":{"echo":"hello"}}`
	failed := `{"taskId":"` + taskID + `","status":"FAILED","error":{"code":"RENDER_FAILED","message":"ffmpeg exited 1"}}`
	for name, tc := range map[string]struct {
		status      int
		body        string
		want        registry.PollStatus
		wantPayload string
	}{
		"queued":                   {status: 200, body: `{"taskId":"` + taskID + `","status":"QUEUED"}`, want: registry.PollRunning},
		"running":                  {status: 200, body: `{"taskId":"` + taskID + `","status":"RUNNING"}`, want: registry.PollRunning},
		"succeeded":                {status: 200, body: succeeded, want: registry.PollSucceeded, wantPayload: succeeded},
		"failed":                   {status: 200, body: failed, want: registry.PollFailed, wantPayload: failed},
		"not found":                {status: 404, body: `{"error":"unknown task"}`, want: registry.PollUnknownTask},
		"server error":             {status: 500, body: `oops`, want: registry.PollStatusUnknown},
		"unreadable":               {status: 200, body: `not json`, want: registry.PollStatusUnknown},
		"other task":               {status: 200, body: `{"taskId":"ext_other","status":"SUCCEEDED","result":{}}`, want: registry.PollStatusUnknown},
		"unknown status":           {status: 200, body: `{"taskId":"` + taskID + `","status":"PAUSED"}`, want: registry.PollStatusUnknown},
		"succeeded without result": {status: 200, body: `{"taskId":"` + taskID + `","status":"SUCCEEDED","result":null}`, want: registry.PollStatusUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			s := &service{getStatus: tc.status, getBody: tc.body}
			client := newClient(t, s)
			got, payload, err := client.Get(context.Background(), taskID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got != tc.want {
				t.Fatalf("status = %s, want %s", got, tc.want)
			}
			if string(payload) != tc.wantPayload {
				t.Errorf("payload = %s, want %q", payload, tc.wantPayload)
			}
			if len(s.getPaths) != 1 || s.getPaths[0] != "/v1/tasks/"+taskID {
				t.Errorf("status queries = %v, want exactly one for the task", s.getPaths)
			}
		})
	}
}

func TestGet_NoResponse_IsRequestErrorWithoutURL(t *testing.T) {
	closed := httptest.NewServer(&service{})
	closed.Close()
	client := externaltask.New(closed.URL, closed.Client())
	_, _, err := client.Get(context.Background(), taskID)
	var reqErr *externaltask.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("error = %v, want a RequestError", err)
	}
	if strings.Contains(err.Error(), closed.URL) {
		t.Errorf("error %q leaks the service URL", err)
	}
	if _, _, err := client.Get(context.Background(), " "); err == nil {
		t.Error("Get with no task id succeeded, want an error")
	}
}
