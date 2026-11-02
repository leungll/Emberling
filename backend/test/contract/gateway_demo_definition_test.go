//go:build integration

package contract

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mocktask"
)

// gatewayDemoDefinitionPath is the live Definition the task gateway demo saves by hand.
// It selects gatewayDemoModelID, which a gateway deployment must list under exactly
// that id.
const (
	gatewayDemoDefinitionPath = "../../../scripts/demo/definitions/image_generation_gateway_live.json"
	gatewayDemoModelID        = "gateway-seedream-5.0-pro"
)

// TestGatewayDemoDefinition_SavesAgainstAGatewayDeployment proves the live gateway
// Definition is accepted by a Backend whose task gateway serves its model, and refused by
// one without the gateway path, where the model does not exist. Saving dispatches nothing.
func TestGatewayDemoDefinition_SavesAgainstAGatewayDeployment(t *testing.T) {
	raw, err := os.ReadFile(gatewayDemoDefinitionPath)
	if err != nil || !json.Valid(raw) {
		t.Fatalf("read gateway demo Definition: valid JSON = %v, error = %v", json.Valid(raw), err)
	}
	save := func(env *testEnv) (*http.Response, []byte) {
		var definition map[string]any
		if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&definition); err != nil {
			t.Fatalf("decode gateway demo Definition: %v", err)
		}
		return env.doJSON(t, http.MethodPost, "/api/definitions", definition)
	}

	withGateway := newTestEnvWithOptions(t, testEnvOptions{TaskGateway: &testTaskGateway{
		ProviderID: testGatewayProviderID,
		BaseURL:    "http://taskgateway.contract-test.invalid",
		Models:     []mocktask.TaskModel{{ID: gatewayDemoModelID, DisplayName: "Seedream 5.0 Pro (gateway)"}},
	}})
	if resp, body := save(withGateway); resp.StatusCode != http.StatusCreated {
		t.Fatalf("save with the gateway path: status = %d, want 201; body=%s", resp.StatusCode, body)
	}

	withoutGateway := newTestEnv(t)
	if resp, body := save(withoutGateway); resp.StatusCode == http.StatusCreated {
		t.Fatalf("save without the gateway path: status = 201, want a refusal; body=%s", body)
	}
}
