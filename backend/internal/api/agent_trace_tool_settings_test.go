package api

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/domain"
)

func TestToolAttemptView_GenerateImage_ProjectsNamedSettings(t *testing.T) {
	got := projectAttempt(t, photoSetRunInput, toolAttempt("generate_image", domain.ToolAttemptStarted,
		`{"photoAssetId":"asset_b","settings":{"prompt":"clay look","aspectRatio":"3:4","resolution":"1K","seed":7}}`, ""),
		toolViewFieldBound)

	assertKey(t, got, "settings", `{"prompt":"clay look","aspectRatio":"3:4","resolution":"1K"}`)
	assertKey(t, got, "withheld", "")
}

func TestToolAttemptView_GenerateImageLongPrompt_TruncatedAndMarked(t *testing.T) {
	// 499 ASCII bytes followed by a two-byte character straddle the cut: the cut keeps
	// whole characters, so the projected prompt stops before it.
	prompt := strings.Repeat("a", maxProjectedPromptBytes-1) + "é" + strings.Repeat("b", 600)
	input, _ := json.Marshal(map[string]any{"photoAssetId": "asset_b", "settings": map[string]any{"prompt": prompt, "resolution": "1K"}})

	got := projectAttempt(t, photoSetRunInput, toolAttempt("generate_image", domain.ToolAttemptStarted, string(input), ""), toolViewFieldBound)

	var settings struct {
		Prompt          string `json:"prompt"`
		PromptTruncated bool   `json:"promptTruncated"`
		Resolution      string `json:"resolution"`
	}
	if err := json.Unmarshal(got["settings"], &settings); err != nil {
		t.Fatalf("decode settings %s: %v", got["settings"], err)
	}
	if want := strings.Repeat("a", maxProjectedPromptBytes-1); settings.Prompt != want {
		t.Errorf("prompt = %d bytes %q..., want the first %d bytes cut at a character boundary", len(settings.Prompt), settings.Prompt[:8], len(want))
	}
	if !settings.PromptTruncated || settings.Resolution != "1K" || !utf8.ValidString(settings.Prompt) {
		t.Errorf("settings = %+v, want promptTruncated true, resolution 1K and valid UTF-8", settings)
	}

	exact := strings.Repeat("c", maxProjectedPromptBytes)
	input, _ = json.Marshal(map[string]any{"photoAssetId": "asset_b", "settings": map[string]any{"prompt": exact}})
	got = projectAttempt(t, photoSetRunInput, toolAttempt("generate_image", domain.ToolAttemptStarted, string(input), ""), toolViewFieldBound)
	assertKey(t, got, "settings", `{"prompt":"`+exact+`"}`)
}

