//go:build integration

package contract

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"testing"
)

// demoDefinition runs the vulnerability-fix demo script in its definition mode, which
// prints the Definition a story would save without touching any stack, under env.
func demoDefinition(t *testing.T, story string, env ...string) documentProcessingFixture {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is required to run the demo script: %v", tool, err)
		}
	}
	cmd := exec.Command("bash", "../../../scripts/demo/vuln-fix.sh", "definition", story)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr = exitErr.Stderr
		}
		t.Fatalf("vuln-fix.sh definition %s: %v\n%s", story, err, stderr)
	}
	var fx documentProcessingFixture
	if err := json.Unmarshal(out, &fx); err != nil {
		t.Fatalf("vuln-fix.sh printed no JSON Definition: %v\n%s", err, out)
	}
	return fx
}

// agentConfigOf returns the agent node's config of a fixture.
func agentConfigOf(t *testing.T, fx documentProcessingFixture) map[string]any {
	t.Helper()
	for _, node := range fx.Nodes {
		if node.ID != "node_agent" {
			continue
		}
		var config map[string]any
		if err := json.Unmarshal(node.Config, &config); err != nil {
			t.Fatalf("decode node_agent config: %v", err)
		}
		return config
	}
	t.Fatal("the Definition has no node_agent")
	return nil
}

// TestDemoVulnFix_RealModel_DefinitionCarriesTheModelBudget proves the demo script gives a
// real model the budget EMBERLING_DEMO_MODEL_TIMEOUT_MS names, binds the Agent to that
// model without a scripted trajectory, and that the Backend accepts the Definition it
// prints. The model id is the test Backend's own, so the save exercises the real
// validation path.
func TestDemoVulnFix_RealModel_DefinitionCarriesTheModelBudget(t *testing.T) {
	f := newVulnFixture(t)
	env := f.startBackend(t, testEnvOptions{})

	fx := demoDefinition(t, "verify", "EMBERLING_DEMO_MODEL=text-model-v1", "EMBERLING_DEMO_MODEL_TIMEOUT_MS=900000")
	config := agentConfigOf(t, fx)
	if config["timeoutMs"] != float64(900000) || config["modelId"] != "text-model-v1" {
		t.Fatalf("agent config = %v, want timeoutMs 900000 and the real model", config)
	}
	if _, scripted := config["modelConfig"]; scripted {
		t.Errorf("agent config keeps modelConfig %v, want the scripted trajectory removed", config["modelConfig"])
	}
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions (real-model demo Definition) status = %d, body=%s", resp.StatusCode, body)
	}
}

// TestDemoVulnFix_RealModel_DefaultBudgetIsThirtyMinutes pins the default real-model budget.
func TestDemoVulnFix_RealModel_DefaultBudgetIsThirtyMinutes(t *testing.T) {
	config := agentConfigOf(t, demoDefinition(t, "verify", "EMBERLING_DEMO_MODEL=some-model", "EMBERLING_DEMO_MODEL_TIMEOUT_MS="))
	if config["timeoutMs"] != float64(1800000) {
		t.Fatalf("timeoutMs = %v, want the default 1800000", config["timeoutMs"])
	}
}

// TestDemoVulnFix_Scripted_DefinitionKeepsTheFixtureBudget proves a scripted story leaves
// the fixture's budget and trajectory binding untouched, whatever the model timeout
// variable says.
func TestDemoVulnFix_Scripted_DefinitionKeepsTheFixtureBudget(t *testing.T) {
	config := agentConfigOf(t, demoDefinition(t, "restart", "EMBERLING_DEMO_MODEL=", "EMBERLING_DEMO_MODEL_TIMEOUT_MS=900000"))
	if config["timeoutMs"] != float64(300000) {
		t.Fatalf("timeoutMs = %v, want the fixture's 300000", config["timeoutMs"])
	}
	modelConfig, _ := config["modelConfig"].(map[string]any)
	if modelConfig["script"] != "vuln-fix-main-verify" {
		t.Fatalf("modelConfig = %v, want the scripted trajectory", config["modelConfig"])
	}
}

