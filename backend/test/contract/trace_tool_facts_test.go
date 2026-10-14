//go:build integration

// Agent Trace Tool-call facts contract: GET /runs/{runId}/nodes/{nodeRunId}/agent carries,
// on each Tool Attempt, the few persisted argument and result members Studio shows on a
// run card - the photo a generate_image call works on and its position in the Run's photo
// set, a sandbox_test result summary, a deploy target and version, and a generate_video
// URL in a form that cannot carry a credential. Argument members come from the Attempt's
// committed input; result members appear only once the Attempt SUCCEEDED, never earlier
// and never invented; a member over the field bound is withheld and named, not cut.
package contract

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/fixture/sandboxrunner"
	"github.com/leungll/Emberling/backend/internal/tools/deploy"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
	"github.com/leungll/Emberling/backend/internal/tools/generatevideo"
	"github.com/leungll/Emberling/backend/internal/tools/sandboxtest"
)

// toolFactsAttempt is one Tool Attempt with every key this projection may add.
type toolFactsAttempt struct {
	ID       string `json:"id"`
	ToolName string `json:"toolName"`
	Status   string `json:"status"`
	Photo    *struct {
		PhotoAssetID string `json:"photoAssetId"`
		Position     *int   `json:"position"`
		Count        *int   `json:"count"`
	} `json:"photo"`
	Test *struct {
		Passed  *bool `json:"passed"`
		Summary *struct {
			Total  *int `json:"total"`
			Failed *int `json:"failed"`
		} `json:"summary"`
	} `json:"test"`
	Deployment *struct {
		Target *struct {
			Service     string `json:"service"`
			Environment string `json:"environment"`
		} `json:"target"`
		Version *string `json:"version"`
	} `json:"deployment"`
	Video *struct {
		VideoURL string `json:"videoUrl"`
	} `json:"video"`
	Withheld []struct {
		Field  string `json:"field"`
		Reason string `json:"reason"`
	} `json:"withheld"`
}

type toolFactsTrace struct {
	Turns []struct {
		ToolAttempts []toolFactsAttempt `json:"toolAttempts"`
	} `json:"turns"`
}

func (tr toolFactsTrace) attemptsOf(toolName string) []toolFactsAttempt {
	var out []toolFactsAttempt
	for _, turn := range tr.Turns {
		for _, attempt := range turn.ToolAttempts {
			if attempt.ToolName == toolName {
				out = append(out, attempt)
			}
		}
	}
	return out
}

// loadToolFactsTrace returns the decoded Trace and the raw JSON of its Tool Attempts only,
// so a secrecy check is not confused by the fact ledger's own bindings.
func loadToolFactsTrace(t *testing.T, env *testEnv, runID, agentNodeRunID string) (toolFactsTrace, string) {
	t.Helper()
	status, body := getAgentTrace(t, env, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, body)
	}
	return decodeBody[toolFactsTrace](t, body), toolAttemptsJSON(t, body)
}

