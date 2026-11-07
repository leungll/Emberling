package runtime

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

const (
	clipSha     = "3333333333333333333333333333333333333333333333333333333333333333"
	otherSha    = "4444444444444444444444444444444444444444444444444444444444444444"
	timelineSha = "5555555555555555555555555555555555555555555555555555555555555555"
)

func deliveredReport(sha string) json.RawMessage {
	return json.RawMessage(`{"outcome":"DELIVERED","clipSha256":"` + sha + `","selectionReason":"states the claim","cannotSatisfyReason":null}`)
}

func rendered(id, clip, timeline string) CompletionFact {
	return CompletionFact{ID: id, Subject: clip, Binding: map[string]string{domain.ClipRenderedBindingTimeline: timeline}}
}

func checked(id, subject, kind string, passed bool) CompletionFact {
	return CompletionFact{ID: id, Subject: subject, Verdict: boolPtr(passed), Binding: map[string]string{domain.MediaCheckedBindingKind: kind, "ruleVersion": "v1"}}
}

func reviewed(id, timeline string, passed bool) CompletionFact {
	return CompletionFact{ID: id, Subject: timeline, Verdict: boolPtr(passed), Binding: map[string]string{"policyVersion": "p1"}}
}

func clipInput(output json.RawMessage) CompletionInput {
	return CompletionInput{Goal: domain.AgentGoalDeliverClip, Termination: domain.TerminationFinalResponse, FinalOutput: output}
}

func checkNamed(t *testing.T, result CompletionResult, name string) domain.CompletionCheck {
	t.Helper()
	for _, check := range result.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("checks %+v lack %s", result.Checks, name)
	return domain.CompletionCheck{}
}