// TestDemoVulnFix_RealModelMain_DefinitionReleasesWithTheModelBudget proves the main story
// with a real model keeps the release configuration (the release Tools, the frozen deploy
// target and the release bounds), gives the model its budget and room for more Turns, and
// that the Backend accepts the Definition it prints.
func TestDemoVulnFix_RealModelMain_DefinitionReleasesWithTheModelBudget(t *testing.T) {
	// A release story's Backend registers the release Tool against a mock production service.
	env := newReleaseStory(t, releaseStoryOptions{script: "vuln-fix-release"}).env

	fx := demoDefinition(t, "main", "EMBERLING_DEMO_MODEL=text-model-v1", "EMBERLING_DEMO_MODEL_TIMEOUT_MS=900000", "EMBERLING_DEMO_APPROVAL=")
	config := agentConfigOf(t, fx)
	if config["timeoutMs"] != float64(900000) || config["modelId"] != "text-model-v1" || config["maxTurns"] != float64(24) {
		t.Fatalf("agent config = %v, want timeoutMs 900000, the real model and maxTurns 24", config)
	}
	if _, scripted := config["modelConfig"]; scripted {
		t.Errorf("agent config keeps modelConfig %v, want the scripted trajectory removed", config["modelConfig"])
	}
	assertReleaseConfig(t, config, 600000)
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions (real-model main Definition) status = %d, body=%s", resp.StatusCode, body)
	}
}

// TestDemoVulnFix_ManualApproval_DeadlineCoversTheWait proves a person deciding by hand gets
// an approval deadline that covers the whole manual wait, so the request never expires
// while the script still waits for them.
func TestDemoVulnFix_ManualApproval_DeadlineCoversTheWait(t *testing.T) {
	config := agentConfigOf(t, demoDefinition(t, "main", "EMBERLING_DEMO_MODEL=", "EMBERLING_DEMO_APPROVAL=manual", "EMBERLING_DEMO_APPROVAL_WAIT_S=3600"))
	if config["approvalTimeoutMs"] != float64(3660000) {
		t.Fatalf("approvalTimeoutMs = %v, want the 3600s wait plus a minute", config["approvalTimeoutMs"])
	}
}

// TestDemoVulnFix_ReleaseStories_DefinitionsSave proves every scripted release story binds
// its trajectory and the release configuration, and that the Backend accepts each
// Definition it prints.
func TestDemoVulnFix_ReleaseStories_DefinitionsSave(t *testing.T) {
	// A release story's Backend registers the release Tool against a mock production service.
	env := newReleaseStory(t, releaseStoryOptions{script: "vuln-fix-release"}).env

	stories := map[string]string{
		"main": "vuln-fix-release", "restart-during-approval": "vuln-fix-release", "duplicate-approval": "vuln-fix-release",
		"reject-resubmit": "vuln-fix-rejected-resubmit", "release-crash-deployed": "vuln-fix-release",
		"release-crash-voided": "vuln-fix-release", "void-exhausted": "vuln-fix-release", "target-occupied": "vuln-fix-release",
		"release-refused": "vuln-fix-release", "unhealthy": "vuln-fix-release", "prepare-only": "vuln-fix-release",
		"release-unreachable-stop": "vuln-fix-release", "stop-then-deployed": "vuln-fix-release",
	}
	for story, script := range stories {
		t.Run(story, func(t *testing.T) {
			fx := demoDefinition(t, story, "EMBERLING_DEMO_MODEL=", "EMBERLING_DEMO_APPROVAL=")
			config := agentConfigOf(t, fx)
			modelConfig, _ := config["modelConfig"].(map[string]any)
			if modelConfig["script"] != script || config["timeoutMs"] != float64(300000) {
				t.Fatalf("agent config = %v, want script %s and the fixture's budget", config, script)
			}
			assertReleaseConfig(t, config, 600000)
			resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
				"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
			})
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("POST /api/definitions (%s) status = %d, body=%s", story, resp.StatusCode, body)
			}
		})
	}
}