func TestToolAttemptView_GenerateImageMissingSettingsMembers_ProjectAbsent(t *testing.T) {
	cases := map[string]struct{ input, want string }{
		"no settings":           {`{"photoAssetId":"asset_b"}`, ""},
		"settings not object":   {`{"photoAssetId":"asset_b","settings":"warm"}`, ""},
		"no named member":       {`{"photoAssetId":"asset_b","settings":{"seed":7}}`, ""},
		"members of other type": {`{"photoAssetId":"asset_b","settings":{"prompt":7,"aspectRatio":["3:4"],"resolution":""}}`, ""},
		"prompt only":           {`{"photoAssetId":"asset_b","settings":{"prompt":"warm"}}`, `{"prompt":"warm"}`},
		"no prompt":             {`{"photoAssetId":"asset_b","settings":{"aspectRatio":"1:1"}}`, `{"aspectRatio":"1:1"}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := projectAttempt(t, photoSetRunInput, toolAttempt("generate_image", domain.ToolAttemptStarted, tc.input, ""), toolViewFieldBound)
			assertKey(t, got, "settings", tc.want)
			assertKey(t, got, "withheld", "")
		})
	}
}

func TestToolAttemptView_SettingsOverFieldBound_WithheldNotCut(t *testing.T) {
	got := projectAttempt(t, `{}`, toolAttempt("generate_image", domain.ToolAttemptStarted,
		`{"photoAssetId":"p","settings":{"prompt":"`+strings.Repeat("w", 40)+`","aspectRatio":"16:9"}}`, ""), 32)

	assertKey(t, got, "settings", `{"aspectRatio":"16:9"}`)
	assertKey(t, got, "withheld", `[{"field":"settings.prompt","reason":"TOO_LARGE"}]`)
}

func TestToolAttemptView_ReviewAssetSucceeded_ProjectsVerdictAndReasons(t *testing.T) {
	got := projectAttempt(t, photoSetRunInput, toolAttempt("review_asset", domain.ToolAttemptSucceeded,
		`{"assetRef":"artifact_1","photoAssetId":"asset_b","styleRequirement":"clay look"}`,
		`{"passed":false,"policyVersion":"vision-v1","reasons":["Face lost its likeness","Background is flat"],"reviewModelId":"vision-review","styleDigest":"sha256:s"}`),
		toolViewFieldBound)

	assertKey(t, got, "review", `{"passed":false,"reasons":["Face lost its likeness","Background is flat"]}`)
	for _, key := range []string{"photo", "settings", "test", "deployment", "video", "withheld"} {
		assertKey(t, got, key, "")
	}
}

func TestToolAttemptView_ReviewAssetMissingOrMalformedMembers_ProjectAbsent(t *testing.T) {
	cases := map[string]struct{ result, want string }{
		"passed without reasons": {`{"passed":true,"policyVersion":"p1"}`, `{"passed":true}`},
		"empty reasons":          {`{"passed":true,"reasons":[]}`, `{"passed":true}`},
		"reasons not strings":    {`{"passed":false,"reasons":[1,2]}`, `{"passed":false}`},
		"too many reasons":       {`{"passed":false,"reasons":["a","b","c","d"]}`, `{"passed":false}`},
		"verdict not boolean":    {`{"passed":"no","reasons":["a"]}`, ""},
		"no verdict":             {`{"reasons":["a"]}`, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := projectAttempt(t, `{}`, toolAttempt("review_asset", domain.ToolAttemptSucceeded,
				`{"assetRef":"artifact_1","photoAssetId":"asset_b","styleRequirement":"clay"}`, tc.result), toolViewFieldBound)
			assertKey(t, got, "review", tc.want)
		})
	}
}

func TestToolAttemptView_ReviewAssetNotSucceeded_ProjectsNoVerdict(t *testing.T) {
	for _, status := range []domain.ToolAttemptStatus{domain.ToolAttemptStarted, domain.ToolAttemptFailed} {
		got := projectAttempt(t, `{}`, toolAttempt("review_asset", status,
			`{"assetRef":"artifact_1","photoAssetId":"asset_b","styleRequirement":"clay"}`, `{"passed":true,"reasons":["ok"]}`), toolViewFieldBound)
		assertNoToolView(t, got)
	}
}

func TestToolAttemptView_ReviewReasonOverFieldBound_WithheldNotCut(t *testing.T) {
	got := projectAttempt(t, `{}`, toolAttempt("review_asset", domain.ToolAttemptSucceeded,
		`{"assetRef":"a","photoAssetId":"p","styleRequirement":"clay"}`,
		`{"passed":false,"reasons":["short","`+strings.Repeat("r", 40)+`"]}`), 32)

	assertKey(t, got, "review", `{"passed":false}`)
	assertKey(t, got, "withheld", `[{"field":"review.reasons","reason":"TOO_LARGE"}]`)
}

func TestToolAttemptView_SettingsAndReview_NeverCarryRawParametersOrCredentials(t *testing.T) {
	const secret = "sk-live-s3cr3t"
	generation := projectAttempt(t, photoSetRunInput, toolAttempt("generate_image", domain.ToolAttemptSucceeded,
		`{"photoAssetId":"asset_b","settings":{"prompt":"warm","seed":424242,"apiKey":"`+secret+`"},"authorization":"Bearer `+secret+`"}`,
		`{"assetRef":"artifact_1","imageUrl":"/api/runs/r/artifacts/artifact_1/content?token=`+secret+`","settingsDigest":"sha256:d","artifact":{"artifactId":"artifact_1","mediaType":"image/png","sizeBytes":1,"sha256":"11"},"usage":{"cost":0.045}}`),
		toolViewFieldBound)
	review := projectAttempt(t, photoSetRunInput, toolAttempt("review_asset", domain.ToolAttemptSucceeded,
		`{"assetRef":"artifact_1","photoAssetId":"asset_b","styleRequirement":"`+secret+`"}`,
		`{"passed":true,"policyVersion":"vision-v1","reasons":[],"reviewModelId":"vision-review","styleDigest":"sha256:s","token":"`+secret+`"}`),
		toolViewFieldBound)

	for name, got := range map[string]map[string]json.RawMessage{"generate_image": generation, "review_asset": review} {
		encoded, _ := json.Marshal(got)
		for _, forbidden := range []string{secret, "424242", "seed", "apiKey", "authorization", "imageUrl", "settingsDigest", "usage", "styleRequirement", "styleDigest", "policyVersion", "reviewModelId"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Errorf("%s Tool Attempt %s carries %q, want only the named members", name, encoded, forbidden)
			}
		}
	}
	assertKey(t, generation, "settings", `{"prompt":"warm"}`)
	assertKey(t, review, "review", `{"passed":true}`)
}