func TestEvaluateCompletion_DeliverClip_DeclarationAndFactCombinations(t *testing.T) {
	delivered := deliveredReport(clipSha)
	render := rendered("fact_render", clipSha, timelineSha)
	tests := []struct {
		name        string
		in          CompletionInput
		wantOutcome domain.CompletionOutcome
		wantMissing []string
	}{
		{
			name:        "ended by the turn bound, never declared",
			in:          CompletionInput{Goal: domain.AgentGoalDeliverClip, Termination: domain.TerminationMaxTurns, ClipRenders: []CompletionFact{render}, MediaChecks: []CompletionFact{checked("c", clipSha, "CLIP", true)}},
			wantOutcome: domain.CompletionIncomplete, wantMissing: []string{MissingFinalResponse},
		},
		{
			name:        "ended by the deadline, never declared",
			in:          CompletionInput{Goal: domain.AgentGoalDeliverClip, Termination: domain.TerminationTimeout},
			wantOutcome: domain.CompletionIncomplete, wantMissing: []string{MissingFinalResponse},
		},
		{
			name:        "final output is a text answer, not a report",
			in:          clipInput(json.RawMessage(`"done"`)),
			wantOutcome: domain.CompletionIncomplete, wantMissing: []string{MissingDeliveryDeclaration},
		},
		{
			name:        "final output declares an unknown outcome",
			in:          clipInput(json.RawMessage(`{"outcome":"MAYBE","clipSha256":null,"selectionReason":"","cannotSatisfyReason":null}`)),
			wantOutcome: domain.CompletionIncomplete, wantMissing: []string{MissingDeliveryDeclaration},
		},
		{
			name:        "cannot satisfy with a reason",
			in:          clipInput(json.RawMessage(`{"outcome":"CANNOT_SATISFY","clipSha256":null,"selectionReason":"","cannotSatisfyReason":"no sentence states a contested claim"}`)),
			wantOutcome: domain.CompletionCannotSatisfy,
		},
		{
			name:        "cannot satisfy without a reason",
			in:          clipInput(json.RawMessage(`{"outcome":"CANNOT_SATISFY","clipSha256":null,"selectionReason":"","cannotSatisfyReason":"  "}`)),
			wantOutcome: domain.CompletionIncomplete, wantMissing: []string{MissingCannotSatisfyReason},
		},
		{
			name:        "delivered without a clip digest",
			in:          clipInput(json.RawMessage(`{"outcome":"DELIVERED","clipSha256":null,"selectionReason":"","cannotSatisfyReason":null}`)),
			wantOutcome: domain.CompletionIncomplete, wantMissing: []string{MissingDeliveryDeclaration},
		},
		{
			name: "declared digest is not a rendered clip",
			in: func() CompletionInput {
				in := clipInput(deliveredReport(otherSha))
				in.ClipRenders = []CompletionFact{render}
				in.MediaChecks = []CompletionFact{checked("c", otherSha, "CLIP", true)}
				return in
			}(),
			wantOutcome: domain.CompletionIncomplete, wantMissing: []string{MissingClipRendered},
		},
		{
			name: "declared clip rendered but never checked",
			in: func() CompletionInput {
				in := clipInput(delivered)
				in.ClipRenders = []CompletionFact{render}
				return in
			}(),
			wantOutcome: domain.CompletionIncomplete, wantMissing: []string{MissingClipCheckPassed},
		},
		{
			name: "only the timeline was checked, not the clip",
			in: func() CompletionInput {
				in := clipInput(delivered)
				in.ClipRenders = []CompletionFact{render}
				in.MediaChecks = []CompletionFact{checked("t", clipSha, "TIMELINE", true)}
				return in
			}(),
			wantOutcome: domain.CompletionIncomplete, wantMissing: []string{MissingClipCheckPassed},
		},
		{
			name: "clip check passed then failed: the newest verdict decides",
			in: func() CompletionInput {
				in := clipInput(delivered)
				in.ClipRenders = []CompletionFact{render}
				in.MediaChecks = []CompletionFact{checked("c1", clipSha, "CLIP", true), checked("c2", clipSha, "CLIP", false)}
				return in
			}(),
			wantOutcome: domain.CompletionIncomplete, wantMissing: []string{MissingClipCheckPassed},
		},
		{
			name: "clip check failed then passed: delivered",
			in: func() CompletionInput {
				in := clipInput(delivered)
				in.ClipRenders = []CompletionFact{render}
				in.MediaChecks = []CompletionFact{checked("c1", clipSha, "CLIP", false), checked("c2", clipSha, "CLIP", true)}
				return in
			}(),
			wantOutcome: domain.CompletionClipDelivered,
		},
		{
			name: "review not required: a failed review is not a condition",
			in: func() CompletionInput {
				in := clipInput(delivered)
				in.ClipRenders = []CompletionFact{render}
				in.MediaChecks = []CompletionFact{checked("c", clipSha, "CLIP", true)}
				in.ContentReviews = []CompletionFact{reviewed("r", timelineSha, false)}
				return in
			}(),
			wantOutcome: domain.CompletionClipDelivered,
		},
		{
			name: "review required but none recorded for the clip's timeline",
			in: func() CompletionInput {
				in := clipInput(delivered)
				in.ContentReviewRequired = true
				in.ClipRenders = []CompletionFact{render}
				in.MediaChecks = []CompletionFact{checked("c", clipSha, "CLIP", true)}
				in.ContentReviews = []CompletionFact{reviewed("r", otherSha, true)}
				return in
			}(),
			wantOutcome: domain.CompletionIncomplete, wantMissing: []string{MissingContentReviewPassed},
		},
		{
			name: "review required and the newest review failed",
			in: func() CompletionInput {
				in := clipInput(delivered)
				in.ContentReviewRequired = true
				in.ClipRenders = []CompletionFact{render}
				in.MediaChecks = []CompletionFact{checked("c", clipSha, "CLIP", true)}
				in.ContentReviews = []CompletionFact{reviewed("r1", timelineSha, true), reviewed("r2", timelineSha, false)}
				return in
			}(),
			wantOutcome: domain.CompletionIncomplete, wantMissing: []string{MissingContentReviewPassed},
		},
		{
			name: "review required and passed: delivered",
			in: func() CompletionInput {
				in := clipInput(delivered)
				in.ContentReviewRequired = true
				in.ClipRenders = []CompletionFact{render}
				in.MediaChecks = []CompletionFact{checked("c", clipSha, "CLIP", true)}
				in.ContentReviews = []CompletionFact{reviewed("r1", timelineSha, false), reviewed("r2", timelineSha, true)}
				return in
			}(),
			wantOutcome: domain.CompletionClipDelivered,
		},
		{
			name: "clip unchecked and unreviewed: both named",
			in: func() CompletionInput {
				in := clipInput(delivered)
				in.ContentReviewRequired = true
				in.ClipRenders = []CompletionFact{render}
				return in
			}(),
			wantOutcome: domain.CompletionIncomplete, wantMissing: []string{MissingClipCheckPassed, MissingContentReviewPassed},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := EvaluateCompletion(tt.in)
			if result.Outcome != tt.wantOutcome {
				t.Fatalf("outcome = %s, want %s (checks %+v, missing %+v)", result.Outcome, tt.wantOutcome, result.Checks, result.Missing)
			}
			if got := missingCodes(result); !slices.Equal(got, tt.wantMissing) {
				t.Errorf("missing = %v, want %v", got, tt.wantMissing)
			}
			if result.Missing == nil || result.Checks == nil || result.Evidence == nil {
				t.Errorf("result carries a nil slice: %+v", result)
			}
			if result.Outcome.IsSuccess() && len(result.Missing) != 0 {
				t.Errorf("a successful outcome lists missing items: %+v", result.Missing)
			}
		})
	}
}

