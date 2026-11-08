package reviewartifact

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/registry"
)

// This file owns the opinion the review model must answer with and the bounds applied
// to what the Agent reads of it.

// Issue types the review reports: the clip does not satisfy the requirement, or a
// selected sentence standing alone distorts the original meaning.
const (
	IssueRequirementNotMet = "REQUIREMENT_NOT_MET"
	IssueMeaningDistorted  = "MEANING_DISTORTED"
)

// Opinion bounds. The result repeats at most maxIssues issues for the Agent, each with
// bounded sentence ids and reason; the opinion itself is refused past maxOpinionBytes
// whatever the model writes.
const (
	maxOpinionBytes     = 16 << 10
	maxIssues           = 3
	maxIssueSentences   = 20
	maxSentenceIDRunes  = 32
	maxReasonRunes      = 300
	maxRequirementRunes = 2000
)

// opinionSchema is the review model's FINAL output Schema. It stays inside the strict
// structured output subset, so the count and length bounds are applied after the answer.
const opinionSchema = `{
  "type": "object",
  "properties": {
    "passed": {"type": "boolean"},
    "issues": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "type": {"type": "string", "enum": ["REQUIREMENT_NOT_MET", "MEANING_DISTORTED"]},
          "sentences": {"type": "array", "items": {"type": "string"}},
          "reason": {"type": "string"}
        },
        "required": ["type", "sentences", "reason"],
        "additionalProperties": false
      }
    }
  },
  "required": ["passed", "issues"],
  "additionalProperties": false
}`

// opinion is the review model's decoded answer.
type opinion struct {
	Passed bool    `json:"passed"`
	Issues []issue `json:"issues"`
}

// issue is one problem the review model reported. Field order is the result's key order.
type issue struct {
	Type      string   `json:"type"`
	Sentences []string `json:"sentences"`
	Reason    string   `json:"reason"`
}

// decodeOpinion accepts only a FINAL whose output satisfies the opinion Schema within
// the opinion bound. Any other answer is an unusable review: the caller fails the call,
// and nothing of the answer is quoted in the error.
func decodeOpinion(decision registry.ModelDecision) (opinion, error) {
	if decision.Kind != registry.DecisionFinal {
		return opinion{}, errors.New("answer is not a FINAL opinion")
	}
	if len(decision.Output) > maxOpinionBytes {
		return opinion{}, fmt.Errorf("opinion exceeds %d bytes", maxOpinionBytes)
	}
	var value any
	if err := json.Unmarshal(decision.Output, &value); err != nil {
		return opinion{}, errors.New("opinion is not JSON")
	}
	schema, err := registry.CompileSchema(json.RawMessage(opinionSchema))
	if err != nil {
		return opinion{}, fmt.Errorf("compile opinion schema: %w", err)
	}
	if err := registry.ValidateValue(schema, value); err != nil {
		return opinion{}, errors.New("opinion does not match the review opinion schema")
	}
	var answer opinion
	if err := json.Unmarshal(decision.Output, &answer); err != nil {
		return opinion{}, errors.New("opinion does not match the review opinion schema")
	}
	return answer, nil
}

// boundedIssues keeps the first maxIssues issues, each field trimmed, made valid UTF-8
// and cut to its bound. The result is never nil, so the Agent always reads a list.
func boundedIssues(issues []issue) []issue {
	out := make([]issue, 0, min(len(issues), maxIssues))
	for _, item := range issues {
		if len(out) == maxIssues {
			break
		}
		ids := make([]string, 0, min(len(item.Sentences), maxIssueSentences))
		for _, id := range item.Sentences {
			if len(ids) == maxIssueSentences {
				break
			}
			ids = append(ids, truncateRunes(strings.TrimSpace(strings.ToValidUTF8(id, "")), maxSentenceIDRunes))
		}
		out = append(out, issue{
			Type:      item.Type,
			Sentences: ids,
			Reason:    truncateRunes(strings.TrimSpace(strings.ToValidUTF8(item.Reason, "")), maxReasonRunes),
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
