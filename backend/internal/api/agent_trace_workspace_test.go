package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

func TestAgentTraceResponse_NoWorkspace_RendersNull(t *testing.T) {
	body, err := json.Marshal(toAgentTraceResponse(service.AgentTrace{}, defaultTraceMaxFieldBytes))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"workspace":null`) {
		t.Errorf("response %s lacks a null workspace", body)
	}
}

func TestAgentTraceResponse_Workspace_RendersBindingAndPatchSummariesWithoutText(t *testing.T) {
	zone := time.FixedZone("UTC+8", 8*3600)
	trace := service.AgentTrace{Workspace: &service.AgentTraceWorkspace{
		Workspace: domain.AgentWorkspace{
			AgentRunID: "ar_1", RepositoryID: "vuln-demo", BaseCommit: strings.Repeat("a", 40),
			AllowedPaths: []string{"go.mod", "internal/auth"},
			DeployTarget: &domain.WorkspaceDeployTarget{Service: "orders-api", Environment: "staging"},
		},
		Patches: []domain.WorkspacePatch{{
			ID: "wpatch_1", AgentRunID: "ar_1", Seq: 1, Patch: "diff --git a/go.mod b/go.mod\n+secret content line\n",
			PatchDigest: strings.Repeat("b", 64), TouchedPaths: []string{"go.mod"}, ToolAttemptID: "ta_1",
			CreatedAt: time.Date(2026, 11, 2, 20, 0, 0, 0, zone),
		}},
		PatchesTruncated: true,
	}}
	body, err := json.Marshal(toAgentTraceResponse(trace, defaultTraceMaxFieldBytes))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `"workspace":{"repositoryId":"vuln-demo","baseCommit":"` + strings.Repeat("a", 40) + `",` +
		`"allowedPaths":["go.mod","internal/auth"],"deployTarget":{"service":"orders-api","environment":"staging"},` +
		`"patches":[{"seq":1,"patchDigest":"` + strings.Repeat("b", 64) + `","touchedPaths":["go.mod"],"toolAttemptId":"ta_1",` +
		`"createdAt":"2026-11-02T12:00:00Z"}],"patchesTruncated":true}`
	if !strings.Contains(string(body), want) {
		t.Errorf("response %s\nlacks %s", body, want)
	}
	if strings.Contains(string(body), "secret content line") || strings.Contains(string(body), "diff --git") {
		t.Errorf("response carries the patch text: %s", body)
	}
}
