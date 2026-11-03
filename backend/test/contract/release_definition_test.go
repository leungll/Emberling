//go:build integration

package contract

import (
	"net/http"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/deployrelease"
)

// releaseBoundFields are the release bounds a node allowing a release Tool must set.
var releaseBoundFields = []string{"releaseConfirmTimeoutMs", "voidGraceMs", "maxReleaseRetries", "humanWaitTimeoutMs"}

func releaseAgentConfig(omit string) map[string]any {
	cfg := map[string]any{
		"allowedTools":            []string{deployrelease.ToolName},
		"goalPointer":             "/goal",
		"approvalTimeoutMs":       3600000,
		"releaseConfirmTimeoutMs": 600000,
		"voidGraceMs":             60000,
		"maxReleaseRetries":       1,
		"humanWaitTimeoutMs":      1800000,
	}
	delete(cfg, omit)
	return baseAgentConfig(cfg)
}

// TestDefinitions_Save_ReleaseToolWithoutReleaseBounds_400 proves a Definition that allows a
// Tool occupying a release target must set every release bound, since the Runtime
// substitutes none of them; each missing bound is named in its own error.
func TestDefinitions_Save_ReleaseToolWithoutReleaseBounds_400(t *testing.T) {
	env := newTestEnvWithOptions(t, testEnvOptions{
		ExtraToolRegistrations: []registry.ToolRegistration{deployrelease.Registration("http://production.invalid", nil)},
	})
	for _, field := range releaseBoundFields {
		resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", agentGraphRequest(releaseAgentConfig(field)))
		if resp.StatusCode != http.StatusBadRequest || errorCode(t, body) != "SEMANTIC_VALIDATION_FAILED" {
			t.Errorf("without %s: status = %d %s, want 400 SEMANTIC_VALIDATION_FAILED", field, resp.StatusCode, body)
			continue
		}
		if !strings.Contains(string(body), "config."+field) {
			t.Errorf("without %s: body %s does not name the missing field", field, body)
		}
	}

	none := baseAgentConfig(map[string]any{
		"allowedTools": []string{deployrelease.ToolName}, "goalPointer": "/goal", "approvalTimeoutMs": 3600000,
	})
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", agentGraphRequest(none))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("without any release bound: status = %d %s, want 400", resp.StatusCode, body)
	}
	for _, field := range releaseBoundFields {
		if !strings.Contains(string(body), "config."+field) {
			t.Errorf("without any release bound: body %s does not name %s", body, field)
		}
	}

	resp, body = env.doJSON(t, http.MethodPost, "/api/definitions", agentGraphRequest(releaseAgentConfig("")))
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("every release bound set: status = %d %s, want 201", resp.StatusCode, body)
	}
}
