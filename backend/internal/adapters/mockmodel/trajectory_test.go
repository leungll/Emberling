package mockmodel

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/registry"
)

const briefDocument = `{"brief":"make it cinematic","photos":[` +
	`{"mediaType":"image/jpeg","photoAssetId":"asset_photo_1","sha256":"abc","sizeBytes":10},` +
	`{"mediaType":"image/jpeg","photoAssetId":"asset_photo_2","sha256":"def","sizeBytes":11},` +
	`{"mediaType":"image/jpeg","photoAssetId":"asset_photo_3","sha256":"fed","sizeBytes":12}]}`

func scriptedRequest(t *testing.T, script string, toolResults ...string) registry.ModelRequest {
	t.Helper()
	messages := []registry.ModelMessage{userMessage(t, briefDocument)}
	for index, result := range toolResults {
		name := "tool"
		actionID := "act_" + string(rune('a'+index))
		messages = append(messages,
			registry.ModelMessage{Role: "assistant", Content: json.RawMessage(`{"kind":"TOOL_CALL"}`), ToolName: &name, ToolActionID: &actionID},
			registry.ModelMessage{Role: roleTool, Content: json.RawMessage(result), ToolName: &name, ToolActionID: &actionID},
		)
	}
	return registry.ModelRequest{
		ModelID:     ModelID,
		Messages:    messages,
		ModelConfig: json.RawMessage(`{"script":"` + script + `"}`),
	}
}

func assertToolCall(t *testing.T, response registry.ModelResponse, wantTool, wantArguments string) {
	t.Helper()
	if response.Decision.Kind != registry.DecisionToolCall || response.Decision.ToolName == nil {
		t.Fatalf("Decision = %+v, want TOOL_CALL", response.Decision)
	}
	if *response.Decision.ToolName != wantTool {
		t.Fatalf("ToolName = %q, want %q", *response.Decision.ToolName, wantTool)
	}
	if string(response.Decision.Arguments) != wantArguments {
		t.Fatalf("Arguments =\n%s\nwant\n%s", response.Decision.Arguments, wantArguments)
	}
}

// scriptedTurn is one expected Turn of a trajectory and the tool result the Runtime would
// feed back for it before the next Turn is requested.
type scriptedTurn struct {
	tool      string
	arguments string
	result    string
}

func imageResult(assetRef, digest string) string {
	return `{"assetRef":"` + assetRef + `","imageUrl":"http://mock/v1/assets/` + assetRef + `.png","settingsDigest":"` + digest + `"}`
}

func reviewResult(passed bool) string {
	if passed {
		return `{"passed":true,"policyVersion":"mock-review-policy-v1","reasons":[]}`
	}
	return `{"passed":false,"policyVersion":"mock-review-policy-v1","reasons":["mock rejection"]}`
}

// playTrajectory requests every scripted Turn in order, feeding back each Turn's tool result,
// and returns the decision of the Turn after the last scripted tool call.
func playTrajectory(t *testing.T, script string, turns []scriptedTurn) registry.ModelResponse {
	t.Helper()
	p := NewProvider()
	results := make([]string, 0, len(turns))
	for index, want := range turns {
		response, err := p.Generate(context.Background(), scriptedRequest(t, script, results...))
		if err != nil {
			t.Fatalf("turn %d Generate() error = %v", index, err)
		}
		assertToolCall(t, response, want.tool, want.arguments)
		results = append(results, want.result)
	}
	response, err := p.Generate(context.Background(), scriptedRequest(t, script, results...))
	if err != nil {
		t.Fatalf("turn %d Generate() error = %v", len(turns), err)
	}
	return response
}

func generateArgs(photo, strength string) string {
	return `{"photoAssetId":"` + photo + `","settings":{"strength":` + strength + `,"style":"film"}}`
}

func reviewArgs(assetRef, verdict, photo string) string {
	return `{"assetRef":"` + assetRef + `","mock":"` + verdict + `","photoAssetId":"` + photo + `"}`
}

