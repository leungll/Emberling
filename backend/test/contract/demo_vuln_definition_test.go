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
