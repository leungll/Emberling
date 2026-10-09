package imagegeneration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// stubPoller answers every Poll with a fixed normalised answer and counts the calls.
type stubPoller struct {
	status  registry.PollStatus
	payload json.RawMessage
	err     error

	calls          int
	externalTaskID string
}

func (p *stubPoller) Poll(_ context.Context, externalTaskID string) (registry.PollStatus, json.RawMessage, error) {
	p.calls++
	p.externalTaskID = externalTaskID
	return p.status, p.payload, p.err
}

func pollingExecutor(poller TaskPoller) Executor {
	return Executor{resolver: stubResolver{model: imageModel(), ok: true}, dispatcher: &recordingDispatcher{}, poller: poller}
}

// TestImageGenerationNode_PollSucceeded_OutputEqualsOnCallbackOutput: a SUCCEEDED poll
// carries the payload the callback would carry, and the node must turn it into exactly
// the output OnCallback produces, whichever path wins the resume.
func TestImageGenerationNode_PollSucceeded_OutputEqualsOnCallbackOutput(t *testing.T) {
	cases := map[string]string{
		"external":                `{"status":"SUCCEEDED","image":{"mediaType":"image/png","width":1024,"uri":"http://provider.test/v1/images/abc.png","source":"EXTERNAL"}}`,
		"asset":                   `{"status":"SUCCEEDED","image":{"source":"ASSET","asset":{"assetId":"asset_123","mediaType":"image/png","sizeBytes":102400,"sha256":"abc"}}}`,
		"adapter success payload": `{"status":"SUCCEEDED","image":{"source":"EXTERNAL","uri":"http://mock-provider.test/v1/images/0011223344556677.png","mediaType":"image/png","width":512}}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			poller := &stubPoller{status: registry.PollSucceeded, payload: json.RawMessage(payload)}
			e := pollingExecutor(poller)

			want, err := e.OnCallback(context.Background(), asyncState(), []byte(payload))
			if err != nil {
				t.Fatalf("OnCallback() error = %v, want nil", err)
			}
			got, err := e.Poll(context.Background(), asyncState())
			if err != nil {
				t.Fatalf("Poll() error = %v, want nil", err)
			}

			if got.Status != registry.PollSucceeded {
				t.Fatalf("Poll() status = %q, want %q", got.Status, registry.PollSucceeded)
			}
			if got.Output == nil {
				t.Fatal("Poll() output = nil, want the OnCallback output")
			}
			if diff := cmp.Diff(want, *got.Output); diff != "" {
				t.Fatalf("Poll() output differs from OnCallback output (-callback +poll):\n%s", diff)
			}
			if got.Error != nil || got.ToolResult != nil {
				t.Fatalf("Poll() = %+v, want only Status and Output", got)
			}
			if poller.calls != 1 || poller.externalTaskID != asyncState().ExternalTask.ExternalTaskID {
				t.Fatalf("poller calls = %d for %q, want exactly 1 for %q", poller.calls, poller.externalTaskID, asyncState().ExternalTask.ExternalTaskID)
			}
		})
	}
}

// TestImageGenerationNode_PollFailed_MapsToTheCallbackProviderFailure: a FAILED poll carries
// the same ExecutionError the callback path reports through ProviderFailure.
func TestImageGenerationNode_PollFailed_MapsToTheCallbackProviderFailure(t *testing.T) {
	cases := map[string]string{
		"reported code and message": `{"status":"FAILED","error":{"code":"PROVIDER_TASK_FAILED","message":"mock provider: task failed by scenario"}}`,
		"no error object":           `{"status":"FAILED"}`,
		"custom code":               `{"status":"FAILED","error":{"code":"CONTENT_REJECTED"}}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			e := pollingExecutor(&stubPoller{status: registry.PollFailed, payload: json.RawMessage(payload)})

			_, callbackErr := e.OnCallback(context.Background(), asyncState(), []byte(payload))
			var failure *registry.ProviderFailure
			if !errors.As(callbackErr, &failure) {
				t.Fatalf("OnCallback() error = %v, want a ProviderFailure", callbackErr)
			}

			got, err := e.Poll(context.Background(), asyncState())
			if err != nil {
				t.Fatalf("Poll() error = %v, want nil", err)
			}
			if got.Status != registry.PollFailed {
				t.Fatalf("Poll() status = %q, want %q", got.Status, registry.PollFailed)
			}
			if got.Error == nil {
				t.Fatal("Poll() error field = nil, want the reported ExecutionError")
			}
			if diff := cmp.Diff(failure.Err, *got.Error); diff != "" {
				t.Fatalf("Poll() failure differs from the callback ProviderFailure (-callback +poll):\n%s", diff)
			}
			if got.Output != nil {
				t.Fatalf("Poll() output = %+v, want none for a failure", got.Output)
			}
		})
	}
}