func TestProvider_Generate_PhotoSetTrajectory_TwoRoundsThenVideoThenFinalExamples(t *testing.T) {
	const first, second = "sha256:first", "sha256:second"
	turns := []scriptedTurn{
		{"generate_image", generateArgs("asset_photo_1", "0.6"), imageResult("img_a1", first)},
		{"review_asset", reviewArgs("img_a1", "pass", "asset_photo_1"), reviewResult(true)},
		{"generate_image", generateArgs("asset_photo_2", "0.6"), imageResult("img_a2", first)},
		{"review_asset", reviewArgs("img_a2", "fail", "asset_photo_2"), reviewResult(false)},
		{"generate_image", generateArgs("asset_photo_3", "0.6"), imageResult("img_a3", first)},
		{"review_asset", reviewArgs("img_a3", "fail", "asset_photo_3"), reviewResult(false)},
		{"generate_image", generateArgs("asset_photo_1", "0.4"), imageResult("img_b1", second)},
		{"review_asset", reviewArgs("img_b1", "pass", "asset_photo_1"), reviewResult(true)},
		{"generate_image", generateArgs("asset_photo_2", "0.4"), imageResult("img_b2", second)},
		{"review_asset", reviewArgs("img_b2", "pass", "asset_photo_2"), reviewResult(true)},
		{"generate_image", generateArgs("asset_photo_3", "0.4"), imageResult("img_b3", second)},
		{"review_asset", reviewArgs("img_b3", "pass", "asset_photo_3"), reviewResult(true)},
		{"generate_video", `{"assetRef":"img_b1","photoAssetId":"asset_photo_1","settings":{"durationSeconds":4}}`, `{"videoUrl":"http://mock/v1/videos/vid_b1.mp4"}`},
	}

	final := playTrajectory(t, "photo-set", turns)

	if final.Decision.Kind != registry.DecisionFinal {
		t.Fatalf("turn %d Decision.Kind = %q, want FINAL", len(turns), final.Decision.Kind)
	}
	// The FINAL is one JSON string, as the Agent's text output requires, holding one
	// second-round example per photo and the settings digest those examples share: the
	// document the media result delivery check consumes.
	var text string
	if err := json.Unmarshal(final.Decision.Output, &text); err != nil {
		t.Fatalf("FINAL Output %s is not a JSON string: %v", final.Decision.Output, err)
	}
	wantText := `{"examples":[` +
		`{"assetRef":"img_b1","photoAssetId":"asset_photo_1"},` +
		`{"assetRef":"img_b2","photoAssetId":"asset_photo_2"},` +
		`{"assetRef":"img_b3","photoAssetId":"asset_photo_3"}],` +
		`"settingsDigest":"sha256:second"}`
	if text != wantText {
		t.Fatalf("FINAL text =\n%s\nwant\n%s", text, wantText)
	}
}

func TestProvider_Generate_PhotoSetSkipReviewTrajectory_DispatchesVideoForUnreviewedAsset(t *testing.T) {
	p := NewProvider()
	results := []string{imageResult("img_a1", "sha256:first")}

	response, err := p.Generate(context.Background(), scriptedRequest(t, "photo-set-skip-review", results...))

	if err != nil {
		t.Fatalf("turn 1 Generate() error = %v", err)
	}
	assertToolCall(t, response, "generate_video", `{"assetRef":"img_a1","photoAssetId":"asset_photo_1","settings":{"durationSeconds":4}}`)
}

func TestProvider_Generate_PhotoSetLimitTrajectory_RequestsAThirdGeneration(t *testing.T) {
	const digest = "sha256:first"
	turns := []scriptedTurn{
		{"generate_image", generateArgs("asset_photo_1", "0.6"), imageResult("img_a1", digest)},
		{"review_asset", reviewArgs("img_a1", "pass", "asset_photo_1"), reviewResult(true)},
		{"generate_image", generateArgs("asset_photo_2", "0.6"), imageResult("img_a2", digest)},
		{"review_asset", reviewArgs("img_a2", "pass", "asset_photo_2"), reviewResult(true)},
	}

	third := playTrajectory(t, "photo-set-limit", turns)

	// With a generation limit of two, the Runtime rejects this call at claim; the script
	// only has to ask for it.
	assertToolCall(t, third, "generate_image", generateArgs("asset_photo_3", "0.6"))
}

