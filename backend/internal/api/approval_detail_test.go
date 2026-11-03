package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

func approvalViewForTest() service.ApprovalView {
	return service.ApprovalView{
		Approval: domain.Approval{
			ID: "apr_1", ActionID: "act_1", RunID: "run_1", AgentRunID: "ar_1", Status: domain.ApprovalPending,
			RequestedAt: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC), Deadline: time.Date(2026, 11, 2, 11, 0, 0, 0, time.UTC),
		},
		ToolName: "deploy_change", Arguments: json.RawMessage(`{"changeRequestId":"cr_1"}`), WorkflowID: "wf_1", NodeRunID: "nr_1",
	}
}

func TestApprovalDetailDTO_NoChangeRequest_KeepsTheViewAndNullsBothMembers(t *testing.T) {
	body, err := json.Marshal(toApprovalDetailDTO(service.ApprovalDetail{ApprovalView: approvalViewForTest()}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"id":"apr_1"`, `"toolName":"deploy_change"`, `"arguments":{"changeRequestId":"cr_1"}`, `"nodeRunId":"nr_1"`, `"changeRequest":null`, `"patch":null`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("detail %s lacks %s", body, want)
		}
	}
}

func TestApprovalDetailDTO_ChangeRequest_CarriesContentBasisAndPatchText(t *testing.T) {
	omitted := domain.ManifestReferenceOnly
	detail := service.ApprovalDetail{
		ApprovalView: approvalViewForTest(),
		ChangeRequest: &service.ApprovalChangeRequest{
			Request: domain.ChangeRequest{
				ID: "cr_1", AgentRunID: "ar_1", PatchDigest: strings.Repeat("b", 64), VerificationFactID: "fact_1",
				ContentArtifactID: "artifact_1", ContentDigest: strings.Repeat("c", 64), ToolAttemptID: "ta_1",
				CreatedAt: time.Date(2026, 11, 2, 9, 59, 0, 0, time.UTC),
			},
			Content: "# Change request\n\n## Summary\n\nUpgrade.\n",
			Basis: []service.ApprovalDecisionBasis{
				{
					ChangeRequestBasis: domain.ChangeRequestBasis{Statement: "The rules allow it.", TurnNo: 2, ManifestRef: "RULES.md"},
					TurnID:             "turn_2",
					Item: &domain.ManifestItem{Kind: domain.ManifestWorkspaceFile, Label: "RULES.md", Ref: "RULES.md", Version: "0",
						SHA256: strings.Repeat("d", 64), Length: 12, Included: true},
				},
				{
					ChangeRequestBasis: domain.ChangeRequestBasis{Statement: "Verified.", TurnNo: 9, ManifestRef: "ta_gone"},
					Item:               &domain.ManifestItem{Kind: domain.ManifestToolResult, Label: "verify_patch result", Ref: "ta_gone", Included: false, OmittedReason: &omitted},
				},
				{ChangeRequestBasis: domain.ChangeRequestBasis{Statement: "Unresolved.", TurnNo: 10, ManifestRef: "nothing"}},
			},
		},
		Patch: &domain.WorkspacePatch{
			ID: "wp_3", AgentRunID: "ar_1", Seq: 3, Patch: "diff --git a/go.mod b/go.mod\n", PatchDigest: strings.Repeat("b", 64),
			TouchedPaths: []string{"go.mod"}, ToolAttemptID: "ta_write", CreatedAt: time.Date(2026, 11, 2, 9, 58, 0, 0, time.UTC),
		},
	}
	body, err := json.Marshal(toApprovalDetailDTO(detail))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wantChangeRequest := `"changeRequest":{"id":"cr_1","patchDigest":"` + strings.Repeat("b", 64) + `","verificationFactId":"fact_1",` +
		`"contentArtifactId":"artifact_1","contentDigest":"` + strings.Repeat("c", 64) + `","toolAttemptId":"ta_1","createdAt":"2026-11-02T09:59:00Z",` +
		`"content":"# Change request\n\n## Summary\n\nUpgrade.\n","decisionBasis":[` +
		`{"statement":"The rules allow it.","turnNo":2,"turnId":"turn_2","manifestRef":"RULES.md","item":{"kind":"WORKSPACE_FILE","label":"RULES.md","ref":"RULES.md","version":"0","sha256":"` + strings.Repeat("d", 64) + `","length":12,"included":true,"omittedReason":null}},` +
		`{"statement":"Verified.","turnNo":9,"turnId":"","manifestRef":"ta_gone","item":{"kind":"TOOL_RESULT","label":"verify_patch result","ref":"ta_gone","version":"","sha256":"","length":0,"included":false,"omittedReason":"REFERENCE_ONLY"}},` +
		`{"statement":"Unresolved.","turnNo":10,"turnId":"","manifestRef":"nothing","item":null}]}`
	wantPatch := `"patch":{"seq":3,"patchDigest":"` + strings.Repeat("b", 64) + `","touchedPaths":["go.mod"],"toolAttemptId":"ta_write","createdAt":"2026-11-02T09:58:00Z","patch":"diff --git a/go.mod b/go.mod\n"}`
	for _, want := range []string{wantChangeRequest, wantPatch} {
		if !strings.Contains(string(body), strings.ReplaceAll(want, "\n", `\n`)) {
			t.Errorf("detail %s\nlacks %s", body, want)
		}
	}
	if strings.Contains(string(body), "wp_3") {
		t.Errorf("detail %s exposes the patch row id, which the trace never shows either", body)
	}
}
