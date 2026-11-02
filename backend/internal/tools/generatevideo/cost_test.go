package generatevideo

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// TestRegistration_DeclaresAStableCostSource: the video task is bound to no Model ID, so
// the Tool declares in its Metadata the stable identifier its charges are attributed to.
// It is not the Provider identifier, which names where the task was sent, and it is what
// the Executor records.
func TestRegistration_DeclaresAStableCostSource(t *testing.T) {
	reg := Registration("http://mock-provider.test", nil)
	if reg.Metadata.CostSource != CostSource || CostSource == "" {
		t.Fatalf("metadata costSource = %q, want %q", reg.Metadata.CostSource, CostSource)
	}
	if reg.Metadata.CostSource == ProviderID {
		t.Fatalf("metadata costSource = %q equals the Provider identifier; want a separate stable identifier", CostSource)
	}
}

// TestOnCallback_ProviderPricesTask_ToolResultCarriesChargeForTheVideoSource: the Mock
// Provider prices every task result in usage.cost; the callback's charge reaches the Tool
// result as an amount in US dollars attributed to the Tool's declared cost source.
func TestOnCallback_ProviderPricesTask_ToolResultCarriesChargeForTheVideoSource(t *testing.T) {
	provider, sink := newProvider(t), newCallbackSink(t)
	executor := New(provider.URL, provider.Client())

	state := dispatch(t, executor, action(sink.server.URL, `{"durationSeconds":4}`))
	payload := sink.next(t)
	var members map[string]json.RawMessage
	if err := json.Unmarshal(payload, &members); err != nil || len(members["usage"]) == 0 {
		t.Fatalf("callback payload %s carries no usage: the Provider must price the task for this test", payload)
	}
	got, err := executor.OnCallback(context.Background(), state, payload)
	if err != nil {
		t.Fatalf("on callback: %v", err)
	}
	want := &domain.Cost{Amount: "0.04", Currency: domain.CurrencyUSD, Source: CostSource}
	if diff := cmp.Diff(want, got.Cost); diff != "" {
		t.Fatalf("result cost (-want +got):\n%s", diff)
	}
}

// TestOnCallback_NoUsageOrUnusableCost_ResultCostIsNilWithoutError: a success payload
// without usage, or with a cost that is not a non-negative number, still yields the video
// result; its Cost is nil, never zero, and the delivery is not rejected.
func TestOnCallback_NoUsageOrUnusableCost_ResultCostIsNilWithoutError(t *testing.T) {
	cases := map[string]string{
		"no usage":      `{"status":"SUCCEEDED","videoUrl":"https://provider.test/v1/videos/task_1.mp4"}`,
		"null usage":    `{"status":"SUCCEEDED","videoUrl":"https://provider.test/v1/videos/task_1.mp4","usage":null}`,
		"no cost":       `{"status":"SUCCEEDED","videoUrl":"https://provider.test/v1/videos/task_1.mp4","usage":{}}`,
		"null cost":     `{"status":"SUCCEEDED","videoUrl":"https://provider.test/v1/videos/task_1.mp4","usage":{"cost":null}}`,
		"string cost":   `{"status":"SUCCEEDED","videoUrl":"https://provider.test/v1/videos/task_1.mp4","usage":{"cost":"0.04"}}`,
		"negative cost": `{"status":"SUCCEEDED","videoUrl":"https://provider.test/v1/videos/task_1.mp4","usage":{"cost":-0.04}}`,
		"usage not obj": `{"status":"SUCCEEDED","videoUrl":"https://provider.test/v1/videos/task_1.mp4","usage":"0.04"}`,
	}
	executor := New("http://mock-provider.test", nil)
	state := registry.ToolAsyncState{AgentRunID: "ar_1", ActionID: "act_1", ToolName: ToolName, AttemptNo: 1,
		ExternalTask: registry.ExternalTask{ProviderID: ProviderID, ExternalTaskID: "task_1"}}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := executor.OnCallback(context.Background(), state, json.RawMessage(payload))
			if err != nil {
				t.Fatalf("on callback: %v", err)
			}
			if got.Cost != nil {
				t.Fatalf("result cost = %+v, want nil", *got.Cost)
			}
			var value map[string]any
			if err := json.Unmarshal(got.Output, &value); err != nil || value["videoUrl"] == "" {
				t.Fatalf("output = %s, want the video URL regardless of the usage member", got.Output)
			}
		})
	}
}
