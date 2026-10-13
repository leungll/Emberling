//go:build integration

package contract

import (
	"net/http"
	"testing"
)

// TestAPI_NodeRunDetail_AttemptUncertain_ProjectsRecordedDetermination: an image dispatch
// refused at dial time is a failure known not to have reached the Provider, so its
// Attempt reports uncertain false; a succeeded Attempt reports null. The key is present
// on every Attempt.
func TestAPI_NodeRunDetail_AttemptUncertain_ProjectsRecordedDetermination(t *testing.T) {
	env := newTestEnvWithOptions(t, testEnvOptions{
		MockTaskBaseURL: "http://mockprovider:9101",
		TaskClient:      &http.Client{Transport: redactingTaskTransport{}},
	})

	runID := env.createAIGCRun(t, "a small ember creature")
	env.waitForTerminal(t, runID, e2eWait)

	image := env.nodeRunOf(t, runID, "image")
	imageNodeRunID, _ := image["id"].(string)
	detail, body := env.nodeRunDetail(t, runID, imageNodeRunID)
	attempts := attemptsOf(t, detail)
	if len(attempts) != 1 {
		t.Fatalf("image Attempts = %d, want 1: %s", len(attempts), body)
	}
	if value, present := attempts[0]["uncertain"]; !present || value != false {
		t.Errorf("failed image Attempt uncertain = %v (present %v), want false", value, present)
	}

	workflowID, version := createFixtureDefinition(t, env)
	okRunID := env.createCompletedFixtureRun(t, workflowID, version)
	for _, nodeID := range []string{"node_input", "node_summary"} {
		nr := env.nodeRunOf(t, okRunID, nodeID)
		nrID, _ := nr["id"].(string)
		okDetail, okBody := env.nodeRunDetail(t, okRunID, nrID)
		for _, attempt := range attemptsOf(t, okDetail) {
			if value, present := attempt["uncertain"]; !present || value != nil {
				t.Errorf("succeeded Attempt uncertain = %v (present %v), want null: %s", value, present, okBody)
			}
		}
	}
}
