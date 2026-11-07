package mockmodel

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ReviewModelID is the stable Model ID of this Provider's deterministic text review
// model. A Tool that requires an independent review model, one other than the Agent's
// decision model, can select it on a deployment that registers only the mock models, so
// a reviewed Definition runs end to end without a real model and without cost.
const ReviewModelID = "text-review-v1"

// The excerpt a review request carries marks each line with one of these prefixes: a
// selected sentence, a context sentence, or a gap between two parts of the excerpt. The
// prefixes are the review Tool's rendering, which this fixture reads back.
const (
	excerptHeaderPrefix = "--- Transcript excerpt"
	selectedLinePrefix  = "> "
	contextLinePrefix   = "  "
	excerptGapLine      = "  [...]"
)

// maxReviewIssues bounds the issues the mock reviewer reports, as the review Tool
// repeats at most three for the Agent.
const maxReviewIssues = 3

// reviewOpinion is the FINAL output the mock reviewer answers with: the review Tool's
// opinion shape, passed with no issues or failed with the issues found.
type reviewOpinion struct {
	Passed bool          `json:"passed"`
	Issues []reviewIssue `json:"issues"`
}

type reviewIssue struct {
	Type      string   `json:"type"`
	Sentences []string `json:"sentences"`
	Reason    string   `json:"reason"`
}

// excerptLine is one parsed line of the excerpt: a sentence id, its text and whether the
// Agent selected it. A gap line is not an excerptLine.
type excerptLine struct {
	id       string
	text     string
	selected bool
}

// reviewScenario answers a review request from the request's content alone, under the
// mock review policy: a selected sentence whose next sentence in the transcript is shown
// as unselected context and begins with a lowercase letter was cut off mid-thought, since
// the transcript's sentence split starts a new sentence in lowercase only after a pause
// inside one spoken sentence. Each such selection is one MEANING_DISTORTED issue naming
// the sentence to add; a selection without one passes. The policy reads no requirement
// and judges nothing else, so a scripted Agent can make a review fail and then pass by
// changing which sentences it selects, and the same excerpt always gets the same verdict.
func reviewScenario(text string) (Scenario, error) {
	lines := parseExcerpt(text)
	opinion := reviewOpinion{Passed: true, Issues: []reviewIssue{}}
	for i := 0; i+1 < len(lines) && len(opinion.Issues) < maxReviewIssues; i++ {
		current, next := lines[i], lines[i+1]
		if !current.selected || next.selected || !startsLowercase(next.text) {
			continue
		}
		opinion.Passed = false
		opinion.Issues = append(opinion.Issues, reviewIssue{
			Type:      "MEANING_DISTORTED",
			Sentences: []string{current.id},
			Reason: fmt.Sprintf("%s stops where the speaker continues the thought in %s; add %s to the selection or leave %s out",
				current.id, next.id, next.id, current.id),
		})
	}
	output, err := json.Marshal(opinion)
	if err != nil {
		return Scenario{}, fmt.Errorf("encode review opinion: %w", err)
	}
	return Scenario{Kind: ScenarioFinal, FinalOutput: output}, nil
}

// parseExcerpt reads the excerpt lines after the excerpt header. A gap line breaks the
// adjacency the policy inspects, so it is kept as a separator rather than dropped.
func parseExcerpt(text string) []excerptLine {
	var lines []excerptLine
	inExcerpt := false
	for _, raw := range strings.Split(text, "\n") {
		if !inExcerpt {
			inExcerpt = strings.HasPrefix(raw, excerptHeaderPrefix)
			continue
		}
		if raw == excerptGapLine {
			// A sentence after a gap is not the next sentence of the one before it.
			lines = append(lines, excerptLine{})
			continue
		}
		var selected bool
		switch {
		case strings.HasPrefix(raw, selectedLinePrefix):
			selected = true
		case strings.HasPrefix(raw, contextLinePrefix):
		default:
			continue
		}
		id, sentence, ok := strings.Cut(raw[len(selectedLinePrefix):], "\t")
		if !ok || id == "" {
			continue
		}
		lines = append(lines, excerptLine{id: id, text: sentence, selected: selected})
	}
	return lines
}

// startsLowercase reports whether the first letter of text is lowercase: the mark of a
// sentence the transcript split off the middle of a spoken sentence.
func startsLowercase(text string) bool {
	r, size := utf8.DecodeRuneInString(strings.TrimSpace(text))
	return size > 0 && unicode.IsLower(r)
}