// TestImageGenerationNode_PollUnknown_ReturnsUnknownWithoutOutput: every answer that does
// not establish a terminal outcome the callback path would accept is unknown, carries no
// output and no failure, and is not an error, so it can change no Attempt state.
func TestImageGenerationNode_PollUnknown_ReturnsUnknownWithoutOutput(t *testing.T) {
	cases := map[string]*stubPoller{
		"adapter reports unknown":             {status: registry.PollStatusUnknown},
		"unrecognised status":                 {status: registry.PollStatus("QUEUED")},
		"succeeded payload is not json":       {status: registry.PollSucceeded, payload: json.RawMessage(`}{`)},
		"succeeded without valid image":       {status: registry.PollSucceeded, payload: json.RawMessage(`{"status":"SUCCEEDED","image":{"url":"http://provider.test/a.png"}}`)},
		"succeeded status but failed payload": {status: registry.PollSucceeded, payload: json.RawMessage(`{"status":"FAILED"}`)},
		"failed status but succeeded payload": {status: registry.PollFailed, payload: json.RawMessage(`{"status":"SUCCEEDED","image":{"source":"EXTERNAL","uri":"http://provider.test/a.png","mediaType":"image/png"}}`)},
	}
	for name, poller := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := pollingExecutor(poller).Poll(context.Background(), asyncState())
			if err != nil {
				t.Fatalf("Poll() error = %v, want nil", err)
			}
			want := registry.PollResult{Status: registry.PollStatusUnknown}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatalf("Poll() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestImageGenerationNode_PollRunning_ReturnsRunningWithoutOutput(t *testing.T) {
	got, err := pollingExecutor(&stubPoller{status: registry.PollRunning}).Poll(context.Background(), asyncState())
	if err != nil {
		t.Fatalf("Poll() error = %v, want nil", err)
	}
	if diff := cmp.Diff(registry.PollResult{Status: registry.PollRunning}, got); diff != "" {
		t.Fatalf("Poll() mismatch (-want +got):\n%s", diff)
	}
}

// TestImageGenerationNode_PollTransportError_ReturnedForTheCaller: a query that got no
// answer is the caller's to judge; the node wraps it so errors.As still reaches the
// Adapter's error, and decides nothing itself.
func TestImageGenerationNode_PollTransportError_ReturnedForTheCaller(t *testing.T) {
	transport := errors.New("connection refused")
	poller := &stubPoller{err: transport}

	got, err := pollingExecutor(poller).Poll(context.Background(), asyncState())
	if !errors.Is(err, transport) {
		t.Fatalf("Poll() error = %v, want it to wrap %v", err, transport)
	}
	if got.Status != "" || got.Output != nil || got.Error != nil {
		t.Fatalf("Poll() result = %+v, want the zero result with an error", got)
	}
	if poller.calls != 1 {
		t.Fatalf("poller calls = %d, want exactly 1 (no retry)", poller.calls)
	}
}

func TestImageGenerationNode_Metadata_DeclaresPollPolicy(t *testing.T) {
	meta := Registration(stubResolver{model: imageModel(), ok: true}, &recordingDispatcher{}).Metadata
	want := domain.PollPolicy{IntervalMs: 2000, MaxPolls: 30}
	if meta.Poll == nil || *meta.Poll != want {
		t.Fatalf("Poll = %+v, want %+v", meta.Poll, want)
	}
}

// TestImageGenerationNode_Registration_PollPolicyRemoved_RegistrationFails proves the
// Registry ties the declared policy to the pollable Executor: the same Executor without
// the declaration is rejected, so the registration only succeeds because both are present.
func TestImageGenerationNode_Registration_PollPolicyRemoved_RegistrationFails(t *testing.T) {
	reg := Registration(stubResolver{model: imageModel(), ok: true}, &recordingDispatcher{})
	if err := registry.NewNodeRegistry().Register(reg); err != nil {
		t.Fatalf("Register() with the poll policy error = %v, want nil", err)
	}

	withoutPolicy := reg
	withoutPolicy.Metadata.Poll = nil
	err := registry.NewNodeRegistry().Register(withoutPolicy)
	if err == nil || !strings.Contains(err.Error(), "metadata declares no poll policy") {
		t.Fatalf("Register() without the poll policy error = %v, want the missing-policy rejection", err)
	}
}