func TestProvider_Generate_ScriptedTrajectory_ReplayedTurnReturnsSameDecision(t *testing.T) {
	p := NewProvider()
	request := scriptedRequest(t, "photo-set", `{"assetRef":"img_0011","imageUrl":"u","settingsDigest":"d"}`)

	first, err := p.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}
	second, err := p.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}
	if string(first.Decision.Arguments) != string(second.Decision.Arguments) || *first.Decision.ToolName != *second.Decision.ToolName {
		t.Fatalf("replayed Turn differs: %+v vs %+v", first.Decision, second.Decision)
	}
}

func TestProvider_Generate_ScriptedTrajectory_FailsExplicitly(t *testing.T) {
	cases := []struct {
		name    string
		request func(t *testing.T) registry.ModelRequest
		wantErr string
	}{
		{
			name:    "unknown script",
			request: func(t *testing.T) registry.ModelRequest { return scriptedRequest(t, "no-such-script") },
			wantErr: `script "no-such-script" is not a known trajectory`,
		},
		{
			name: "placeholder misses the tool result member",
			request: func(t *testing.T) registry.ModelRequest {
				return scriptedRequest(t, "photo-set", `{"imageUrl":"u"}`)
			},
			wantErr: "placeholder {{tool:0:/assetRef}}",
		},
		{
			name: "turn beyond the script",
			request: func(t *testing.T) registry.ModelRequest {
				return scriptedRequest(t, "photo-set", slices.Repeat([]string{`{}`}, 14)...)
			},
			wantErr: `script "photo-set" has no turn 14`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewProvider().Generate(context.Background(), tc.request(t))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Generate() error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestProvider_Generate_ScriptDirective_SelectsTrajectory(t *testing.T) {
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "mock:script:photo-set")}}

	_, err := NewProvider().Generate(context.Background(), request)

	// The directive's own text is the task, so the first Turn's task placeholder cannot
	// resolve: the trajectory was selected and failed explicitly rather than falling back.
	if err == nil || !strings.Contains(err.Error(), `script "photo-set" turn 0`) {
		t.Fatalf("Generate() error = %v, want the photo-set trajectory's turn 0 to be attempted", err)
	}
}

const deliveryTask = `{"baseCommit":"fixture-v1","patch":"--- a/x\n+++ b/x\n",` +
	`"target":{"environment":"staging","service":"checkout"},"mock":"fail"}`

// playDeliveryTrajectory plays the named software-delivery trajectory against deliveryTask,
// feeding back a passing test result and a deployment, and returns the FINAL text.
func playDeliveryTrajectory(t *testing.T, script, wantTestArguments string) string {
	t.Helper()
	p := NewProvider()
	turns := []scriptedTurn{
		{"sandbox_test", wantTestArguments,
			`{"passed":true,"baseCommit":"fixture-v1","patchDigest":"sha256:p","resultDigest":"sha256:r","summary":{"total":12,"failed":0}}`},
		{"deploy",
			`{"baseCommit":"fixture-v1","parameters":{},"patchDigest":"sha256:p","target":{"environment":"staging","service":"checkout"}}`,
			`{"operationId":"op_act_b_1","version":"v1"}`},
	}
	results := make([]string, 0, len(turns))
	for index := 0; index <= len(turns); index++ {
		messages := []registry.ModelMessage{userMessage(t, deliveryTask)}
		for resultIndex, result := range results {
			name := turns[resultIndex].tool
			actionID := "act_" + string(rune('a'+resultIndex))
			messages = append(messages,
				registry.ModelMessage{Role: "assistant", Content: json.RawMessage(`{"kind":"TOOL_CALL"}`), ToolName: &name, ToolActionID: &actionID},
				registry.ModelMessage{Role: roleTool, Content: json.RawMessage(result), ToolName: &name, ToolActionID: &actionID},
			)
		}
		response, err := p.Generate(context.Background(), registry.ModelRequest{
			ModelID: ModelID, Messages: messages, ModelConfig: json.RawMessage(`{"script":"` + script + `"}`),
		})
		if err != nil {
			t.Fatalf("turn %d Generate() error = %v", index, err)
		}
		if index == len(turns) {
			if response.Decision.Kind != registry.DecisionFinal {
				t.Fatalf("turn %d Decision.Kind = %q, want FINAL", index, response.Decision.Kind)
			}
			var text string
			if err := json.Unmarshal(response.Decision.Output, &text); err != nil {
				t.Fatalf("FINAL Output %s is not a JSON string: %v", response.Decision.Output, err)
			}
			return text
		}
		assertToolCall(t, response, turns[index].tool, turns[index].arguments)
		results = append(results, turns[index].result)
	}
	return ""
}

