package openaimodel_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/domain"
)

const finalContent = `{"kind":"FINAL","tool":null,"arguments":null,"output":"ok","statePatch":null}`

// TestProvider_Generate_RequestBody_AsksForUsageAccounting: every request carries
// usage.include=true, so a Provider that can report the call's charge does.
func TestProvider_Generate_RequestBody_AsksForUsageAccounting(t *testing.T) {
	server, seen := fakeProvider(t, http.StatusOK, completion(t, finalContent, ""))
	provider := newProvider(t, server.URL, server.Client())

	if _, err := provider.Generate(context.Background(), agentRequest()); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	var body struct {
		Usage *struct {
			Include *bool `json:"include"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(seen.body, &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if body.Usage == nil || body.Usage.Include == nil || !*body.Usage.Include {
		t.Fatalf("request usage = %+v, want {\"include\": true}", body.Usage)
	}
}

// TestProvider_Generate_UsageCost_ReturnsCostInUSDForThisModel: a reported usage.cost is
// normalised to a Cost amount in US dollars attributed to the configured Model ID, beside
// the token counts.
func TestProvider_Generate_UsageCost_ReturnsCostInUSDForThisModel(t *testing.T) {
	usage := `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"cost":0.0000315}`
	server, _ := fakeProvider(t, http.StatusOK, completion(t, finalContent, usage))
	provider := newProvider(t, server.URL, server.Client())

	response, err := provider.Generate(context.Background(), agentRequest())
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	want := &domain.Cost{Amount: "0.000032", Currency: domain.CurrencyUSD, Source: testModelID}
	if diff := cmp.Diff(want, response.Cost); diff != "" {
		t.Fatalf("response cost (-want +got):\n%s", diff)
	}
	if response.TokenUsage == nil || response.TokenUsage.TotalTokens != 15 {
		t.Fatalf("TokenUsage = %+v, want the reported counts beside the cost", response.TokenUsage)
	}
}

// TestProvider_Generate_NoUsageCost_ReturnsNilCostNotZero: a Provider that reports no
// cost member, no usage at all, or a cost that is not a non-negative number leaves Cost
// nil. The absence is a persisted fact distinct from a reported zero.
func TestProvider_Generate_NoUsageCost_ReturnsNilCostNotZero(t *testing.T) {
	cases := map[string]string{
		"no usage":      "",
		"tokens only":   `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}`,
		"null cost":     `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"cost":null}`,
		"string cost":   `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"cost":"0.01"}`,
		"negative cost": `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"cost":-0.01}`,
	}
	for name, usage := range cases {
		t.Run(name, func(t *testing.T) {
			server, _ := fakeProvider(t, http.StatusOK, completion(t, finalContent, usage))
			provider := newProvider(t, server.URL, server.Client())

			response, err := provider.Generate(context.Background(), agentRequest())
			if err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			if response.Cost != nil {
				t.Fatalf("Cost = %+v, want nil when the Provider reports no usable charge", *response.Cost)
			}
		})
	}
}

// TestProvider_Generate_ZeroUsageCost_IsAKnownZero: a reported 0 is a Cost with amount
// "0", not an absence.
func TestProvider_Generate_ZeroUsageCost_IsAKnownZero(t *testing.T) {
	usage := `{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"cost":0}`
	server, _ := fakeProvider(t, http.StatusOK, completion(t, finalContent, usage))
	provider := newProvider(t, server.URL, server.Client())

	response, err := provider.Generate(context.Background(), agentRequest())
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.Cost == nil || response.Cost.Amount != "0" {
		t.Fatalf("Cost = %+v, want amount \"0\"", response.Cost)
	}
}
