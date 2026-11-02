package imagegeneration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const succeededImage = `{"source":"EXTERNAL","uri":"http://provider.test/v1/images/abc.png","mediaType":"image/png","width":1024}`

// TestImageGenerationNode_OnCallback_UsageCost_RecordedOnOutput: a SUCCEEDED payload's
// usage.cost becomes the output's Cost, in US dollars, attributed to the configured
// Model ID, with the amount rounded to the persisted precision.
func TestImageGenerationNode_OnCallback_UsageCost_RecordedOnOutput(t *testing.T) {
	e := newTestExecutor(stubResolver{model: imageModel(), ok: true}, &recordingDispatcher{})
	cases := map[string]struct {
		usage string
		want  string
	}{
		"fixed mock cost":  {usage: `{"cost":0.04}`, want: "0.04"},
		"integer":          {usage: `{"cost":2}`, want: "2"},
		"rounded to micro": {usage: `{"cost":0.0000315}`, want: "0.000032"},
		"zero is known":    {usage: `{"cost":0}`, want: "0"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			payload := []byte(`{"status":"SUCCEEDED","image":` + succeededImage + `,"usage":` + tc.usage + `}`)
			output, err := e.OnCallback(context.Background(), asyncState(), payload)
			if err != nil {
				t.Fatalf("OnCallback() error = %v, want nil", err)
			}
			want := &domain.Cost{Amount: tc.want, Currency: domain.CurrencyUSD, Source: "image-model-v1"}
			if diff := cmp.Diff(want, output.Cost); diff != "" {
				t.Fatalf("output cost (-want +got):\n%s", diff)
			}
		})
	}
}

// TestImageGenerationNode_OnCallback_NoUsageCost_OutputCostIsNil: a Provider that reports
// no charge, or one this node cannot read as a non-negative number, leaves the output
// without a Cost. The image is still accepted: a tracing value never fails a result.
func TestImageGenerationNode_OnCallback_NoUsageCost_OutputCostIsNil(t *testing.T) {
	e := newTestExecutor(stubResolver{model: imageModel(), ok: true}, &recordingDispatcher{})
	cases := map[string]string{
		"no usage":         ``,
		"empty usage":      `,"usage":{}`,
		"null cost":        `,"usage":{"cost":null}`,
		"string cost":      `,"usage":{"cost":"0.04"}`,
		"negative cost":    `,"usage":{"cost":-1}`,
		"usage not object": `,"usage":"0.04"`,
	}
	for name, usage := range cases {
		t.Run(name, func(t *testing.T) {
			payload := []byte(`{"status":"SUCCEEDED","image":` + succeededImage + usage + `}`)
			output, err := e.OnCallback(context.Background(), asyncState(), payload)
			if err != nil {
				t.Fatalf("OnCallback() error = %v, want nil", err)
			}
			if output.Cost != nil {
				t.Fatalf("output cost = %+v, want nil: the Provider reported no usable charge", *output.Cost)
			}
			if _, ok := output.Ports["image"]; !ok {
				t.Fatalf("output ports = %v, want the image port regardless of usage", output.Ports)
			}
		})
	}
}

// TestImageGenerationNode_PollSucceeded_CostEqualsOnCallbackCost: the poll path reads the
// same terminal payload and must record the same Cost the callback path records.
func TestImageGenerationNode_PollSucceeded_CostEqualsOnCallbackCost(t *testing.T) {
	payload := `{"status":"SUCCEEDED","image":` + succeededImage + `,"usage":{"cost":0.04}}`
	poller := &stubPoller{status: registry.PollSucceeded, payload: json.RawMessage(payload)}
	e := pollingExecutor(poller)

	fromCallback, err := e.OnCallback(context.Background(), asyncState(), []byte(payload))
	if err != nil {
		t.Fatalf("OnCallback() error = %v", err)
	}
	fromPoll, err := e.Poll(context.Background(), asyncState())
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if fromPoll.Output == nil || fromPoll.Output.Cost == nil || fromCallback.Cost == nil {
		t.Fatalf("poll output = %+v, callback cost = %+v, want both to carry the Cost", fromPoll.Output, fromCallback.Cost)
	}
	if diff := cmp.Diff(fromCallback.Cost, fromPoll.Output.Cost); diff != "" {
		t.Fatalf("poll cost differs from callback cost (-callback +poll):\n%s", diff)
	}
}
