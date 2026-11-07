//go:build integration

package contract

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// workspaceAgentConfig is an agent config carrying every workspace, goal and wait-bound
// field the agent node accepts, bound to the repository the test Backend registers.
func workspaceAgentConfig() map[string]any {
	return baseAgentConfig(map[string]any{
		"workspace": map[string]any{
			"repositoryId": "vuln-demo",
			"baseCommit":   "0123456789abcdef0123456789abcdef01234567",
			"allowedPaths": []any{"go.mod", "go.sum", "internal/"},
			"deployTarget": map[string]any{"service": "orders-api", "environment": "staging"},
		},
		"goalPointer":             "/goal",
		"approvalTimeoutMs":       3600000,
		"releaseConfirmTimeoutMs": 900000,
		"voidGraceMs":             60000,
		"maxReleaseRetries":       2,
		"humanWaitTimeoutMs":      7200000,
	})
}

// agentNodeConfigOf extracts node_agent.config from a Definition response body.
func agentNodeConfigOf(t *testing.T, body []byte) map[string]any {
	t.Helper()
	def := decodeBody[map[string]any](t, body)
	nodes, ok := def["nodes"].([]any)
	if !ok {
		t.Fatalf("definition response missing nodes: %s", body)
	}
	for _, n := range nodes {
		node, _ := n.(map[string]any)
		if node["id"] == "node_agent" {
			config, _ := node["config"].(map[string]any)
			return config
		}
	}
	t.Fatalf("definition response missing node_agent: %s", body)
	return nil
}

// normaliseJSON round-trips a value through JSON so request literals (Go ints) compare
// equal to decoded response values (float64).
func normaliseJSON(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %v: %v", v, err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return out
}

// TestDefinitions_Save_AgentWorkspaceConfig_RoundTripsIntact proves the Definition API keeps
// an agent node's workspace binding, goal pointer and wait bounds exactly as submitted, in
// the Create response and in the stored immutable version.
func TestDefinitions_Save_AgentWorkspaceConfig_RoundTripsIntact(t *testing.T) {
	env := newTestEnvWithOptions(t, testEnvOptions{WorkspaceRepositories: []string{"vuln-demo"}})

	submitted := workspaceAgentConfig()
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", agentGraphRequest(submitted))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions (workspace agent) status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)

	versionResp, versionBody := env.doJSON(t, http.MethodGet,
		"/api/definitions/"+workflowID+"/versions/"+strconv.Itoa(int(version)), nil)
	if versionResp.StatusCode != http.StatusOK {
		t.Fatalf("GET definition version status = %d, want %d, body=%s", versionResp.StatusCode, http.StatusOK, versionBody)
	}

	for name, config := range map[string]map[string]any{
		"create response": agentNodeConfigOf(t, body),
		"stored version":  agentNodeConfigOf(t, versionBody),
	} {
		for _, key := range []string{"workspace", "goalPointer", "approvalTimeoutMs", "releaseConfirmTimeoutMs", "voidGraceMs", "maxReleaseRetries", "humanWaitTimeoutMs"} {
			got, present := config[key]
			if !present {
				t.Errorf("%s: node_agent.config.%s missing", name, key)
				continue
			}
			if want := normaliseJSON(t, submitted[key]); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: node_agent.config.%s = %v, want %v", name, key, got, want)
			}
		}
	}
}

// TestDefinitions_Save_AgentWorkspaceUnregisteredRepository_400 proves a workspace bound to
// a repository the Backend does not register fails semantic validation, naming the
// workspace path, so no Definition version is created for a repository no Tool can read.
func TestDefinitions_Save_AgentWorkspaceUnregisteredRepository_400(t *testing.T) {
	env := newTestEnvWithOptions(t, testEnvOptions{WorkspaceRepositories: []string{"other-repo"}})

	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", agentGraphRequest(workspaceAgentConfig()))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /api/definitions (unregistered repository) status = %d, want %d, body=%s", resp.StatusCode, http.StatusBadRequest, body)
	}
	if code := errorCode(t, body); code != "SEMANTIC_VALIDATION_FAILED" {
		t.Errorf("error.code = %q, want %q, body=%s", code, "SEMANTIC_VALIDATION_FAILED", body)
	}
	envelope := decodeBody[map[string]any](t, body)
	errBody, _ := envelope["error"].(map[string]any)
	if raw, _ := json.Marshal(errBody); !strings.Contains(string(raw), "nodes[node_agent].config.workspace") {
		t.Errorf("error does not name the workspace path: %s", body)
	}
}