// TestDemoVulnFix_StopStories_ConfirmDeadlineHandsTheReleaseToAPerson proves the two stop
// stories give the release a confirm deadline short enough for the human wait to open
// while the audience watches, a human wait long enough for the script to stop it, and a
// void grace for the operation production never received.
func TestDemoVulnFix_StopStories_ConfirmDeadlineHandsTheReleaseToAPerson(t *testing.T) {
	for _, story := range []string{"release-unreachable-stop", "stop-then-deployed"} {
		config := agentConfigOf(t, demoDefinition(t, story, "EMBERLING_DEMO_MODEL=", "EMBERLING_DEMO_APPROVAL="))
		if config["releaseConfirmTimeoutMs"] != float64(15000) || config["humanWaitTimeoutMs"] != float64(600000) ||
			config["voidGraceMs"] != float64(10000) {
			t.Errorf("%s release bounds = confirm %v human %v grace %v, want 15000, 600000 and 10000", story,
				config["releaseConfirmTimeoutMs"], config["humanWaitTimeoutMs"], config["voidGraceMs"])
		}
	}
	main := agentConfigOf(t, demoDefinition(t, "main", "EMBERLING_DEMO_MODEL=", "EMBERLING_DEMO_APPROVAL="))
	if main["releaseConfirmTimeoutMs"] != float64(120000) {
		t.Errorf("main releaseConfirmTimeoutMs = %v, want 120000: no human wait opens by itself", main["releaseConfirmTimeoutMs"])
	}
}

// TestDemoVulnFix_IncompleteFinal_DefinitionSaves proves the incomplete-final story binds
// its trajectory to the repair Definition without the release Tools, and that the Backend
// accepts it.
func TestDemoVulnFix_IncompleteFinal_DefinitionSaves(t *testing.T) {
	f := newVulnFixture(t)
	env := f.startBackend(t, testEnvOptions{})
	fx := demoDefinition(t, "incomplete-final", "EMBERLING_DEMO_MODEL=", "EMBERLING_DEMO_APPROVAL=")
	config := agentConfigOf(t, fx)
	modelConfig, _ := config["modelConfig"].(map[string]any)
	if modelConfig["script"] != "vuln-fix-incomplete-final" || config["timeoutMs"] != float64(300000) {
		t.Fatalf("agent config = %v, want script vuln-fix-incomplete-final and the fixture's budget", config)
	}
	tools, _ := config["allowedTools"].([]any)
	for _, tool := range tools {
		if tool == "deploy_change" {
			t.Errorf("allowedTools = %v, want no release Tool for a repair story", tools)
		}
	}
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions (incomplete-final) status = %d, body=%s", resp.StatusCode, body)
	}
}

// assertReleaseConfig checks the release Tools, the frozen deploy target and the approval
// deadline of a release story's agent config.
func assertReleaseConfig(t *testing.T, config map[string]any, approvalTimeoutMs float64) {
	t.Helper()
	tools, _ := config["allowedTools"].([]any)
	counts := map[any]int{}
	for _, tool := range tools {
		counts[tool]++
	}
	if counts["create_change_request"] != 1 || counts["deploy_change"] != 1 {
		t.Errorf("allowedTools = %v, want create_change_request and deploy_change once each", tools)
	}
	workspace, _ := config["workspace"].(map[string]any)
	target, _ := workspace["deployTarget"].(map[string]any)
	if target["service"] != "token-service" || target["environment"] != "staging" {
		t.Errorf("deployTarget = %v, want token-service/staging", workspace["deployTarget"])
	}
	if config["approvalTimeoutMs"] != approvalTimeoutMs || config["maxReleaseRetries"] != float64(1) {
		t.Errorf("release bounds = approval %v retries %v, want %v and 1", config["approvalTimeoutMs"], config["maxReleaseRetries"], approvalTimeoutMs)
	}
}