func TestEvaluateCompletion_DeliverClip_ChecksCiteTheFactsTheyRestOn(t *testing.T) {
	in := clipInput(deliveredReport(clipSha))
	in.ContentReviewRequired = true
	in.ClipRenders = []CompletionFact{rendered("fact_render_old", clipSha, otherSha), rendered("fact_render", clipSha, timelineSha)}
	in.MediaChecks = []CompletionFact{checked("fact_check_fail", clipSha, "CLIP", false), checked("fact_check_pass", clipSha, "CLIP", true)}
	in.ContentReviews = []CompletionFact{reviewed("fact_review", timelineSha, true)}
	result := EvaluateCompletion(in)
	if result.Outcome != domain.CompletionClipDelivered {
		t.Fatalf("outcome = %s, want CLIP_DELIVERED: %+v", result.Outcome, result.Missing)
	}
	wantRefs := map[string][]string{
		CheckFinalResponse:   {},
		CheckClipDeclared:    {},
		CheckClipRendered:    {"fact_render"},
		CheckClipChecked:     {"fact_check_pass"},
		CheckContentReviewed: {"fact_review"},
	}
	if len(result.Checks) != len(wantRefs) {
		t.Errorf("checks = %+v, want %d", result.Checks, len(wantRefs))
	}
	for name, refs := range wantRefs {
		check := checkNamed(t, result, name)
		if !check.Passed || !slices.Equal(check.Refs, refs) {
			t.Errorf("check %s = %+v, want passed with refs %v", name, check, refs)
		}
	}
	wantEvidence := []domain.CompletionEvidence{
		{Kind: domain.CompletionEvidenceFact, ID: "fact_render"},
		{Kind: domain.CompletionEvidenceFact, ID: "fact_check_pass"},
		{Kind: domain.CompletionEvidenceFact, ID: "fact_review"},
	}
	if !slices.Equal(result.Evidence, wantEvidence) {
		t.Errorf("evidence = %+v, want %+v", result.Evidence, wantEvidence)
	}
}

func TestEvaluateCompletion_DeliverClip_FailedCheckCitesTheVerdictItRejected(t *testing.T) {
	in := clipInput(deliveredReport(clipSha))
	in.ClipRenders = []CompletionFact{rendered("fact_render", clipSha, timelineSha)}
	in.MediaChecks = []CompletionFact{checked("fact_check_fail", clipSha, "CLIP", false)}
	result := EvaluateCompletion(in)
	if result.Outcome != domain.CompletionIncomplete {
		t.Fatalf("outcome = %s, want INCOMPLETE", result.Outcome)
	}
	check := checkNamed(t, result, CheckClipChecked)
	if check.Passed || !slices.Equal(check.Refs, []string{"fact_check_fail"}) {
		t.Errorf("clip check = %+v, want failed, citing the rejected verdict", check)
	}
	if len(result.Missing) != 1 || result.Missing[0].Code != MissingClipCheckPassed || result.Missing[0].Description == "" {
		t.Errorf("missing = %+v, want CLIP_CHECK_NOT_PASSED with a description", result.Missing)
	}
}

func TestParseClipReport_ReadsDeclarationAndRejectsNonObjects(t *testing.T) {
	report, ok := ParseClipReport(json.RawMessage(`{"outcome":"CANNOT_SATISFY","clipSha256":null,"selectionReason":"x","cannotSatisfyReason":"too long"}`))
	if !ok || report.Outcome != ClipReportCannotSatisfy || report.ClipSha256 != "" || report.CannotSatisfyReason != "too long" {
		t.Errorf("ParseClipReport() = %+v, %v", report, ok)
	}
	for _, raw := range []string{``, `"text"`, `[]`, `{}`, `{"outcome":1}`, `{"clipSha256":"abc"}`} {
		if _, ok := ParseClipReport(json.RawMessage(raw)); ok {
			t.Errorf("ParseClipReport(%s) accepted a value without a string outcome", raw)
		}
	}
}