// TestDefinitions_Save_AgentWorkspaceInvalidAllowedPath_400 proves an allowedPaths entry
// escaping the repository is rejected before any Definition version exists.
func TestDefinitions_Save_AgentWorkspaceInvalidAllowedPath_400(t *testing.T) {
	env := newTestEnvWithOptions(t, testEnvOptions{WorkspaceRepositories: []string{"vuln-demo"}})

	config := workspaceAgentConfig()
	config["workspace"].(map[string]any)["allowedPaths"] = []any{"../secrets"}
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", agentGraphRequest(config))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /api/definitions (escaping allowedPaths) status = %d, want %d, body=%s", resp.StatusCode, http.StatusBadRequest, body)
	}
	if code := errorCode(t, body); code != "SEMANTIC_VALIDATION_FAILED" {
		t.Errorf("error.code = %q, want %q, body=%s", code, "SEMANTIC_VALIDATION_FAILED", body)
	}
}

const authorizedTestToolName = "contract_test_release"

// reconcilableTestToolExecutor is the pollable test executor with the void and target
// queries a Tool that declares itself reconcilable must answer; the registry refuses the
// declaration without them. The catalogue test never calls either.
type reconcilableTestToolExecutor struct{ pollableTestToolExecutor }

func (reconcilableTestToolExecutor) Void(context.Context, registry.ToolAsyncState) (registry.PollResult, error) {
	return registry.PollResult{Status: registry.PollRunning}, nil
}

func (reconcilableTestToolExecutor) CheckTarget(context.Context, registry.ToolAsyncState) (registry.TargetState, error) {
	return registry.TargetState{}, nil
}

// TestAPI_ToolsCatalog_AuthorizationAndRecords_Serialised proves the Tool catalogue exposes
// the authorization, record kinds and workspace flags of a Tool that declares them, and
// serialises a Tool that declares none as LOW risk by absence with an empty records list.
func TestAPI_ToolsCatalog_AuthorizationAndRecords_Serialised(t *testing.T) {
	release := registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:                   authorizedTestToolName,
			Description:            "Production release used only by catalogue contract tests",
			InputSchema:            json.RawMessage(`{"type":"object"}`),
			OutputSchema:           json.RawMessage(`{"type":"object"}`),
			SideEffect:             domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed},
			ExecutionKind:          domain.ToolExecutionAsync,
			Poll:                   &domain.PollPolicy{IntervalMs: 2500, MaxPolls: 6},
			Authorization:          &domain.ToolAuthorization{Risk: domain.ToolRiskProduction, Reversible: false},
			Records:                []domain.ToolRecordKind{domain.ToolRecordChangeRequest},
			UsesWorkspace:          true,
			Reconcilable:           true,
			ReturnUnmetRequirement: true,
			CountedFailedFacts:     []string{"media_checked", "content_reviewed"},
		},
		Executor: reconcilableTestToolExecutor{},
	}
	env := newTestEnvWithOptions(t, testEnvOptions{ExtraToolRegistrations: []registry.ToolRegistration{release}})

	resp, body := env.doJSON(t, http.MethodGet, "/api/tools", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/tools status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	items := catalogItemsByKey(t, body, "name")

	want := map[string]map[string]string{
		authorizedTestToolName: {
			"authorization":          `{"risk":"PRODUCTION","reversible":false}`,
			"records":                `["CHANGE_REQUEST"]`,
			"usesWorkspace":          `true`,
			"reconcilable":           `true`,
			"returnUnmetRequirement": `true`,
			"countedFailedFacts":     `["media_checked","content_reviewed"]`,
		},
		"lookup": {
			"records":                `[]`,
			"usesWorkspace":          `false`,
			"reconcilable":           `false`,
			"returnUnmetRequirement": `false`,
			"countedFailedFacts":     `[]`,
		},
	}
	for name, fields := range want {
		item, ok := items[name]
		if !ok {
			t.Fatalf("GET /api/tools does not list %s: %s", name, body)
		}
		for key, expected := range fields {
			if got := compactJSON(t, string(item[key])); got != expected {
				t.Errorf("%s.%s = %s, want %s", name, key, got, expected)
			}
		}
	}
	if raw, present := items["lookup"]["authorization"]; present {
		t.Errorf("lookup.authorization = %s, want the key absent for an undeclared (LOW) authorization", raw)
	}
}
