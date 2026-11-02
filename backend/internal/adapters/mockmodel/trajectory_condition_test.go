package mockmodel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/registry"
)

// The bounded tool messages the Runtime hands an Agent in place of a result when the
// process running its Tool call stopped. Only the status member is read by a script.
const (
	uncertainMessage   = `{"status":"RESULT_UNCERTAIN","message":"The generate_image call was interrupted.","generationCalls":{"used":1,"limit":12}}`
	interruptedMessage = `{"status":"EXECUTION_INTERRUPTED","message":"The review_asset call was interrupted."}`
)

// generateScriptedTurn requests the next Turn of script after toolResults.
func generateScriptedTurn(t *testing.T, script string, toolResults ...string) (registry.ModelResponse, error) {
	t.Helper()
	return NewProvider().Generate(context.Background(), scriptedRequest(t, script, toolResults...))
}

func visionReviewArgs(assetRef, photo string) string {
	return `{"assetRef":"` + assetRef + `","photoAssetId":"` + photo + `","styleRequirement":"Warm film look"}`
}

// TestProvider_Generate_UncertainGeneration_InsertsRegenerationThenResumesScript plays the
// sync-crash trajectory: the first generation's outcome is reported uncertain, the script
// inserts one regeneration, and every later Turn and the FINAL read the regenerated image
// as if the interrupted call had never been part of the script.
func TestProvider_Generate_UncertainGeneration_InsertsRegenerationThenResumesScript(t *testing.T) {
	const script, digest = "photo-set-sync-crash", "sha256:first"
	results := []string{}
	step := func(wantTool, wantArguments, result string) {
		t.Helper()
		response, err := generateScriptedTurn(t, script, results...)
		if err != nil {
			t.Fatalf("turn %d Generate() error = %v", len(results), err)
		}
		assertToolCall(t, response, wantTool, wantArguments)
		results = append(results, result)
	}

	step("generate_image", generateArgs("asset_photo_1", firstRoundPrompt), uncertainMessage)
	step("generate_image", generateArgs("asset_photo_1", firstRoundPrompt), imageResult("img_r1", digest))
	step("review_asset", reviewArgs("img_r1", "pass", "asset_photo_1"), reviewResult(true))
	step("generate_image", generateArgs("asset_photo_2", firstRoundPrompt), imageResult("img_a2", digest))
	step("review_asset", reviewArgs("img_a2", "pass", "asset_photo_2"), reviewResult(true))
	step("generate_image", generateArgs("asset_photo_3", firstRoundPrompt), imageResult("img_a3", digest))
	step("review_asset", reviewArgs("img_a3", "pass", "asset_photo_3"), reviewResult(true))

	final, err := generateScriptedTurn(t, script, results...)
	if err != nil {
		t.Fatalf("final Generate() error = %v", err)
	}
	var text string
	if final.Decision.Kind != registry.DecisionFinal || json.Unmarshal(final.Decision.Output, &text) != nil {
		t.Fatalf("final Decision = %+v, want FINAL with a JSON string output", final.Decision)
	}
	wantText := `{"examples":[` +
		`{"assetRef":"img_r1","photoAssetId":"asset_photo_1"},` +
		`{"assetRef":"img_a2","photoAssetId":"asset_photo_2"},` +
		`{"assetRef":"img_a3","photoAssetId":"asset_photo_3"}],` +
		`"settings":{"prompt":"` + firstRoundPrompt + `"},` +
		`"settingsDigest":"sha256:first"}`
	if text != wantText {
		t.Fatalf("FINAL text =\n%s\nwant\n%s", text, wantText)
	}
}

