package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

func TestDecisionBasisOfArguments_ReadsOnlyForToolsRecordingChangeRequests(t *testing.T) {
	arguments := json.RawMessage(`{"patchDigest":"d","decisionBasis":[{"statement":"s","turnNo":2,"manifestRef":"tool_attempt_1"}]}`)
	recording := domain.ToolMetadata{Name: "create_change_request", Records: []domain.ToolRecordKind{domain.ToolRecordChangeRequest}}
	basis, err := decisionBasisOfArguments(recording, arguments)
	if err != nil || len(basis) != 1 || basis[0].TurnNo != 2 || basis[0].ManifestRef != "tool_attempt_1" {
		t.Fatalf("basis = %+v, %v, want the one cited item", basis, err)
	}
	other := domain.ToolMetadata{Name: "write_file", Records: []domain.ToolRecordKind{domain.ToolRecordWorkspacePatch}}
	if basis, err := decisionBasisOfArguments(other, arguments); err != nil || basis != nil {
		t.Errorf("a Tool that records no change request: basis %+v err %v, want none", basis, err)
	}
	if basis, err := decisionBasisOfArguments(recording, json.RawMessage(`{"patchDigest":"d"}`)); err != nil || basis != nil {
		t.Errorf("arguments without a basis: basis %+v err %v, want none", basis, err)
	}
	var invalid *errDecisionBasisInvalid
	_, err = decisionBasisOfArguments(recording, json.RawMessage(`{"decisionBasis":[{"statement":"","turnNo":1,"manifestRef":"x"}]}`))
	if !errors.As(err, &invalid) {
		t.Errorf("a malformed basis: err %v, want errDecisionBasisInvalid", err)
	}
	if _, err := decisionBasisOfArguments(recording, json.RawMessage(`[]`)); !errors.As(err, &invalid) {
		t.Errorf("arguments that are not an object: err %v, want errDecisionBasisInvalid", err)
	}
}

func TestChangeRequestResultOutput_NamesTheRecordAndWhetherItExisted(t *testing.T) {
	output := json.RawMessage(`{"patchDigest":"d","basisCount":2}`)
	stored := domain.ChangeRequest{ID: "cr_1"}
	created, err := changeRequestResultOutput(output, stored, true)
	if err != nil {
		t.Fatalf("created: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(created, &fields); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if fields["changeRequestId"] != "cr_1" || fields["existing"] != false || fields["patchDigest"] != "d" || fields["basisCount"] != float64(2) {
		t.Errorf("created output = %v, want the original fields plus changeRequestId and existing=false", fields)
	}
	existing, err := changeRequestResultOutput(output, stored, false)
	if err != nil || !strings.Contains(string(existing), `"existing":true`) {
		t.Errorf("existing output = %s (%v), want existing=true", existing, err)
	}
	if _, err := changeRequestResultOutput(json.RawMessage(`"text"`), stored, true); err == nil {
		t.Error("a non-object output must be refused")
	}
}

func TestManifestHolds_MatchesTheItemReference(t *testing.T) {
	manifest := domain.AgentTurnManifest{Items: []domain.ManifestItem{
		{Kind: domain.ManifestInstructions, Ref: "ar_1"},
		{Kind: domain.ManifestToolResult, Ref: "tool_attempt_1", Version: "1"},
	}}
	if !manifestHolds(manifest, "tool_attempt_1") || !manifestHolds(manifest, "ar_1") {
		t.Error("existing references must resolve")
	}
	if manifestHolds(manifest, "tool_attempt_2") || manifestHolds(manifest, "") {
		t.Error("a reference the manifest does not hold must not resolve")
	}
}

func TestSameBasis_ComparesEveryItem(t *testing.T) {
	a := []domain.ChangeRequestBasis{{Statement: "s", TurnNo: 1, ManifestRef: "r"}}
	b := []domain.ChangeRequestBasis{{Statement: "s", TurnNo: 1, ManifestRef: "r"}}
	if !sameBasis(a, b) {
		t.Error("equal bases must compare equal")
	}
	b[0].TurnNo = 2
	if sameBasis(a, b) || sameBasis(a, nil) {
		t.Error("a differing or missing item must compare unequal")
	}
}

func TestReviewFactOfArguments_ReadsOnlyForToolsRecordingChangeRequests(t *testing.T) {
	recording := domain.ToolMetadata{Name: "create_change_request", Records: []domain.ToolRecordKind{domain.ToolRecordChangeRequest}}
	cited, err := reviewFactOfArguments(recording, json.RawMessage(`{"patchDigest":"d","reviewFactId":"fact_review_1"}`))
	if err != nil || cited != "fact_review_1" {
		t.Fatalf("cited = %q, %v, want fact_review_1", cited, err)
	}
	if cited, err := reviewFactOfArguments(recording, json.RawMessage(`{"patchDigest":"d"}`)); err != nil || cited != "" {
		t.Errorf("arguments without a citation: %q, %v, want none", cited, err)
	}
	if cited, err := reviewFactOfArguments(recording, json.RawMessage(`{"reviewFactId":null}`)); err != nil || cited != "" {
		t.Errorf("a null citation: %q, %v, want none", cited, err)
	}
	other := domain.ToolMetadata{Name: "write_file", Records: []domain.ToolRecordKind{domain.ToolRecordWorkspacePatch}}
	if cited, err := reviewFactOfArguments(other, json.RawMessage(`{"reviewFactId":"fact_review_1"}`)); err != nil || cited != "" {
		t.Errorf("a Tool that records no change request: %q, %v, want none", cited, err)
	}
	var invalid *errDecisionBasisInvalid
	if _, err := reviewFactOfArguments(recording, json.RawMessage(`{"reviewFactId":7}`)); !errors.As(err, &invalid) {
		t.Errorf("a non-string citation: err %v, want errDecisionBasisInvalid", err)
	}
	if _, err := reviewFactOfArguments(recording, json.RawMessage(`{"reviewFactId":""}`)); !errors.As(err, &invalid) {
		t.Errorf("an empty citation: err %v, want errDecisionBasisInvalid", err)
	}
	if _, err := reviewFactOfArguments(recording, json.RawMessage(`[]`)); !errors.As(err, &invalid) {
		t.Errorf("arguments that are not an object: err %v, want errDecisionBasisInvalid", err)
	}
}

func TestReviewFactMatchesPatch_RequiresTypeRunAndSubject(t *testing.T) {
	verdict := false
	fact := domain.ExecutionFact{ID: "fact_review_1", RunID: "run_1", FactType: reviewFactType, SubjectRef: "digest-a", Verdict: &verdict}
	if reason := reviewFactMismatch(fact, "run_1", "digest-a"); reason != "" {
		t.Errorf("matching fact rejected: %s", reason)
	}
	for name, tc := range map[string]struct {
		mutate func(*domain.ExecutionFact)
		want   string
	}{
		"other run":     {func(f *domain.ExecutionFact) { f.RunID = "run_2" }, "this run"},
		"other type":    {func(f *domain.ExecutionFact) { f.FactType = "patch_verified" }, reviewFactType},
		"other subject": {func(f *domain.ExecutionFact) { f.SubjectRef = "digest-b" }, "patch"},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := fact
			tc.mutate(&candidate)
			if reason := reviewFactMismatch(candidate, "run_1", "digest-a"); reason == "" || !strings.Contains(reason, tc.want) {
				t.Errorf("reason = %q, want a mismatch mentioning %q", reason, tc.want)
			}
		})
	}
}
