package reviewpatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// This file owns the review model's side of the Tool: the fixed instructions, the user
// message built from the workspace and the repository text, the opinion the model must
// answer with, and the bounds applied to what the Agent reads of it.

// reviewInstructions are the fixed instructions every review is asked with. A change to
// them changes what a verdict answered, so they are versioned by the Tool and not
// configurable per call.
const reviewInstructions = `You are an independent code reviewer. Another model prepared a patch for a service to fix a dependency vulnerability; you did not write it and you must judge it on its own merits.

You receive the repository's project rules, the security advisories the repository keeps, and the complete patch against the base commit as a unified diff. Review the patch for:
1. Correctness: the change compiles in intent, adapts every caller to the changed API, and does not alter behaviour the tests protect.
2. Completeness: the vulnerable dependency version is fully removed, the fixed version named by the advisory and permitted by the project rules is used, and nothing the advisory requires is left out.
3. Rule compliance: the change stays within the paths and practices the project rules allow, and touches nothing the rules forbid.
4. Risk: anything that could break the service at runtime or hide a regression.

Answer FINAL with "blocking", "findings" and "summary". "blocking" is true only when at least one finding must be fixed before the patch can be released. Each finding has a "severity" of "blocking", "major" or "minor", the repository "path" it concerns (or "" for the whole patch) and a concise "message" naming the problem and what would fix it. Report at most twelve findings, the most important first. The summary is one or two sentences for a human approver. Judge only what you were given; do not call tools and do not invent repository content you did not see.`

// severityBlocking is the finding severity the result repeats for the Agent; the opinion
// Schema enumerates it with "major" and "minor", which stay in the Artifact only.
const severityBlocking = "blocking"

// opinionSchema is the review model's FINAL output Schema. It stays inside the strict
// structured output subset, so the length bounds are applied after the answer.
const opinionSchema = `{
  "type": "object",
  "properties": {
    "blocking": {"type": "boolean"},
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "severity": {"type": "string", "enum": ["blocking", "major", "minor"]},
          "path": {"type": "string"},
          "message": {"type": "string"}
        },
        "required": ["severity", "path", "message"],
        "additionalProperties": false
      }
    },
    "summary": {"type": "string"}
  },
  "required": ["blocking", "findings", "summary"],
  "additionalProperties": false
}`

// opinion is the review model's decoded answer.
type opinion struct {
	Blocking bool      `json:"blocking"`
	Findings []finding `json:"findings"`
	Summary  string    `json:"summary"`
}

// finding is one problem the review model reported.
type finding struct {
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Message  string `json:"message"`
}

// userMessage renders what the review model reads: the workspace's identity, the
// repository text read beside the patch, and the patch itself. Repository files are
// quoted in full within their bound and labelled when cut.
func userMessage(view registry.WorkspaceView, files []ContextFile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository: %s\nBase commit: %s\nPatch digest: %s\n", view.RepositoryID, view.BaseCommit, view.PatchDigest)
	touched := domain.WorkspacePatchTouchedPaths(view.Patch)
	if len(touched) > 0 {
		fmt.Fprintf(&b, "Touched paths: %s\n", strings.Join(touched, ", "))
	}
	if len(files) == 0 {
		b.WriteString("\nThe repository keeps no rules file or advisories at the known locations.\n")
	}
	for _, file := range files {
		fmt.Fprintf(&b, "\n--- Repository file %s", file.Path)
		if file.Truncated {
			b.WriteString(" (truncated)")
		}
		b.WriteString(" ---\n")
		b.WriteString(file.Content)
		if !strings.HasSuffix(file.Content, "\n") {
			b.WriteString("\n")
		}
	}
	b.WriteString("\n--- Patch (unified diff against the base commit) ---\n")
	if view.Patch == "" {
		b.WriteString("(the workspace is unchanged: the patch is empty)\n")
	} else {
		b.WriteString(view.Patch)
		if !strings.HasSuffix(view.Patch, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// decodeOpinion accepts only a FINAL whose output satisfies the opinion Schema and stays
// within the opinion bound, and returns the decoded opinion with the output bytes the
// Artifact keeps. Any other answer is an unusable review: the caller fails the call, and
// nothing of the answer is quoted in the error.
func decodeOpinion(decision registry.ModelDecision) (opinion, json.RawMessage, error) {
	if decision.Kind != registry.DecisionFinal {
		return opinion{}, nil, errors.New("answer is not a FINAL opinion")
	}
	if len(decision.Output) > maxOpinionBytes {
		return opinion{}, nil, fmt.Errorf("opinion exceeds %d bytes", maxOpinionBytes)
	}
	var value any
	if err := json.Unmarshal(decision.Output, &value); err != nil {
		return opinion{}, nil, errors.New("opinion is not JSON")
	}
	schema, err := registry.CompileSchema(json.RawMessage(opinionSchema))
	if err != nil {
		return opinion{}, nil, fmt.Errorf("compile opinion schema: %w", err)
	}
	if err := registry.ValidateValue(schema, value); err != nil {
		return opinion{}, nil, errors.New("opinion does not match the review opinion schema")
	}
	var answer opinion
	if err := json.Unmarshal(decision.Output, &answer); err != nil {
		return opinion{}, nil, errors.New("opinion does not match the review opinion schema")
	}
	if answer.Findings == nil {
		answer.Findings = []finding{}
	}
	return answer, decision.Output, nil
}

// blockingFindings keeps the first maxBlockingFindings findings of blocking severity, each
// field trimmed, made valid UTF-8 and cut to its bound. The result is never nil, so the
// Agent always reads a list.
func blockingFindings(findings []finding) []finding {
	out := make([]finding, 0, maxBlockingFindings)
	for _, item := range findings {
		if len(out) == maxBlockingFindings {
			break
		}
		if strings.TrimSpace(item.Severity) != severityBlocking {
			continue
		}
		out = append(out, finding{
			Severity: severityBlocking,
			Path:     truncateRunes(strings.TrimSpace(strings.ToValidUTF8(item.Path, "")), maxFindingPathRunes),
			Message:  truncateRunes(strings.TrimSpace(strings.ToValidUTF8(item.Message, "")), maxFindingMessageRunes),
		})
	}
	return out
}

// truncateRunes cuts s to at most limit runes.
func truncateRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}
