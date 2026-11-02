//go:build integration

// Agent Trace generation settings and review verdict contract: each generate_image Tool
// Attempt shows the prompt, aspect ratio and resolution its committed arguments asked
// for, with a long prompt cut and marked, and each SUCCEEDED review_asset Tool Attempt
// shows its verdict and reasons. Nothing else of either call's arguments or result, and
// no credential, reaches the response.
package contract

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
	"github.com/leungll/Emberling/backend/internal/tools/reviewasset"
)

// toolSettingsAttempt is one Tool Attempt with the settings and review keys.
type toolSettingsAttempt struct {
	ID       string `json:"id"`
	ToolName string `json:"toolName"`
	Status   string `json:"status"`
	Settings *struct {
		Prompt          *string `json:"prompt"`
		PromptTruncated bool    `json:"promptTruncated"`
		AspectRatio     *string `json:"aspectRatio"`
		Resolution      *string `json:"resolution"`
	} `json:"settings"`
	Review *struct {
		Passed  bool     `json:"passed"`
		Reasons []string `json:"reasons"`
	} `json:"review"`
}

type toolSettingsTrace struct {
	Turns []struct {
		ToolAttempts []toolSettingsAttempt `json:"toolAttempts"`
	} `json:"turns"`
}

func (tr toolSettingsTrace) attemptsOf(toolName string) []toolSettingsAttempt {
	var out []toolSettingsAttempt
	for _, turn := range tr.Turns {
		for _, attempt := range turn.ToolAttempts {
			if attempt.ToolName == toolName {
				out = append(out, attempt)
			}
		}
	}
	return out
}

// TestTraceToolSettings_PhotoSetStory_ShowsEachRoundsSettingsAndReviewVerdicts runs the
// two-round photo story: the first round's three generations show the first prompt, the
// second round's the revised one, and every review shows the verdict and reasons it
// returned, so a viewer sees how the settings changed between rounds and why.
func TestTraceToolSettings_PhotoSetStory_ShowsEachRoundsSettingsAndReviewVerdicts(t *testing.T) {
	env := newPhotoSetEnv(t)
	snapshot, _, _ := runPhotoSet(t, env, "photo-set", 0)
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", got, snapshot)
	}
	runID, _ := snapshot["run"].(map[string]any)["id"].(string)
	status, body := getAgentTrace(t, env.testEnv, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, body)
	}
	trace := decodeBody[toolSettingsTrace](t, body)
	raw := toolAttemptsJSON(t, body)

	generations := trace.attemptsOf(generateimage.ToolName)
	if len(generations) != 6 {
		t.Fatalf("generate_image Attempts = %d, want 6", len(generations))
	}
	for i, attempt := range generations {
		want := "Warm film look with strong grain"
		if i >= 3 {
			want = "Warm film look with soft grain"
		}
		if attempt.Settings == nil || attempt.Settings.Prompt == nil || *attempt.Settings.Prompt != want ||
			attempt.Settings.PromptTruncated || attempt.Settings.AspectRatio != nil || attempt.Settings.Resolution != nil {
			t.Errorf("generate_image Attempt %d settings = %+v, want only the prompt %q", i, attempt.Settings, want)
		}
		if attempt.Review != nil {
			t.Errorf("generate_image Attempt %d projects a review %+v", i, attempt.Review)
		}
	}

	reviews := trace.attemptsOf(reviewasset.ToolName)
	wantPassed := []bool{true, false, false, true, true, true}
	if len(reviews) != len(wantPassed) {
		t.Fatalf("review_asset Attempts = %d, want %d", len(reviews), len(wantPassed))
	}
	for i, attempt := range reviews {
		if attempt.Review == nil || attempt.Review.Passed != wantPassed[i] {
			t.Errorf("review_asset Attempt %d review = %+v, want passed %v", i, attempt.Review, wantPassed[i])
			continue
		}
		if wantPassed[i] && len(attempt.Review.Reasons) != 0 {
			t.Errorf("review_asset Attempt %d reasons = %v, want none for a pass", i, attempt.Review.Reasons)
		}
		if !wantPassed[i] && len(attempt.Review.Reasons) != 1 {
			t.Errorf("review_asset Attempt %d reasons = %v, want the one reason the review gave", i, attempt.Review.Reasons)
		}
		if attempt.Settings != nil {
			t.Errorf("review_asset Attempt %d projects settings %+v", i, attempt.Settings)
		}
	}

	for _, forbidden := range []string{`"styleRequirement"`, `"styleDigest"`, `"policyVersion"`, `"reviewModelId"`, `"mock"`,
		`"settingsDigest"`, `"imageUrl"`, `"usage"`, `"arguments"`, `"result"`, testCallbackSigningSecret} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("agent trace Tool Attempts contain %s, want only the named settings and review members", forbidden)
		}
	}
}

// TestTraceToolSettings_LongPrompt_TruncatedAndMarked drives one generation whose prompt
// is the longest the Tool accepts: the Trace shows its first 500 bytes marked as cut,
// never the rest, and still shows the other named settings in full.
func TestTraceToolSettings_LongPrompt_TruncatedAndMarked(t *testing.T) {
	env := newTraceFactsEnv(t)
	head := strings.Repeat("Warm film look. ", 32)[:500]
	tail := "TAIL-NOT-PROJECTED-" + strings.Repeat("x", 4000)
	prompt := (head + tail)[:4000]
	var calls atomic.Int32
	env.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		if calls.Add(1) == 1 {
			arguments, _ := json.Marshal(map[string]any{
				"photoAssetId": traceFactsPhotoA,
				"settings":     map[string]any{"prompt": prompt, "aspectRatio": "3:4", "resolution": "1K", "seed": 9876543},
			})
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioToolCall, ToolName: generateimage.ToolName, ToolArguments: arguments}
		}
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: traceFactsFinal}
	}
	workflowID, version := saveTraceFactsDefinition(t, env, nil)
	runID := createAgentRun(t, env, workflowID, version, "make one image")
	snapshot := env.waitForTerminal(t, runID, agentTraceWait)
	if status := runStatusOf(snapshot); status != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", status, snapshot)
	}
	status, body := getAgentTrace(t, env, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, body)
	}
	trace := decodeBody[toolSettingsTrace](t, body)
	raw := toolAttemptsJSON(t, body)

	generations := trace.attemptsOf(generateimage.ToolName)
	if len(generations) != 1 || generations[0].Settings == nil {
		t.Fatalf("generate_image Attempts = %+v, want one with settings", generations)
	}
	settings := generations[0].Settings
	if settings.Prompt == nil || *settings.Prompt != head || !settings.PromptTruncated {
		t.Errorf("settings prompt = %d bytes, truncated %v; want the first 500 bytes marked as cut", len(derefString(settings.Prompt)), settings.PromptTruncated)
	}
	if derefString(settings.AspectRatio) != "3:4" || derefString(settings.Resolution) != "1K" {
		t.Errorf("settings = %+v, want aspectRatio 3:4 and resolution 1K", settings)
	}
	for _, forbidden := range []string{"TAIL-NOT-PROJECTED", "9876543", `"seed"`} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("agent trace Tool Attempts contain %s, want the prompt cut and the seed left out", forbidden)
		}
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
