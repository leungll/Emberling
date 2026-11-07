package reviewartifact

import (
	"fmt"
	"strings"
)

// This file owns the review model's side of the Tool: the fixed instructions and the
// user message built from the requirement, the policy and the excerpt.

// PolicyVersion names the review standard a verdict was reached under. The standard is
// the fixed instructions below together with how the excerpt is built; a change to either
// changes what a verdict answered, so the version moves with them and is recorded in
// every fact. It is never configurable per call.
const PolicyVersion = "review_v1"

// maxExcerptBytes bounds the excerpt the model reads. With at most maxTimelineSegments
// ranges over a bounded transcript it is rarely approached; a call past it fails rather
// than reviewing part of a selection.
const maxExcerptBytes = 64 << 10

// reviewInstructions are the fixed instructions every review is asked with. They ask two
// things of the selection: whether it satisfies the requirement, and whether any selected
// sentence, standing alone in the clip, distorts what the speaker meant.
const reviewInstructions = `You are an independent content reviewer. Another model selected sentences of a transcribed talk to cut into a short clip; you did not select them and you must judge the selection on its own merits.

You receive the user's requirement for the clip and an excerpt of the transcript. Lines starting with ">" are the selected sentences, in the order the clip plays them; lines starting with two spaces are the sentences spoken just before or after a selection, shown only so you know the original context. A "[...]" line marks sentences left out between two parts of the excerpt. Sentences are identified by their ids, such as S12.

Judge two things:
1. Requirement: do the selected sentences, as a clip, satisfy the user's requirement? A clip that is about something else, or that leaves out what the requirement asks for, does not.
2. Meaning: does any selected sentence, standing alone in the clip without its neighbours, distort what the speaker meant? Cutting a sentence off from the condition, negation, question or contrast that follows or precedes it, or quoting a view the speaker then rejects, distorts the meaning.

Answer FINAL with "passed" and "issues". "passed" is true only when the clip satisfies the requirement and no selected sentence distorts the original meaning. Each issue has a "type" of "REQUIREMENT_NOT_MET" or "MEANING_DISTORTED", the "sentences" it concerns as a list of ids from the excerpt, and a concise "reason" naming the problem and what would fix it, such as the sentence that must be added or removed. When passed is false, report at least one issue and at most three, the most important first; when passed is true, report none. Judge only what you were given; do not call tools and do not invent what the speaker did not say.`

// userMessage renders what the review model reads: the policy version, the requirement,
// the selected ranges, and the excerpt with selected sentences marked and gaps between
// windows marked. Times are not shown; the review judges words.
func userMessage(requirement string, segments []timelineSegment, lines []excerptLine) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Policy: %s\nRequirement: %s\n", PolicyVersion, requirement)
	for i, segment := range segments {
		fmt.Fprintf(&b, "Segment %d: %s to %s\n", i+1, segment.From, segment.To)
	}
	b.WriteString("\n--- Transcript excerpt (\">\" selected, \"  \" context) ---\n")
	previous := -1
	for _, line := range lines {
		if previous >= 0 && line.Index > previous+1 {
			b.WriteString("  [...]\n")
		}
		previous = line.Index
		marker := "  "
		if line.Selected {
			marker = "> "
		}
		fmt.Fprintf(&b, "%s%s\t%s\n", marker, line.ID, line.Text)
	}
	return b.String()
}
