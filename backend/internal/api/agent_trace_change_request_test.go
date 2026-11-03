package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

func TestAgentTraceResponse_NoChangeRequests_RendersEmptyList(t *testing.T) {
	body, err := json.Marshal(toAgentTraceResponse(service.AgentTrace{}, defaultTraceMaxFieldBytes))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"changeRequests":{"items":[],"truncated":false}`) {
		t.Errorf("response %s lacks an empty change request list", body)
	}
}

func TestAgentTraceResponse_ChangeRequests_RendersReferencesWithoutBasisOrContent(t *testing.T) {
	zone := time.FixedZone("UTC+8", 8*3600)
	trace := service.AgentTrace{ChangeRequests: service.AgentTraceChangeRequests{
		Requests: []domain.ChangeRequest{{
			ID: "cr_1", AgentRunID: "ar_1", PatchDigest: strings.Repeat("b", 64), VerificationFactID: "fact_1",
			ContentArtifactID: "artifact_" + strings.Repeat("c", 32), ContentDigest: strings.Repeat("c", 64),
			DecisionBasis: json.RawMessage(`[{"statement":"secret statement line","turnNo":2,"manifestRef":"tool_attempt_1"}]`),
			ToolAttemptID: "ta_2", CreatedAt: time.Date(2026, 11, 2, 20, 0, 0, 0, zone),
		}},
		Truncated: true,
	}}
	body, err := json.Marshal(toAgentTraceResponse(trace, defaultTraceMaxFieldBytes))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `"changeRequests":{"items":[{"id":"cr_1","patchDigest":"` + strings.Repeat("b", 64) + `","verificationFactId":"fact_1",` +
		`"contentArtifactId":"artifact_` + strings.Repeat("c", 32) + `","contentDigest":"` + strings.Repeat("c", 64) + `",` +
		`"toolAttemptId":"ta_2","createdAt":"2026-11-02T12:00:00Z"}],"truncated":true}`
	if !strings.Contains(string(body), want) {
		t.Errorf("response %s\nlacks %s", body, want)
	}
	if strings.Contains(string(body), "secret statement line") || strings.Contains(string(body), "decisionBasis") {
		t.Errorf("response carries the decision basis: %s", body)
	}
}