// The task's own mock member never reaches a Tool call: the test mode is the trajectory's
// choice, and the deployment carries only the target, the tested commit and patch digest.
func TestProvider_Generate_SoftwareDeliveryTrajectories_TestThenDeployThenFinal(t *testing.T) {
	const wantFinal = `{"operationId":"op_act_b_1","patchDigest":"sha256:p","version":"v1"}`
	cases := map[string]string{
		"software-delivery":                    `{"baseCommit":"fixture-v1","patch":"--- a/x\n+++ b/x\n"}`,
		"software-delivery-duplicate-callback": `{"baseCommit":"fixture-v1","mock":"duplicate","patch":"--- a/x\n+++ b/x\n"}`,
	}
	for script, wantTestArguments := range cases {
		t.Run(script, func(t *testing.T) {
			if got := playDeliveryTrajectory(t, script, wantTestArguments); got != wantFinal {
				t.Fatalf("FINAL text =\n%s\nwant\n%s", got, wantFinal)
			}
		})
	}
}

func TestTrajectories_EmbeddedFixtures_LoadAndAppearInTextModelConfigSchema(t *testing.T) {
	names := TrajectoryNames()
	for _, want := range []string{
		"photo-set", "photo-set-limit", "photo-set-skip-review",
		"software-delivery", "software-delivery-duplicate-callback",
	} {
		if !slices.Contains(names, want) {
			t.Fatalf("TrajectoryNames() = %v, want to contain %q", names, want)
		}
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(textConfigSchema, &schema); err != nil {
		t.Fatalf("text config schema is not JSON: %v", err)
	}
	if !slices.Equal(schema.Properties["script"].Enum, names) {
		t.Fatalf("script enum = %v, want %v", schema.Properties["script"].Enum, names)
	}
}

func TestParseTrajectory_MalformedFixture_Rejected(t *testing.T) {
	cases := map[string]string{
		"no turns":                `{"turns":[]}`,
		"both shapes":             `{"turns":[{"toolCall":{"toolName":"t","arguments":{}},"final":{"output":1}}]}`,
		"last not final":          `{"turns":[{"toolCall":{"toolName":"t","arguments":{}}}]}`,
		"final not last":          `{"turns":[{"final":{"output":1}},{"final":{"output":2}}]}`,
		"unknown member":          `{"turns":[{"final":{"output":1}}],"extra":true}`,
		"tool without name":       `{"turns":[{"toolCall":{"toolName":"","arguments":{}}},{"final":{"output":1}}]}`,
		"final without output":    `{"turns":[{"final":{}}]}`,
		"final with both outputs": `{"turns":[{"final":{"output":1,"outputText":{}}}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseTrajectory([]byte(raw)); err == nil {
				t.Fatal("parseTrajectory() error = nil, want rejection")
			}
		})
	}
}
