package mockmodel

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// reviewRequest builds the request the review Tool sends: the fixed instructions and one
// user message holding the requirement and the excerpt, with no script configured.
func reviewRequest(excerpt string) registry.ModelRequest {
	return registry.ModelRequest{
		ModelID:      ReviewModelID,
		Instructions: "review",
		Messages: []registry.ModelMessage{{
			Role:  "user",
			Parts: []registry.ContentPart{{Type: registry.ContentPartText, Text: "Policy: review_v1\nRequirement: a short clip\nSegment 1: S24 to S24\n\n--- Transcript excerpt (\">\" selected, \"  \" context) ---\n" + excerpt}},
		}},
	}
}

type reviewAnswer struct {
	Passed bool `json:"passed"`
	Issues []struct {
		Type      string   `json:"type"`
		Sentences []string `json:"sentences"`
		Reason    string   `json:"reason"`
	} `json:"issues"`
}

func decodeReview(t *testing.T, response registry.ModelResponse) reviewAnswer {
	t.Helper()
	var answer reviewAnswer
	if response.Decision.Kind != registry.DecisionFinal || json.Unmarshal(response.Decision.Output, &answer) != nil {
		t.Fatalf("Decision = %+v, want a FINAL opinion object", response.Decision)
	}
	return answer
}

func TestProvider_Models_RegistersTextReviewV1WithStructuredDecision(t *testing.T) {
	models, err := NewProvider().Models(context.Background())
	if err != nil {
		t.Fatalf("Models() error = %v", err)
	}
	var found bool
	for _, model := range models {
		if model.ID != ReviewModelID {
			continue
		}
		found = true
		if !model.HasCapability(domain.ModelCapabilityStructuredDecision) {
			t.Fatalf("%s capabilities = %v, want structured decision", ReviewModelID, model.Capabilities)
		}
		if model.ID == ModelID {
			t.Fatalf("the review model repeats the decision model's id %s", ModelID)
		}
	}
	if !found {
		t.Fatalf("Models() does not list %s", ReviewModelID)
	}
}

// TestProvider_Generate_ReviewModel_CutOffSentenceFailsWithTheSentenceToAdd covers the
// mock review policy on the shape the demo transcript has: a selected sentence followed by
// an unselected context sentence that begins in lowercase was cut off mid-thought.
func TestProvider_Generate_ReviewModel_CutOffSentenceFailsWithTheSentenceToAdd(t *testing.T) {
	excerpt := "  S23\tIt's hard to have your voice heard as a woman and to be respected.\n" +
		"> S24\tA lot of engineers are very introverted, and we don't like the spotlight,\n" +
		"  S25\tand we kind of like to help behind the scenes, but we always like to know that we're helping someone.\n"
	response, err := NewProvider().Generate(context.Background(), reviewRequest(excerpt))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	answer := decodeReview(t, response)
	if answer.Passed || len(answer.Issues) != 1 {
		t.Fatalf("answer = %+v, want one issue and passed=false", answer)
	}
	issue := answer.Issues[0]
	if issue.Type != "MEANING_DISTORTED" || len(issue.Sentences) != 1 || issue.Sentences[0] != "S24" || issue.Reason == "" {
		t.Fatalf("issue = %+v, want MEANING_DISTORTED about S24 with a reason", issue)
	}
}

// TestProvider_Generate_ReviewModel_SelectionWithTheContinuationPasses covers the
// revision the policy asks for: once the continuing sentence is selected too, the same
// excerpt shape passes with no issues.
func TestProvider_Generate_ReviewModel_SelectionWithTheContinuationPasses(t *testing.T) {
	excerpt := "  S23\tIt's hard to have your voice heard as a woman and to be respected.\n" +
		"> S24\tA lot of engineers are very introverted, and we don't like the spotlight,\n" +
		"> S25\tand we kind of like to help behind the scenes, but we always like to know that we're helping someone.\n"
	response, err := NewProvider().Generate(context.Background(), reviewRequest(excerpt))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if answer := decodeReview(t, response); !answer.Passed || len(answer.Issues) != 0 {
		t.Fatalf("answer = %+v, want passed with no issues", answer)
	}
}

// TestProvider_Generate_ReviewModel_GapAndUppercaseContextAreNotContinuations bounds the
// policy: a context sentence after a gap marker or one that begins in uppercase does not
// continue the selected sentence, so those selections pass.
func TestProvider_Generate_ReviewModel_GapAndUppercaseContextAreNotContinuations(t *testing.T) {
	excerpt := "> S10\tWe knew that was our goal, was to graduate as an engineer.\n" +
		"  S11\tI talked to someone who was an engineer.\n" +
		"  [...]\n" +
		"> S24\tA lot of engineers are very introverted, and we don't like the spotlight,\n" +
		"  [...]\n" +
		"  S25\tand we kind of like to help behind the scenes.\n"
	response, err := NewProvider().Generate(context.Background(), reviewRequest(excerpt))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if answer := decodeReview(t, response); !answer.Passed || len(answer.Issues) != 0 {
		t.Fatalf("answer = %+v, want passed with no issues", answer)
	}
}

// TestProvider_Generate_ReviewModel_ScriptHookStillOverrides keeps the in-process Script
// hook first, so a test can script any opinion for the review model as before.
func TestProvider_Generate_ReviewModel_ScriptHookStillOverrides(t *testing.T) {
	p := NewProvider()
	p.Script = func(registry.ModelRequest) *Scenario {
		return &Scenario{Kind: ScenarioFinal, FinalOutput: json.RawMessage(`{"passed":false,"issues":[]}`)}
	}
	response, err := p.Generate(context.Background(), reviewRequest("> S25\tand so on.\n"))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if answer := decodeReview(t, response); answer.Passed {
		t.Fatalf("answer = %+v, want the scripted failure", answer)
	}
}

// TestProvider_Generate_ReviewModel_SameExcerptSameVerdict proves the verdict is a
// function of the request alone and carries the fixture's zero cost.
func TestProvider_Generate_ReviewModel_SameExcerptSameVerdict(t *testing.T) {
	excerpt := "> S24\tA lot of engineers are very introverted,\n  S25\tand we kind of like to help.\n"
	p := NewProvider()
	first, err := p.Generate(context.Background(), reviewRequest(excerpt))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	second, err := p.Generate(context.Background(), reviewRequest(excerpt))
	if err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}
	if string(first.Decision.Output) != string(second.Decision.Output) {
		t.Fatalf("verdicts differ: %s vs %s", first.Decision.Output, second.Decision.Output)
	}
	if first.Cost == nil || first.Cost.Amount != "0" || first.Cost.Source != ReviewModelID {
		t.Fatalf("cost = %+v, want a known zero charged to %s", first.Cost, ReviewModelID)
	}
}