// TestProvider_Generate_InterruptedReview_InsertsSameReviewThenFinishes plays the review
// trajectory: the review is reported interrupted, the script calls the same review again,
// and its FINAL names the image the uninterrupted review checked.
func TestProvider_Generate_InterruptedReview_InsertsSameReviewThenFinishes(t *testing.T) {
	const script = "photo-review-interrupted"
	generated := imageResult("img_a1", "sha256:first")

	retried, err := generateScriptedTurn(t, script, generated, interruptedMessage)
	if err != nil {
		t.Fatalf("Generate() after the interruption error = %v", err)
	}
	assertToolCall(t, retried, "review_asset", visionReviewArgs("img_a1", "asset_photo_1"))

	final, err := generateScriptedTurn(t, script, generated, interruptedMessage, reviewResult(true))
	if err != nil {
		t.Fatalf("final Generate() error = %v", err)
	}
	var text string
	if final.Decision.Kind != registry.DecisionFinal || json.Unmarshal(final.Decision.Output, &text) != nil {
		t.Fatalf("final Decision = %+v, want FINAL with a JSON string output", final.Decision)
	}
	if want := `{"examples":[{"assetRef":"img_a1","photoAssetId":"asset_photo_1"}],"settings":{"prompt":"` + firstRoundPrompt + `"},"settingsDigest":"sha256:first"}`; text != want {
		t.Fatalf("FINAL text =\n%s\nwant\n%s", text, want)
	}
}

// TestProvider_Generate_ConditionalDecision_SecondMatchFailsExplicitly bounds the inserted
// Decision: the same status arriving a second time fails the Turn instead of looping.
func TestProvider_Generate_ConditionalDecision_SecondMatchFailsExplicitly(t *testing.T) {
	_, err := generateScriptedTurn(t, "photo-set-sync-crash", uncertainMessage, uncertainMessage)

	if err == nil || !strings.Contains(err.Error(), `script "photo-set-sync-crash": tool status RESULT_UNCERTAIN arrived 2 times`) {
		t.Fatalf("Generate() error = %v, want the second RESULT_UNCERTAIN refused", err)
	}
}

// TestProvider_Generate_StatusWithoutCondition_IsAnOrdinaryResult keeps conditions opt-in: a
// trajectory that declares none reads an interruption message as an ordinary Tool result,
// so its next placeholder fails explicitly rather than guessing.
func TestProvider_Generate_StatusWithoutCondition_IsAnOrdinaryResult(t *testing.T) {
	_, err := generateScriptedTurn(t, "photo-set", uncertainMessage)

	if err == nil || !strings.Contains(err.Error(), "placeholder {{tool:0:/assetRef}}") {
		t.Fatalf("Generate() error = %v, want turn 1 to read the message as tool result 0", err)
	}
}

// TestProvider_Generate_ConditionalStatusOnEarlierMessage_DoesNotInsertAgain shows the
// inserted Decision answers only the latest tool message: once a later result arrives, the
// script continues from its own Turns.
func TestProvider_Generate_ConditionalStatusOnEarlierMessage_DoesNotInsertAgain(t *testing.T) {
	response, err := generateScriptedTurn(t, "photo-review-interrupted",
		imageResult("img_a1", "sha256:first"), interruptedMessage, reviewResult(false))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.Decision.Kind != registry.DecisionFinal {
		t.Fatalf("Decision = %+v, want the script's FINAL", response.Decision)
	}
}

func TestParseTrajectory_MalformedCondition_Rejected(t *testing.T) {
	const turns = `"turns":[{"final":{"output":1}}]`
	cases := map[string]string{
		"no status":         `{` + turns + `,"onToolStatus":[{"status":"","toolCall":{"toolName":"t","arguments":{}}}]}`,
		"no tool call":      `{` + turns + `,"onToolStatus":[{"status":"RESULT_UNCERTAIN"}]}`,
		"tool without name": `{` + turns + `,"onToolStatus":[{"status":"RESULT_UNCERTAIN","toolCall":{"toolName":"","arguments":{}}}]}`,
		"duplicate status": `{` + turns + `,"onToolStatus":[` +
			`{"status":"RESULT_UNCERTAIN","toolCall":{"toolName":"t","arguments":{}}},` +
			`{"status":"RESULT_UNCERTAIN","toolCall":{"toolName":"u","arguments":{}}}]}`,
		"unknown member": `{` + turns + `,"onToolStatus":[{"status":"RESULT_UNCERTAIN","toolCall":{"toolName":"t","arguments":{}},"final":{}}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseTrajectory([]byte(raw)); err == nil {
				t.Fatal("parseTrajectory() error = nil, want rejection")
			}
		})
	}
}
