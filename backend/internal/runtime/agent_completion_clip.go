package runtime

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// This file owns the completion check of a DELIVER_CLIP task: the pure decision whether
// the clip the Agent declared was rendered, passed its checks and, when the Agent could
// review, passed its review, or whether the Agent declared with a reason that the request
// cannot be satisfied. EvaluateCompletion dispatches here by goal.

// Check names of the clip delivery check.
const (
	CheckClipDeclared        = "clip_delivery_declared"
	CheckCannotSatisfyReason = "cannot_satisfy_reason_given"
	CheckClipRendered        = "declared_clip_rendered"
	CheckClipChecked         = "declared_clip_check_passed"
	CheckContentReviewed     = "timeline_content_review_passed"
)

// Missing codes of an INCOMPLETE clip delivery.
const (
	MissingDeliveryDeclaration = "DELIVERY_NOT_DECLARED"
	MissingCannotSatisfyReason = "CANNOT_SATISFY_REASON_MISSING"
	MissingClipRendered        = "CLIP_NOT_RENDERED"
	MissingClipCheckPassed     = "CLIP_CHECK_NOT_PASSED"
	MissingContentReviewPassed = "CONTENT_REVIEW_NOT_PASSED"
)

// The outcomes a clip delivery Agent declares in its final output.
const (
	ClipReportDelivered     = "DELIVERED"
	ClipReportCannotSatisfy = "CANNOT_SATISFY"
)

// ClipReport is what the check reads from the Agent's final output: the declared outcome,
// the clip digest a DELIVERED report names and the reason a CANNOT_SATISFY report gives.
// The declaration only decides which facts are checked; it is never itself evidence.
type ClipReport struct {
	Outcome             string
	ClipSha256          string
	CannotSatisfyReason string
}

// ParseClipReport reads the clip delivery report from a FINAL output. The second result is
// false when the output is not a JSON object carrying a string outcome; an absent or null
// clipSha256 or cannotSatisfyReason reads as empty.
func ParseClipReport(output json.RawMessage) (ClipReport, bool) {
	if len(bytes.TrimSpace(output)) == 0 {
		return ClipReport{}, false
	}
	var raw struct {
		Outcome             *string `json:"outcome"`
		ClipSha256          *string `json:"clipSha256"`
		CannotSatisfyReason *string `json:"cannotSatisfyReason"`
	}
	if err := json.Unmarshal(output, &raw); err != nil || raw.Outcome == nil {
		return ClipReport{}, false
	}
	report := ClipReport{Outcome: *raw.Outcome}
	if raw.ClipSha256 != nil {
		report.ClipSha256 = *raw.ClipSha256
	}
	if raw.CannotSatisfyReason != nil {
		report.CannotSatisfyReason = *raw.CannotSatisfyReason
	}
	return report, true
}

