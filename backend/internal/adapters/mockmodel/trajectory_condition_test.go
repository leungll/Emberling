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

// The bounded tool message the Runtime hands the delivery Agent when the process running
// its deploy call stopped. The deployment does not count toward the generation limit.
const uncertainDeployMessage = `{"status":"RESULT_UNCERTAIN","message":"The deploy call was interrupted. It does not count toward the generation limit."}`

const deliveryTestResult = `{"passed":true,"baseCommit":"fixture-v1","patchDigest":"sha256:p","resultDigest":"sha256:r","summary":{"total":12,"failed":0}}`

// TestProvider_Generate_UncertainDeployment_InsertsFinalReportWithoutSecondDeploy plays the
// deploy-lost trajectory: the deployment's outcome is reported uncertain and the script
// answers with its inserted FINAL, a report naming the tested patch, instead of calling
// deploy again or reading a deployment that never returned.
func TestProvider_Generate_UncertainDeployment_InsertsFinalReportWithoutSecondDeploy(t *testing.T) {
	const script = "software-delivery-deploy-lost"

	final, err := NewProvider().Generate(context.Background(), scriptedDeliveryRequest(t, script, deliveryTestResult, uncertainDeployMessage))
	if err != nil {
		t.Fatalf("Generate() after the uncertain deployment error = %v", err)
	}
	var text string
	if final.Decision.Kind != registry.DecisionFinal || json.Unmarshal(final.Decision.Output, &text) != nil {
		t.Fatalf("final Decision = %+v, want FINAL with a JSON string output", final.Decision)
	}
	want := `{"baseCommit":"fixture-v1",` +
		`"message":"The deploy call was interrupted and its outcome is unknown. It was not sent again; check production for this patch before deploying it again.",` +
		`"patchDigest":"sha256:p","status":"DEPLOYMENT_UNCERTAIN"}`
	if text != want {
		t.Fatalf("FINAL text =\n%s\nwant\n%s", text, want)
	}
}

// TestProvider_Generate_UncertainDeploymentTwice_FailsExplicitly bounds the inserted FINAL
// like an inserted Tool call: RESULT_UNCERTAIN arriving a second time fails the Turn.
func TestProvider_Generate_UncertainDeploymentTwice_FailsExplicitly(t *testing.T) {
	_, err := NewProvider().Generate(context.Background(),
		scriptedDeliveryRequest(t, "software-delivery-deploy-lost", deliveryTestResult, uncertainDeployMessage, uncertainDeployMessage))

	if err == nil || !strings.Contains(err.Error(), `script "software-delivery-deploy-lost": tool status RESULT_UNCERTAIN arrived 2 times`) {
		t.Fatalf("Generate() error = %v, want the second RESULT_UNCERTAIN refused", err)
	}
}

// scriptedDeliveryRequest builds a request for a software-delivery script with the
// delivery task and the given tool results, oldest first.
func scriptedDeliveryRequest(t *testing.T, script string, toolResults ...string) registry.ModelRequest {
	t.Helper()
	request := scriptedRequest(t, script, toolResults...)
	request.Messages[0] = userMessage(t, deliveryTask)
	return request
}

func TestParseTrajectory_MalformedCondition_Rejected(t *testing.T) {
	const turns = `"turns":[{"final":{"output":1}}]`
	cases := map[string]string{
		"no status":         `{` + turns + `,"onToolStatus":[{"status":"","toolCall":{"toolName":"t","arguments":{}}}]}`,
		"no decision":       `{` + turns + `,"onToolStatus":[{"status":"RESULT_UNCERTAIN"}]}`,
		"tool without name": `{` + turns + `,"onToolStatus":[{"status":"RESULT_UNCERTAIN","toolCall":{"toolName":"","arguments":{}}}]}`,
		"duplicate status": `{` + turns + `,"onToolStatus":[` +
			`{"status":"RESULT_UNCERTAIN","toolCall":{"toolName":"t","arguments":{}}},` +
			`{"status":"RESULT_UNCERTAIN","toolCall":{"toolName":"u","arguments":{}}}]}`,
		"tool call and final":     `{` + turns + `,"onToolStatus":[{"status":"RESULT_UNCERTAIN","toolCall":{"toolName":"t","arguments":{}},"final":{"output":1}}]}`,
		"final without output":    `{` + turns + `,"onToolStatus":[{"status":"RESULT_UNCERTAIN","final":{}}]}`,
		"final with both outputs": `{` + turns + `,"onToolStatus":[{"status":"RESULT_UNCERTAIN","final":{"output":1,"outputText":{}}}]}`,
		"unknown member":          `{` + turns + `,"onToolStatus":[{"status":"RESULT_UNCERTAIN","toolCall":{"toolName":"t","arguments":{}},"retry":true}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseTrajectory([]byte(raw)); err == nil {
				t.Fatal("parseTrajectory() error = nil, want rejection")
			}
		})
	}
}
