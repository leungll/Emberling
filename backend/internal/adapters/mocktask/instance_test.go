package mocktask

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const gatewayProviderID = "task-gateway-v1"

// recordingProvider records each dispatch body and answers 202, then answers every status
// query with 503 so a poll is still a normalised answer.
func recordingProvider(t *testing.T, dispatchStatus int) (*httptest.Server, *map[string]json.RawMessage) {
	t.Helper()
	body := map[string]json.RawMessage{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode dispatch body: %v", err)
			}
			w.WriteHeader(dispatchStatus)
			_, _ = w.Write([]byte(`{"externalTaskId":"gw_0000000000000001"}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	return server, &body
}

func TestAdapter_WithProviderID_ReportsIDInTaskAndErrors(t *testing.T) {
	server, _ := recordingProvider(t, http.StatusAccepted)
	adapter := New(server.URL, server.Client(), WithProviderID(gatewayProviderID))
	if adapter.ProviderID() != gatewayProviderID {
		t.Fatalf("ProviderID() = %q, want %q", adapter.ProviderID(), gatewayProviderID)
	}
	task, err := adapter.Dispatch(context.Background(), "prompt", nil, nil, testCallback(), "nr_1")
	if err != nil || task.ProviderID != gatewayProviderID {
		t.Fatalf("Dispatch() = %+v, %v; want the instance Provider ID", task, err)
	}

	rejecting, _ := recordingProvider(t, http.StatusTooManyRequests)
	_, err = New(rejecting.URL, rejecting.Client(), WithProviderID(gatewayProviderID)).Dispatch(context.Background(), "prompt", nil, nil, testCallback(), "nr_1")
	var dispatchErr *DispatchError
	if !errors.As(err, &dispatchErr) || dispatchErr.ProviderID != gatewayProviderID {
		t.Fatalf("Dispatch() error = %v, want a DispatchError naming %q", err, gatewayProviderID)
	}

	_, _, err = New("http://127.0.0.1:1", nil, WithProviderID(gatewayProviderID)).Poll(context.Background(), "gw_1")
	var pollErr *PollError
	if !errors.As(err, &pollErr) || pollErr.ProviderID != gatewayProviderID {
		t.Fatalf("Poll() error = %v, want a PollError naming %q", err, gatewayProviderID)
	}

	// Without the option the instance keeps the Mock Provider's identity.
	if got := New(server.URL, nil).ProviderID(); got != ProviderID {
		t.Fatalf("default ProviderID() = %q, want %q", got, ProviderID)
	}
}

func TestAdapter_WithoutMockScenarios_ForwardsPromptVerbatim(t *testing.T) {
	for _, prompt := range []string{"mock:failed", "mock:delay:500", "a small ember creature"} {
		t.Run(prompt, func(t *testing.T) {
			server, body := recordingProvider(t, http.StatusAccepted)
			adapter := New(server.URL, server.Client(), WithoutMockScenarios())
			if _, err := adapter.Dispatch(context.Background(), prompt, nil, map[string]any{"modelId": "gateway-x"}, testCallback(), "nr_1"); err != nil {
				t.Fatalf("Dispatch() error = %v", err)
			}
			var got string
			_ = json.Unmarshal((*body)["prompt"], &got)
			if got != prompt {
				t.Fatalf("prompt = %q, want it forwarded verbatim", got)
			}
			for _, member := range []string{"payload", "outcome", "delayMs"} {
				if _, present := (*body)[member]; present {
					t.Fatalf("dispatch body carries %q, want no Mock Provider scenario member", member)
				}
			}
		})
	}
}

func TestAdapter_ContextDeadline_SentAsDeadline(t *testing.T) {
	server, body := recordingProvider(t, http.StatusAccepted)
	// A whole-second deadline in a non-UTC zone: the member must be its UTC RFC 3339 form.
	deadline := time.Now().Add(time.Hour).Truncate(time.Second).In(time.FixedZone("UTC+8", 8*3600))
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if _, err := New(server.URL, server.Client()).Dispatch(ctx, "prompt", nil, nil, testCallback(), "nr_1"); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if got, want := string((*body)["deadline"]), `"`+deadline.UTC().Format(time.RFC3339)+`"`; got != want {
		t.Fatalf("deadline = %s, want the context deadline in RFC 3339 UTC", got)
	}

	without, bodyWithout := recordingProvider(t, http.StatusAccepted)
	if _, err := New(without.URL, without.Client()).Dispatch(context.Background(), "prompt", nil, nil, testCallback(), "nr_1"); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if _, present := (*bodyWithout)["deadline"]; present {
		t.Fatal("dispatch without a context deadline carries a deadline member")
	}
}