// evaluateClipDelivery decides a DELIVER_CLIP task. The Agent must have declared
// completion itself; any other termination is INCOMPLETE. A CANNOT_SATISFY declaration
// needs a reason and no facts: the NodeRun succeeds without a clip, and whether the
// refusal was justified is for evaluation against the annotated material, not for the
// Runtime. A DELIVERED declaration is checked entirely against committed facts: a
// clip_rendered fact about the declared digest, the newest clip check about it passed,
// and, when the Agent could review, the newest content review of the timeline that clip
// was rendered from passed. Every other combination is INCOMPLETE and Missing names what
// the facts lack.
func evaluateClipDelivery(in CompletionInput) CompletionResult {
	e := &completionEvaluator{allPassed: true}
	incomplete := func() CompletionResult {
		return finishCompletion(domain.CompletionIncomplete, e)
	}

	if in.Termination != domain.TerminationFinalResponse {
		e.fail(CheckFinalResponse, MissingFinalResponse,
			"The agent did not declare completion; it ended with "+string(in.Termination)+".", nil)
		return incomplete()
	}
	e.pass(CheckFinalResponse, nil)

	report, ok := ParseClipReport(in.FinalOutput)
	switch {
	case !ok || (report.Outcome != ClipReportDelivered && report.Outcome != ClipReportCannotSatisfy):
		e.fail(CheckClipDeclared, MissingDeliveryDeclaration,
			"The agent's final output declares neither DELIVERED nor CANNOT_SATISFY.", nil)
		return incomplete()
	case report.Outcome == ClipReportCannotSatisfy:
		e.pass(CheckClipDeclared, nil)
		if strings.TrimSpace(report.CannotSatisfyReason) == "" {
			e.fail(CheckCannotSatisfyReason, MissingCannotSatisfyReason,
				"The agent declared that the request cannot be satisfied but gave no reason.", nil)
			return incomplete()
		}
		e.pass(CheckCannotSatisfyReason, nil)
		return finishCompletion(domain.CompletionCannotSatisfy, e)
	case report.ClipSha256 == "":
		e.fail(CheckClipDeclared, MissingDeliveryDeclaration,
			"The agent declared DELIVERED without naming the clip digest.", nil)
		return incomplete()
	}
	e.pass(CheckClipDeclared, nil)

	render := latestFactAbout(in.ClipRenders, report.ClipSha256, nil)
	if render == nil {
		e.fail(CheckClipRendered, MissingClipRendered,
			"No render is recorded for the declared clip digest.", nil)
		return incomplete()
	}
	e.pass(CheckClipRendered, []string{render.ID})
	e.cite(domain.CompletionEvidenceFact, render.ID)
	timeline := render.Binding[domain.ClipRenderedBindingTimeline]

	clipKind := map[string]string{domain.MediaCheckedBindingKind: domain.MediaCheckedKindClip}
	switch check := latestFactAbout(in.MediaChecks, report.ClipSha256, clipKind); {
	case check == nil:
		e.fail(CheckClipChecked, MissingClipCheckPassed, "The declared clip was never checked.", nil)
	case check.Verdict == nil || !*check.Verdict:
		e.fail(CheckClipChecked, MissingClipCheckPassed, "The newest check of the declared clip did not pass.", []string{check.ID})
		e.cite(domain.CompletionEvidenceFact, check.ID)
	default:
		e.pass(CheckClipChecked, []string{check.ID})
		e.cite(domain.CompletionEvidenceFact, check.ID)
	}

	if in.ContentReviewRequired {
		var review *CompletionFact
		if timeline != "" {
			review = latestFactAbout(in.ContentReviews, timeline, nil)
		}
		switch {
		case review == nil:
			e.fail(CheckContentReviewed, MissingContentReviewPassed,
				"No content review is recorded for the timeline the declared clip was rendered from.", nil)
		case review.Verdict == nil || !*review.Verdict:
			e.fail(CheckContentReviewed, MissingContentReviewPassed,
				"The newest content review of the timeline the declared clip was rendered from did not pass.", []string{review.ID})
			e.cite(domain.CompletionEvidenceFact, review.ID)
		default:
			e.pass(CheckContentReviewed, []string{review.ID})
			e.cite(domain.CompletionEvidenceFact, review.ID)
		}
	}

	if e.allPassed {
		return finishCompletion(domain.CompletionClipDelivered, e)
	}
	return incomplete()
}

// latestFactAbout returns the newest fact about subject whose bindings carry every value
// in bindings, or nil. Facts are passed in recorded order, oldest first, so the newest
// is the last match: a later check of the same subject overrides an earlier one.
func latestFactAbout(facts []CompletionFact, subject string, bindings map[string]string) *CompletionFact {
	var latest *CompletionFact
	for i := range facts {
		fact := &facts[i]
		if fact.Subject != subject {
			continue
		}
		matches := true
		for name, value := range bindings {
			if fact.Binding[name] != value {
				matches = false
				break
			}
		}
		if matches {
			latest = fact
		}
	}
	return latest
}

// finishCompletion renders the evaluator's checks, missing items and evidence as the
// result of outcome, with empty slices rather than nil so the persisted and projected
// JSON always carries arrays. A successful outcome lists nothing as missing.
func finishCompletion(outcome domain.CompletionOutcome, e *completionEvaluator) CompletionResult {
	result := CompletionResult{Outcome: outcome, Checks: e.checks, Missing: e.missing, Evidence: e.evidence}
	if outcome.IsSuccess() || result.Missing == nil {
		result.Missing = []domain.CompletionMissing{}
	}
	if result.Checks == nil {
		result.Checks = []domain.CompletionCheck{}
	}
	if result.Evidence == nil {
		result.Evidence = []domain.CompletionEvidence{}
	}
	return result
}
