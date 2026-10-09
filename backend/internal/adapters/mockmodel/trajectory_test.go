package mockmodel

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/registry"
)

const briefDocument = `{"brief":"make it cinematic","photos":[{"mediaType":"image/jpeg","photoAssetId":"asset_photo_1","sha256":"abc","sizeBytes":10}]}`

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

func TestProvider_Generate_ScriptedTrajectory_AnswersEachTurnFromToolResultCount(t *testing.T) {
	p := NewProvider()
	ctx := context.Background()
	imageResult := `{"assetRef":"img_0011","imageUrl":"http://mock/v1/assets/img_0011.png","settingsDigest":"sha256:00"}`
	reviewResult := `{"passed":true,"policyVersion":"mock-policy-v1","reasons":[]}`
	videoResult := `{"videoUrl":"http://mock/v1/videos/vid_0011.mp4"}`

	turn0, err := p.Generate(ctx, scriptedRequest(t, "photo-set"))
	if err != nil {
		t.Fatalf("turn 0 Generate() error = %v", err)
	}
	assertToolCall(t, turn0, "generate_image", `{"photoAssetId":"asset_photo_1","settings":{"strength":0.6,"style":"film"}}`)

	turn1, err := p.Generate(ctx, scriptedRequest(t, "photo-set", imageResult))
	if err != nil {
		t.Fatalf("turn 1 Generate() error = %v", err)
	}
	assertToolCall(t, turn1, "review_asset", `{"assetRef":"img_0011","mock":"pass","photoAssetId":"asset_photo_1"}`)

	turn2, err := p.Generate(ctx, scriptedRequest(t, "photo-set", imageResult, reviewResult))
	if err != nil {
		t.Fatalf("turn 2 Generate() error = %v", err)
	}
	assertToolCall(t, turn2, "generate_video", `{"assetRef":"img_0011","photoAssetId":"asset_photo_1","settings":{"durationSeconds":4}}`)

	turn3, err := p.Generate(ctx, scriptedRequest(t, "photo-set", imageResult, reviewResult, videoResult))
	if err != nil {
		t.Fatalf("turn 3 Generate() error = %v", err)
	}
	if turn3.Decision.Kind != registry.DecisionFinal {
		t.Fatalf("turn 3 Decision.Kind = %q, want FINAL", turn3.Decision.Kind)
	}
	wantFinal := `{"assetRef":"img_0011","imageUrl":"http://mock/v1/assets/img_0011.png","photoAssetId":"asset_photo_1","videoUrl":"http://mock/v1/videos/vid_0011.mp4"}`
	if string(turn3.Decision.Output) != wantFinal {
		t.Fatalf("turn 3 Output =\n%s\nwant\n%s", turn3.Decision.Output, wantFinal)
	}
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
				return scriptedRequest(t, "photo-set", `{}`, `{}`, `{}`, `{}`)
			},
			wantErr: `script "photo-set" has no turn 4`,
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

func TestTrajectories_EmbeddedFixtures_LoadAndAppearInTextModelConfigSchema(t *testing.T) {
	names := TrajectoryNames()
	for _, want := range []string{"photo-set", "photo-set-skip-review"} {
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
		"no turns":          `{"turns":[]}`,
		"both shapes":       `{"turns":[{"toolCall":{"toolName":"t","arguments":{}},"final":{"output":1}}]}`,
		"last not final":    `{"turns":[{"toolCall":{"toolName":"t","arguments":{}}}]}`,
		"final not last":    `{"turns":[{"final":{"output":1}},{"final":{"output":2}}]}`,
		"unknown member":    `{"turns":[{"final":{"output":1}}],"extra":true}`,
		"tool without name": `{"turns":[{"toolCall":{"toolName":"","arguments":{}}},{"final":{"output":1}}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseTrajectory([]byte(raw)); err == nil {
				t.Fatal("parseTrajectory() error = nil, want rejection")
			}
		})
	}
}