func toolAttemptsJSON(t *testing.T, body []byte) string {
	t.Helper()
	var raw struct {
		Turns []struct {
			ToolAttempts json.RawMessage `json:"toolAttempts"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode agent trace: %v", err)
	}
	var out strings.Builder
	for _, turn := range raw.Turns {
		out.Write(turn.ToolAttempts)
	}
	return out.String()
}

func withheldFields(attempt toolFactsAttempt) map[string]string {
	out := make(map[string]string, len(attempt.Withheld))
	for _, w := range attempt.Withheld {
		out[w.Field] = w.Reason
	}
	return out
}

// TestTraceToolFacts_PhotoSetStory_ProjectsPhotoPositionsAndVideoURL proves every
// generate_image Attempt names its photo with that photo's 1-based position among the
// Run's three uploaded photos, and the generate_video Attempt names the Mock Provider's
// video URL, while no other argument or result member is projected.
func TestTraceToolFacts_PhotoSetStory_ProjectsPhotoPositionsAndVideoURL(t *testing.T) {
	env := newPhotoSetEnv(t)
	snapshot, _, photoIDs := runPhotoSet(t, env, "photo-set", 0)
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", got, snapshot)
	}
	runID, _ := snapshot["run"].(map[string]any)["id"].(string)
	trace, raw := loadToolFactsTrace(t, env.testEnv, runID, nodeRunIDOf(t, snapshot, "node_agent"))

	generations := trace.attemptsOf(generateimage.ToolName)
	if len(generations) != 6 {
		t.Fatalf("generate_image Attempts = %d, want 6", len(generations))
	}
	for _, attempt := range generations {
		if attempt.Photo == nil {
			t.Errorf("generate_image Attempt %s has no photo", attempt.ID)
			continue
		}
		index := slices.Index(photoIDs, attempt.Photo.PhotoAssetID)
		if index < 0 {
			t.Errorf("Attempt %s photoAssetId = %q, want one of %v", attempt.ID, attempt.Photo.PhotoAssetID, photoIDs)
			continue
		}
		if attempt.Photo.Position == nil || *attempt.Photo.Position != index+1 || attempt.Photo.Count == nil || *attempt.Photo.Count != 3 {
			t.Errorf("Attempt %s photo = %+v, want position %d of 3", attempt.ID, attempt.Photo, index+1)
		}
		if attempt.Test != nil || attempt.Deployment != nil || attempt.Video != nil || len(attempt.Withheld) != 0 {
			t.Errorf("generate_image Attempt %s carries another Tool's facts: %+v", attempt.ID, attempt)
		}
	}

	videos := trace.attemptsOf(generatevideo.ToolName)
	if len(videos) != 1 || videos[0].Status != "SUCCEEDED" || videos[0].Video == nil {
		t.Fatalf("generate_video Attempts = %+v, want one SUCCEEDED Attempt with a video", videos)
	}
	if url := videos[0].Video.VideoURL; !strings.HasPrefix(url, env.providerURL+"/v1/videos/vid_") || !strings.HasSuffix(url, ".mp4") {
		t.Errorf("video.videoUrl = %q, want the Mock Provider's video URL", url)
	}
	if videos[0].Photo != nil {
		t.Errorf("generate_video Attempt projects photo %+v, want none", videos[0].Photo)
	}

	for _, forbidden := range []string{`"settings"`, `"prompt"`, `"imageUrl"`, `"assetRef"`, `"arguments"`, `"result"`, `"sha256"`} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("agent trace Tool Attempts contain %s, want only the projected members", forbidden)
		}
	}
}

// TestTraceToolFacts_PhotoIDNotInRunInput_ProjectsAssetWithoutPosition proves a
// generate_image call whose photo is not in any AssetRef list of the Run input is shown
// with its photo id only: the position is absent rather than guessed.
func TestTraceToolFacts_PhotoIDNotInRunInput_ProjectsAssetWithoutPosition(t *testing.T) {
	limit := 5
	_, body := runTraceFactsAgent(t, &limit)
	trace := decodeBody[toolFactsTrace](t, []byte(body))
	raw := toolAttemptsJSON(t, []byte(body))

	generations := trace.attemptsOf(generateimage.ToolName)
	if len(generations) != 2 {
		t.Fatalf("generate_image Attempts = %d, want 2", len(generations))
	}
	want := []string{traceFactsPhotoA, traceFactsPhotoB}
	for i, attempt := range generations {
		if attempt.Photo == nil || attempt.Photo.PhotoAssetID != want[i] {
			t.Errorf("generate_image Attempt %d photo = %+v, want %s", i, attempt.Photo, want[i])
			continue
		}
		if attempt.Photo.Position != nil || attempt.Photo.Count != nil {
			t.Errorf("generate_image Attempt %d photo = %+v, want no position: the Run input holds no photo set", i, attempt.Photo)
		}
	}
	if strings.Contains(raw, `"position"`) || strings.Contains(raw, `"count"`) {
		t.Errorf("agent trace Tool Attempts invent a photo position: %s", raw)
	}
}

// TestTraceToolFacts_SoftwareDelivery_ResultFactsAppearOnlyOnceSucceeded proves the
// sandbox_test Attempt shows no test summary while it waits for the runner's callback,
// and after the delivery completes shows the runner's summary; the deploy Attempt shows
// its target and the version production reported.
func TestTraceToolFacts_SoftwareDelivery_ResultFactsAppearOnlyOnceSucceeded(t *testing.T) {
	f := newDeliveryFixture(t)
	env := f.startBackend(t, testEnvOptions{})

	control(t, f.runner.http.URL, "pause", `{"kinds":["callback"]}`)
	runID := createDeliveryRun(t, env, "software-delivery")
	env.waitForNodeRunStatus(t, runID, "node_agent", "WAITING_CALLBACK", e2eWait)
	waitRunnerHeld(t, f, "callback")
	agentNodeRunID := nodeRunIDOfSnapshot(t, env, runID, "node_agent")

	waiting, _ := loadToolFactsTrace(t, env, runID, agentNodeRunID)
	tests := waiting.attemptsOf(sandboxtest.ToolName)
	if len(tests) != 1 || tests[0].Status != "DISPATCHED" {
		t.Fatalf("sandbox_test Attempts while waiting = %+v, want one DISPATCHED Attempt", tests)
	}
	if tests[0].Test != nil || len(tests[0].Withheld) != 0 {
		t.Errorf("DISPATCHED sandbox_test Attempt projects %+v, want no result facts before the result commits", tests[0])
	}

	control(t, f.runner.http.URL, "release", "")
	snapshot := env.waitForTerminal(t, runID, e2eWait)
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", got, snapshot)
	}
	done, raw := loadToolFactsTrace(t, env, runID, agentNodeRunID)

	tests = done.attemptsOf(sandboxtest.ToolName)
	if len(tests) != 1 || tests[0].Status != "SUCCEEDED" || tests[0].Test == nil {
		t.Fatalf("sandbox_test Attempts = %+v, want one SUCCEEDED Attempt with a test summary", tests)
	}
	test := tests[0].Test
	if test.Passed == nil || !*test.Passed || test.Summary == nil || test.Summary.Total == nil || *test.Summary.Total != 12 ||
		test.Summary.Failed == nil || *test.Summary.Failed != 0 {
		t.Errorf("sandbox_test test = %+v, want passed with 12 total and 0 failed", test)
	}

	deploys := done.attemptsOf(deploy.ToolName)
	if len(deploys) != 1 || deploys[0].Status != "SUCCEEDED" || deploys[0].Deployment == nil {
		t.Fatalf("deploy Attempts = %+v, want one SUCCEEDED Attempt with a deployment", deploys)
	}
	deployment := deploys[0].Deployment
	if deployment.Target == nil || deployment.Target.Service != deliveryService || deployment.Target.Environment != deliveryEnvironment ||
		deployment.Version == nil || *deployment.Version != "v1" {
		t.Errorf("deploy deployment = %+v, want %s/%s at v1", deployment, deliveryService, deliveryEnvironment)
	}
	for _, forbidden := range []string{deliveryPatch[:12], `"patch"`, `"patchDigest"`, `"resultDigest"`, `"operationId"`, sandboxrunner.PatchDigest(deliveryPatch)} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("agent trace Tool Attempts contain %q, want only the projected members", forbidden)
		}
	}
}

// TestTraceToolFacts_DeployAwaitingProduction_ProjectsTargetWithoutVersion proves a
// deploy Attempt still STARTED, its request held by production, shows the target from its
// committed arguments and no version, because no result has been committed.
func TestTraceToolFacts_DeployAwaitingProduction_ProjectsTargetWithoutVersion(t *testing.T) {
	f := newDeliveryFixture(t)
	control(t, f.production.URL, "pause", `{"kinds":["deploy"]}`)
	env := f.startBackend(t, testEnvOptions{})

	runID := createDeliveryRun(t, env, "software-delivery")
	select {
	case <-f.productionHeld:
	case <-time.After(e2eWait):
		t.Fatalf("production held no deployment request within %s", e2eWait)
	}
	trace, _ := loadToolFactsTrace(t, env, runID, nodeRunIDOfSnapshot(t, env, runID, "node_agent"))

	deploys := trace.attemptsOf(deploy.ToolName)
	if len(deploys) != 1 || deploys[0].Status != "STARTED" {
		t.Fatalf("deploy Attempts = %+v, want one STARTED Attempt", deploys)
	}
	deployment := deploys[0].Deployment
	if deployment == nil || deployment.Target == nil || deployment.Target.Service != deliveryService ||
		deployment.Target.Environment != deliveryEnvironment {
		t.Fatalf("STARTED deploy deployment = %+v, want the target %s/%s", deployment, deliveryService, deliveryEnvironment)
	}
	if deployment.Version != nil {
		t.Errorf("STARTED deploy version = %q, want none before the result commits", *deployment.Version)
	}

	control(t, f.production.URL, "release", "")
	if got := runStatusOf(env.waitForTerminal(t, runID, e2eWait)); got != "COMPLETED" {
		t.Errorf("Run status after release = %q, want COMPLETED", got)
	}
}

// TestTraceToolFacts_FieldBound_WithholdsOversizedMembersAndNamesThem proves a member
// whose encoding exceeds the configured Trace field bound is left out whole and listed in
// withheld with TOO_LARGE, while members that fit are still projected.
func TestTraceToolFacts_FieldBound_WithholdsOversizedMembersAndNamesThem(t *testing.T) {
	env := newPhotoSetEnv(t, func(_ string, opts *testEnvOptions) { opts.TraceMaxFieldBytes = 16 })
	snapshot, _, _ := runPhotoSet(t, env, "photo-set", 0)
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", got, snapshot)
	}
	runID, _ := snapshot["run"].(map[string]any)["id"].(string)
	trace, raw := loadToolFactsTrace(t, env.testEnv, runID, nodeRunIDOf(t, snapshot, "node_agent"))

	for _, attempt := range trace.attemptsOf(generateimage.ToolName) {
		if attempt.Photo != nil {
			t.Errorf("generate_image Attempt %s photo = %+v, want it withheld: the asset id exceeds 16 bytes", attempt.ID, attempt.Photo)
		}
		if reason := withheldFields(attempt)["photo.photoAssetId"]; reason != "TOO_LARGE" {
			t.Errorf("generate_image Attempt %s withheld = %+v, want photo.photoAssetId TOO_LARGE", attempt.ID, attempt.Withheld)
		}
	}
	videos := trace.attemptsOf(generatevideo.ToolName)
	if len(videos) != 1 || videos[0].Video != nil || withheldFields(videos[0])["video.videoUrl"] != "TOO_LARGE" {
		t.Errorf("generate_video Attempts = %+v, want the video URL withheld as TOO_LARGE", videos)
	}
	if strings.Contains(raw, env.providerURL+"/v1/videos/") {
		t.Errorf("agent trace Tool Attempts carry a cut or whole video URL despite the bound: %s", raw)
	}
}
