//go:build integration

package contract

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const describedModelDescription = "Balanced general-purpose model; moderate speed."

// describedModelProvider registers one model with a description and one without, so the
// catalogue test observes both wire forms. It is never asked to Generate.
type describedModelProvider struct{}

func (describedModelProvider) Models(context.Context) ([]registry.ModelRegistration, error) {
	return []registry.ModelRegistration{
		{ModelMetadata: domain.ModelMetadata{
			ID:           "contract-test-described-model",
			DisplayName:  "Described Model",
			Description:  describedModelDescription,
			Capabilities: []string{domain.ModelCapabilityStructuredDecision},
			ConfigSchema: json.RawMessage(`{"type":"object"}`),
		}},
		{ModelMetadata: domain.ModelMetadata{
			ID:           "contract-test-undescribed-model",
			DisplayName:  "Undescribed Model",
			Capabilities: []string{domain.ModelCapabilityStructuredDecision},
			ConfigSchema: json.RawMessage(`{"type":"object"}`),
		}},
	}, nil
}

func (describedModelProvider) Generate(context.Context, registry.ModelRequest) (registry.ModelResponse, error) {
	return registry.ModelResponse{}, errors.New("describedModelProvider: Generate must not be called")
}

// TestModelsCatalog_Description_RoundTrips covers GET /models description: a registered
// description is served verbatim, a model without one omits the field rather than sending
// an empty string, and every deterministic mock model carries its fixed description.
func TestModelsCatalog_Description_RoundTrips(t *testing.T) {
	env := newTestEnvWithOptions(t, testEnvOptions{
		ExtraModelProviders: []registry.ModelProvider{describedModelProvider{}},
	})

	resp, body := env.doJSON(t, http.MethodGet, "/api/models", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/models status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	rawCatalog := decodeBody[map[string]json.RawMessage](t, body)
	rawItems := decodeBody[[]map[string]json.RawMessage](t, rawCatalog["items"])
	byID := make(map[string]map[string]json.RawMessage, len(rawItems))
	for _, item := range rawItems {
		var id string
		if err := json.Unmarshal(item["id"], &id); err != nil {
			t.Fatalf("GET /api/models item id is not a string: %v, item=%s", err, item["id"])
		}
		byID[id] = item
	}

	described, ok := byID["contract-test-described-model"]
	if !ok {
		t.Fatalf("GET /api/models does not list the described model: %s", body)
	}
	var description string
	if err := json.Unmarshal(described["description"], &description); err != nil || description != describedModelDescription {
		t.Errorf("described model description = %s, want %q", described["description"], describedModelDescription)
	}

	undescribed, ok := byID["contract-test-undescribed-model"]
	if !ok {
		t.Fatalf("GET /api/models does not list the undescribed model: %s", body)
	}
	if raw, present := undescribed["description"]; present {
		t.Errorf("undescribed model description = %s, want the field omitted", raw)
	}

	for _, id := range []string{"text-model-v1", "image-model-v1"} {
		item, ok := byID[id]
		if !ok {
			t.Fatalf("GET /api/models does not list mock model %s: %s", id, body)
		}
		var mockDescription string
		if err := json.Unmarshal(item["description"], &mockDescription); err != nil || mockDescription == "" {
			t.Errorf("mock model %s description = %s, want a non-empty string", id, item["description"])
		}
	}
}
