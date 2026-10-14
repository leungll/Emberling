package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

// toolViewFieldBound is the field bound most cases project under: far above any member
// these fixtures carry, so only the oversized cases hit it.
const toolViewFieldBound = 8192

// photoSetRunInput is a Run input holding one three-photo AssetRef list and a text brief.
const photoSetRunInput = `{"brief":"one warm look","photos":[` +
	`{"assetId":"asset_a","mediaType":"image/png","sizeBytes":10,"sha256":"aa"},` +
	`{"assetId":"asset_b","mediaType":"image/png","sizeBytes":11,"sha256":"bb"},` +
	`{"assetId":"asset_c","mediaType":"image/png","sizeBytes":12,"sha256":"cc"}]}`

// projectAttempt renders one Tool Attempt through the whole Agent Trace response and
// returns that Attempt's JSON object, so a case sees exactly what the route serves.
func projectAttempt(t *testing.T, runInput string, attempt domain.ToolAttempt, bound int) map[string]json.RawMessage {
	t.Helper()
	trace := service.AgentTrace{
		RunInput: json.RawMessage(runInput),
		Turns:    []service.AgentTraceTurn{{ToolAttempts: []domain.ToolAttempt{attempt}}},
	}
	body, err := json.Marshal(toAgentTraceResponse(trace, bound))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Turns []struct {
			ToolAttempts []map[string]json.RawMessage `json:"toolAttempts"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if len(decoded.Turns) != 1 || len(decoded.Turns[0].ToolAttempts) != 1 {
		t.Fatalf("response %s does not hold exactly one Tool Attempt", body)
	}
	return decoded.Turns[0].ToolAttempts[0]
}

func toolAttempt(tool string, status domain.ToolAttemptStatus, input, result string) domain.ToolAttempt {
	a := domain.ToolAttempt{
		ID: "ta_1", ToolName: tool, AttemptNo: 1, Status: status,
		Input: json.RawMessage(input), StartedAt: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC),
	}
	if result != "" {
		a.Result = json.RawMessage(result)
	}
	return a
}

// assertKey compares one projected key with want, given as compact JSON; want "" means the
// key must be absent.
func assertKey(t *testing.T, got map[string]json.RawMessage, key, want string) {
	t.Helper()
	raw, present := got[key]
	if want == "" {
		if present {
			t.Errorf("%s = %s, want the key absent", key, raw)
		}
		return
	}
	if !present {
		t.Errorf("%s is absent, want %s", key, want)
		return
	}
	var gotValue, wantValue any
	if err := json.Unmarshal(raw, &gotValue); err != nil {
		t.Fatalf("decode %s: %v", key, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode want %s: %v", want, err)
	}
	if diff := cmp.Diff(wantValue, gotValue); diff != "" {
		t.Errorf("%s mismatch (-want +got):\n%s", key, diff)
	}
}

// toolViewKeys are every key this projection may add to a Tool Attempt.
var toolViewKeys = []string{"photo", "test", "deployment", "video", "withheld"}

func assertNoToolView(t *testing.T, got map[string]json.RawMessage) {
	t.Helper()
	for _, key := range toolViewKeys {
		assertKey(t, got, key, "")
	}
}

const generateImageInput = `{"photoAssetId":"asset_b","settings":{"prompt":"warm"}}`

func TestToolAttemptView_GenerateImageInPhotoSet_ProjectsPhotoPositionOfCount(t *testing.T) {
	got := projectAttempt(t, photoSetRunInput, toolAttempt("generate_image", domain.ToolAttemptSucceeded, generateImageInput,
		`{"assetRef":"artifact_1","imageUrl":"/api/runs/run_1/artifacts/artifact_1/content","settingsDigest":"sha256:1","artifact":{"artifactId":"artifact_1","mediaType":"image/png","sizeBytes":1,"sha256":"11"}}`),
		toolViewFieldBound)

	assertKey(t, got, "photo", `{"photoAssetId":"asset_b","position":2,"count":3}`)
	for _, key := range []string{"test", "deployment", "video", "withheld"} {
		assertKey(t, got, key, "")
	}
	for _, forbidden := range []string{"settings", "imageUrl", "artifact", "result", "arguments"} {
		if _, present := got[forbidden]; present {
			t.Errorf("Tool Attempt projects %q, want only the declared members", forbidden)
		}
	}
}

func TestToolAttemptView_GenerateImageStillRunning_ProjectsPhotoFromArguments(t *testing.T) {
	got := projectAttempt(t, photoSetRunInput, toolAttempt("generate_image", domain.ToolAttemptStarted, generateImageInput, ""), toolViewFieldBound)

	assertKey(t, got, "photo", `{"photoAssetId":"asset_b","position":2,"count":3}`)
}

func TestToolAttemptView_PhotoNotInRunInputAssetList_ProjectsAssetWithoutPosition(t *testing.T) {
	cases := map[string]string{
		"no run input":         ``,
		"no asset list":        `{"task":"make two images"}`,
		"id not in the set":    `{"photos":[{"assetId":"asset_a"},{"assetId":"asset_c"}]}`,
		"id in two lists":      `{"left":[{"assetId":"asset_b"}],"right":[{"assetId":"asset_a"},{"assetId":"asset_b"}]}`,
		"id twice in one set":  `{"photos":[{"assetId":"asset_b"},{"assetId":"asset_b"}]}`,
		"not an AssetRef list": `{"photos":[{"assetId":"asset_b"},"asset_c"]}`,
	}
	for name, runInput := range cases {
		t.Run(name, func(t *testing.T) {
			got := projectAttempt(t, runInput, toolAttempt("generate_image", domain.ToolAttemptFailed, generateImageInput, ""), toolViewFieldBound)
			assertKey(t, got, "photo", `{"photoAssetId":"asset_b"}`)
		})
	}
}

func TestToolAttemptView_SandboxTestSucceeded_ProjectsPassedAndSummary(t *testing.T) {
	got := projectAttempt(t, `{}`, toolAttempt("sandbox_test", domain.ToolAttemptSucceeded,
		`{"baseCommit":"fixture-v1","patch":"--- a\n+++ b\n"}`,
		`{"passed":false,"baseCommit":"fixture-v1","patchDigest":"sha256:p","resultDigest":"sha256:r","summary":{"total":12,"failed":3}}`),
		toolViewFieldBound)

	assertKey(t, got, "test", `{"passed":false,"summary":{"total":12,"failed":3}}`)
	for _, key := range []string{"photo", "deployment", "video", "withheld"} {
		assertKey(t, got, key, "")
	}
}

func TestToolAttemptView_NoCommittedResult_ProjectsNoResultMembers(t *testing.T) {
	for _, status := range []domain.ToolAttemptStatus{domain.ToolAttemptStarted, domain.ToolAttemptDispatched, domain.ToolAttemptFailed} {
		t.Run(string(status), func(t *testing.T) {
			sandbox := projectAttempt(t, `{}`, toolAttempt("sandbox_test", status, `{"baseCommit":"fixture-v1","patch":"p"}`, ""), toolViewFieldBound)
			assertNoToolView(t, sandbox)

			video := projectAttempt(t, `{}`, toolAttempt("generate_video", status, `{"assetRef":"artifact_1","photoAssetId":"asset_a","settings":{}}`, ""), toolViewFieldBound)
			assertNoToolView(t, video)

			deployment := projectAttempt(t, `{}`, toolAttempt("deploy", status,
				`{"target":{"service":"checkout","environment":"staging"},"baseCommit":"fixture-v1","patchDigest":"sha256:p"}`, ""), toolViewFieldBound)
			assertKey(t, deployment, "deployment", `{"target":{"service":"checkout","environment":"staging"}}`)
		})
	}
}

func TestToolAttemptView_FailedAttemptWithStrayResult_ProjectsNoResultMembers(t *testing.T) {
	// Only a SUCCEEDED Attempt's result is a committed Tool result; anything else is not
	// projected even if a row carried bytes there.
	got := projectAttempt(t, `{}`, toolAttempt("sandbox_test", domain.ToolAttemptFailed, `{"baseCommit":"b","patch":"p"}`,
		`{"passed":true,"summary":{"total":1,"failed":0}}`), toolViewFieldBound)
	assertNoToolView(t, got)
}

func TestToolAttemptView_DeploySucceeded_ProjectsTargetAndVersion(t *testing.T) {
	got := projectAttempt(t, `{}`, toolAttempt("deploy", domain.ToolAttemptSucceeded,
		`{"target":{"service":"checkout","environment":"staging"},"baseCommit":"fixture-v1","patchDigest":"sha256:p","parameters":{"replicas":2}}`,
		`{"operationId":"op_1","version":"v1"}`), toolViewFieldBound)

	assertKey(t, got, "deployment", `{"target":{"service":"checkout","environment":"staging"},"version":"v1"}`)
	for _, key := range []string{"photo", "test", "video", "withheld"} {
		assertKey(t, got, key, "")
	}
}

func TestToolAttemptView_GenerateVideoSucceeded_ProjectsSafeVideoURL(t *testing.T) {
	got := projectAttempt(t, `{}`, toolAttempt("generate_video", domain.ToolAttemptSucceeded,
		`{"assetRef":"artifact_1","photoAssetId":"asset_a","settings":{"durationSeconds":4}}`,
		`{"videoUrl":"http://127.0.0.1:9100/v1/videos/vid_0a1b2c3d.mp4"}`), toolViewFieldBound)

	assertKey(t, got, "video", `{"videoUrl":"http://127.0.0.1:9100/v1/videos/vid_0a1b2c3d.mp4"}`)
	assertKey(t, got, "withheld", "")
	assertKey(t, got, "photo", "")
}

func TestToolAttemptView_VideoURLCouldCarryCredential_WithheldAsUnsafe(t *testing.T) {
	cases := map[string]string{
		"signed query":   "https://cdn.example.test/v.mp4?X-Amz-Signature=deadbeef&token=s3cr3t",
		"empty query":    "https://cdn.example.test/v.mp4?",
		"userinfo":       "https://user:s3cr3t@cdn.example.test/v.mp4",
		"fragment":       "https://cdn.example.test/v.mp4#token=s3cr3t",
		"relative":       "/v1/videos/vid_1.mp4",
		"other scheme":   "s3://bucket/videos/vid_1.mp4",
		"no host":        "https:///v.mp4",
		"not a url text": "::not a url::",
	}
	for name, url := range cases {
		t.Run(name, func(t *testing.T) {
			result, _ := json.Marshal(map[string]string{"videoUrl": url})
			got := projectAttempt(t, `{}`, toolAttempt("generate_video", domain.ToolAttemptSucceeded,
				`{"assetRef":"artifact_1","photoAssetId":"asset_a","settings":{}}`, string(result)), toolViewFieldBound)

			assertKey(t, got, "video", "")
			assertKey(t, got, "withheld", `[{"field":"video.videoUrl","reason":"UNSAFE_URL"}]`)
			for _, raw := range got {
				if strings.Contains(string(raw), "s3cr3t") || strings.Contains(string(raw), "deadbeef") {
					t.Errorf("Tool Attempt %v carries a credential of %q", got, url)
				}
			}
		})
	}
}

func TestToolAttemptView_MemberOverFieldBound_WithheldNotCut(t *testing.T) {
	longVersion := strings.Repeat("v", 64)
	got := projectAttempt(t, `{}`, toolAttempt("deploy", domain.ToolAttemptSucceeded,
		`{"target":{"service":"checkout","environment":"staging"},"baseCommit":"b","patchDigest":"p"}`,
		`{"operationId":"op_1","version":"`+longVersion+`"}`), 32)

	assertKey(t, got, "deployment", `{"target":{"service":"checkout","environment":"staging"}}`)
	assertKey(t, got, "withheld", `[{"field":"deployment.version","reason":"TOO_LARGE"}]`)

	video := projectAttempt(t, `{}`, toolAttempt("generate_video", domain.ToolAttemptSucceeded,
		`{"assetRef":"a","photoAssetId":"p","settings":{}}`,
		`{"videoUrl":"http://provider.example/v1/videos/`+strings.Repeat("x", 64)+`.mp4"}`), 32)
	assertKey(t, video, "video", "")
	assertKey(t, video, "withheld", `[{"field":"video.videoUrl","reason":"TOO_LARGE"}]`)

	photo := projectAttempt(t, photoSetRunInput, toolAttempt("generate_image", domain.ToolAttemptStarted,
		`{"photoAssetId":"`+strings.Repeat("p", 64)+`","settings":{"prompt":"x"}}`, ""), 32)
	assertKey(t, photo, "photo", "")
	assertKey(t, photo, "withheld", `[{"field":"photo.photoAssetId","reason":"TOO_LARGE"}]`)
}

func TestToolAttemptView_MembersOfAnotherShape_ProjectNothing(t *testing.T) {
	// A Tool registered under one of these names with a different schema yields no
	// projection: every member is type-checked, nothing is coerced.
	cases := []domain.ToolAttempt{
		toolAttempt("generate_image", domain.ToolAttemptSucceeded, `{"photoAssetId":7}`, `{}`),
		toolAttempt("sandbox_test", domain.ToolAttemptSucceeded, `{}`, `{"passed":"yes","summary":{"total":1,"failed":0}}`),
		toolAttempt("sandbox_test", domain.ToolAttemptSucceeded, `{}`, `{"passed":true,"summary":{"total":1.5,"failed":0}}`),
		toolAttempt("sandbox_test", domain.ToolAttemptSucceeded, `{}`, `{"passed":true}`),
		toolAttempt("deploy", domain.ToolAttemptSucceeded, `{"target":"checkout"}`, `{"version":2}`),
		toolAttempt("deploy", domain.ToolAttemptSucceeded, `{"target":{"service":"checkout"}}`, `not json`),
		toolAttempt("generate_video", domain.ToolAttemptSucceeded, `{}`, `{"videoUrl":["http://a.example/v.mp4"]}`),
	}
	for _, attempt := range cases {
		t.Run(attempt.ToolName+" "+string(attempt.Result), func(t *testing.T) {
			assertNoToolView(t, projectAttempt(t, photoSetRunInput, attempt, toolViewFieldBound))
		})
	}
}

func TestToolAttemptView_OtherTools_ProjectNothing(t *testing.T) {
	for _, attempt := range []domain.ToolAttempt{
		toolAttempt("lookup", domain.ToolAttemptSucceeded, `{"key":"record-1"}`, `{"value":"v1"}`),
		toolAttempt("review_asset", domain.ToolAttemptSucceeded, `{"assetRef":"artifact_1","photoAssetId":"asset_b"}`, `{"passed":true,"policyVersion":"p1"}`),
	} {
		assertNoToolView(t, projectAttempt(t, photoSetRunInput, attempt, toolViewFieldBound))
	}
}
