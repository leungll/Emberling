//go:build integration

// Review crash story over the public API: the Backend stops while review_asset waits for
// the review model, so the review never returns. The restarted Backend tells the Agent the
// review was interrupted and is safe to call again; the scripted Agent reviews again, and
// exactly one asset_reviewed fact exists, recorded only by the review that returned.
package contract

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/tools/reviewasset"
)

// TestE2E_ReviewModel_CrashDuringReview_NoFactThenAgentReviewsAgainOnce holds the first
// review model request until the Backend stops. No asset_reviewed fact exists after the
// crash. A second Backend over the same database and volume hands the orphaned review back
// to the Agent as EXECUTION_INTERRUPTED with the generation count unchanged; the Agent's
// scripted trajectory calls review_asset again, that review reaches the review model and
// succeeds, and the Run completes with exactly one asset_reviewed fact bound to the review
// model.
func TestE2E_ReviewModel_CrashDuringReview_NoFactThenAgentReviewsAgainOnce(t *testing.T) {
	const runnerBefore, runnerAfter = "runner-before-review-crash", "runner-after-review-crash"
	inFlight := make(chan struct{})
	var first sync.Once
	verdict := answerVerdict(`{"passed":true,"reasons":["Keeps the subject and the warm film look."]}`)
	upstream := newVisionUpstream(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		held := false
		first.Do(func() { held = true })
		if !held {
			verdict(w, r, body)
			return
		}
		close(inFlight)
		<-r.Context().Done()
	})
	crashed := newPhotoSetEnv(t, withReviewModel(t, upstream, e2eHTTPTimeout), func(_ string, opts *testEnvOptions) {
		opts.RunnerID = runnerBefore
	})

	workflowID, version := savePhotoSetDefinition(t, crashed.testEnv, "photo-review-interrupted", 0,
		map[string]any{"reviewModelId": contractReviewModelID})
	ref := crashed.uploadPNG(t, []byte("\x89PNG\r\n\x1a\nreview-crash-photo"))
	resp, body := crashed.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version,
		"input": map[string]any{"brief": "one warm film look", "photos": []any{ref}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, body=%s", resp.StatusCode, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)

	select {
	case <-inFlight:
	case <-time.After(syncCrashInFlightWait):
		t.Fatal("the review never reached the review model")
	}
	// The process stops while the review model call is in flight.
	crashed.stop()

	var reviewAttemptID string
	if err := crashed.pool.QueryRow(context.Background(),
		`SELECT id FROM tool_attempts WHERE tool_name = $1 AND status = 'STARTED'`, reviewasset.ToolName).Scan(&reviewAttemptID); err != nil {
		t.Fatalf("read the STARTED review_asset Attempt left by the stopped Backend: %v", err)
	}
	var reviewedBefore int
	if err := crashed.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM execution_facts WHERE run_id = $1 AND fact_type = $2`, runID, reviewasset.FactType).Scan(&reviewedBefore); err != nil || reviewedBefore != 0 {
		t.Fatalf("asset_reviewed facts after the crash = %d (err %v), want none", reviewedBefore, err)
	}

	restartOpts := testEnvOptions{
		Pool: crashed.pool, AssetStorageRoot: crashed.assetRoot, MockTaskBaseURL: crashed.providerURL, RunnerID: runnerAfter,
	}
	withReviewModel(t, upstream, e2eHTTPTimeout)(crashed.providerURL, &restartOpts)
	restarted := &photoSetEnv{providerURL: crashed.providerURL, testEnv: newTestEnvWithOptions(t, restartOpts)}
	runOrphanScan(t, restarted.testEnv, runnerAfter)

	snapshot := restarted.waitForTerminal(t, runID, photoSetWait)
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED after the Agent reviewed again; snapshot=%v", got, snapshot)
	}
	status, traceBody := getAgentTrace(t, restarted.testEnv, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, traceBody)
	}
	trace := decodeBody[syncCrashTrace](t, traceBody)

	// generate, interrupted review, review again, FINAL.
	if len(trace.Turns) != 4 {
		t.Fatalf("Turns = %d, want 4: %s", len(trace.Turns), traceBody)
	}
	interrupted, retried := trace.Turns[1], trace.Turns[2]
	if interrupted.Action == nil || interrupted.Action.Status != "FAILED" || interrupted.Action.FailureSource == nil ||
		*interrupted.Action.FailureSource != "RUNNER_LOST" || interrupted.Action.Error == nil || interrupted.Action.Error.Code != "EXECUTION_INTERRUPTED" {
		t.Errorf("Turn 2 action = %+v, want FAILED with RUNNER_LOST and EXECUTION_INTERRUPTED", interrupted.Action)
	}
	if len(interrupted.ToolAttempts) != 1 || interrupted.ToolAttempts[0].ID != reviewAttemptID || interrupted.ToolAttempts[0].Status != "FAILED" {
		t.Errorf("Turn 2 attempts = %+v, want the orphaned review Attempt %s FAILED", interrupted.ToolAttempts, reviewAttemptID)
	}
	if retried.Decision == nil || retried.Decision.Tool == nil || *retried.Decision.Tool != reviewasset.ToolName ||
		retried.Action == nil || retried.Action.Status != "SUCCEEDED" {
		t.Errorf("Turn 3 = decision %+v action %+v, want the Agent's new review_asset call SUCCEEDED", retried.Decision, retried.Action)
	}

	// The Agent was told the review is safe to repeat and costs no generation.
	notices := toolStatusMessages(restarted.provider.Requests())
	if len(notices) != 1 || notices[0]["status"] != "EXECUTION_INTERRUPTED" {
		t.Fatalf("tool status messages the Agent received = %v, want one EXECUTION_INTERRUPTED", notices)
	}
	if _, counted := notices[0]["generationCalls"]; counted {
		t.Errorf("notice = %v, want no generationCalls: a review does not count toward the limit", notices[0])
	}

	var reviewed []map[string]string
	for _, fact := range trace.Facts.Items {
		if fact.FactType == reviewasset.FactType {
			reviewed = append(reviewed, fact.Bindings)
		}
	}
	if len(reviewed) != 1 || reviewed[0]["reviewModelId"] != contractReviewModelID || reviewed[0]["policyVersion"] != reviewasset.VisionPolicyVersion {
		t.Errorf("asset_reviewed facts = %v, want exactly one bound to %s under %s", reviewed, contractReviewModelID, reviewasset.VisionPolicyVersion)
	}
	if trace.GenerationBudget.GenerationCallsUsed != 1 {
		t.Errorf("generation calls used = %d, want 1: the interrupted review does not count", trace.GenerationBudget.GenerationCallsUsed)
	}
	if got := upstream.requests.Load(); got != 2 {
		t.Errorf("review model requests = %d, want 2: the interrupted one and the Agent's new review", got)
	}
	if generated, _ := restarted.providerRecord(t); len(generated) != 1 {
		t.Errorf("provider generations = %d, want 1: reviewing again generates nothing", len(generated))
	}
}
