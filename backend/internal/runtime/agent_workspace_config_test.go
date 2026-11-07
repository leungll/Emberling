package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
)

const testBaseCommit = "0123456789abcdef0123456789abcdef01234567"

// workspaceAgentConfig is a complete agent config using every workspace-related field,
// as JSON, with overrides applied on top.
func workspaceAgentConfig(overrides map[string]any) json.RawMessage {
	cfg := map[string]any{
		"modelId":   "text-model-v1",
		"maxTurns":  6,
		"timeoutMs": 600000,
		"workspace": map[string]any{
			"repositoryId": "vuln-demo",
			"baseCommit":   testBaseCommit,
			"allowedPaths": []string{"go.mod", "go.sum", "internal/auth"},
			"deployTarget": map[string]any{"service": "token-service", "environment": "production"},
		},
		"goalPointer":             "/goal",
		"approvalTimeoutMs":       300000,
		"releaseConfirmTimeoutMs": 120000,
		"voidGraceMs":             30000,
		"maxReleaseRetries":       2,
		"humanWaitTimeoutMs":      900000,
	}
	for key, value := range overrides {
		if value == nil {
			delete(cfg, key)
			continue
		}
		cfg[key] = value
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		panic(err)
	}
	return raw
}

func workspaceOverride(field string, value any) map[string]any {
	ws := map[string]any{
		"repositoryId": "vuln-demo",
		"baseCommit":   testBaseCommit,
		"allowedPaths": []string{"go.mod", "internal/auth"},
	}
	ws[field] = value
	return map[string]any{"workspace": ws}
}

func TestAgentNodeConfig_Parse_WorkspaceAndWaitBounds_Passes(t *testing.T) {
	cfg, err := ParseAgentNodeConfig(workspaceAgentConfig(nil))
	if err != nil {
		t.Fatalf("ParseAgentNodeConfig() = %v, want nil", err)
	}
	if cfg.Workspace == nil || cfg.Workspace.RepositoryID != "vuln-demo" || cfg.Workspace.BaseCommit != testBaseCommit {
		t.Fatalf("Workspace = %+v, want the declared repository and commit", cfg.Workspace)
	}
	if len(cfg.Workspace.AllowedPaths) != 3 || cfg.Workspace.AllowedPaths[2] != "internal/auth" {
		t.Errorf("AllowedPaths = %v, want the three declared paths in order", cfg.Workspace.AllowedPaths)
	}
	if cfg.Workspace.DeployTarget == nil || cfg.Workspace.DeployTarget.Service != "token-service" || cfg.Workspace.DeployTarget.Environment != "production" {
		t.Errorf("DeployTarget = %+v, want token-service/production", cfg.Workspace.DeployTarget)
	}
	if cfg.GoalPointer != "/goal" {
		t.Errorf("GoalPointer = %q, want /goal", cfg.GoalPointer)
	}
	for name, got := range map[string]*int{
		"approvalTimeoutMs": cfg.ApprovalTimeoutMs, "releaseConfirmTimeoutMs": cfg.ReleaseConfirmTimeoutMs,
		"voidGraceMs": cfg.VoidGraceMs, "maxReleaseRetries": cfg.MaxReleaseRetries, "humanWaitTimeoutMs": cfg.HumanWaitTimeoutMs,
	} {
		if got == nil || *got < 1 {
			t.Errorf("%s = %v, want the declared positive value", name, got)
		}
	}
}

func TestAgentNodeConfig_Parse_WorkspaceFieldsAbsent_StayNil(t *testing.T) {
	cfg, err := ParseAgentNodeConfig(json.RawMessage(`{"modelId":"text-model-v1","maxTurns":2,"timeoutMs":1000}`))
	if err != nil {
		t.Fatalf("ParseAgentNodeConfig() = %v, want nil", err)
	}
	if cfg.Workspace != nil || cfg.GoalPointer != "" || cfg.ApprovalTimeoutMs != nil || cfg.ReleaseConfirmTimeoutMs != nil ||
		cfg.VoidGraceMs != nil || cfg.MaxReleaseRetries != nil || cfg.HumanWaitTimeoutMs != nil {
		t.Errorf("optional workspace fields are set on a config that omitted them: %+v", cfg)
	}
}

func TestAgentNodeConfig_Parse_InvalidAllowedPaths_Fails(t *testing.T) {
	cases := []struct {
		name  string
		paths any
		want  string
	}{
		{"empty list", []string{}, "must name at least one path"},
		{"absolute path", []string{"/internal/auth"}, "must be relative"},
		{"parent segment", []string{"internal/../auth"}, `must not contain "." or ".." segments`},
		{"current segment", []string{"./internal"}, `must not contain "." or ".." segments`},
		{"repository root", []string{""}, "path is empty"},
		{"duplicate after normalisation", []string{"internal/auth/", "internal/auth"}, "repeats an earlier path"},
		{"backslash separator", []string{`internal\auth`}, `must use "/" as its separator`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAgentNodeConfig(workspaceAgentConfig(workspaceOverride("allowedPaths", tc.paths)))
			assertAgentConfigError(t, err, "workspace", tc.want)
		})
	}
}

func TestAgentNodeConfig_Parse_InvalidBaseCommit_Fails(t *testing.T) {
	for _, commit := range []string{"", "abc123", strings.ToUpper(testBaseCommit), testBaseCommit + "0", "g" + testBaseCommit[1:]} {
		_, err := ParseAgentNodeConfig(workspaceAgentConfig(workspaceOverride("baseCommit", commit)))
		assertAgentConfigError(t, err, "workspace", "baseCommit must be 40 lowercase hexadecimal digits")
	}
}

func TestAgentNodeConfig_Parse_WorkspaceWithoutRepositoryOrHalfTarget_Fails(t *testing.T) {
	_, err := ParseAgentNodeConfig(workspaceAgentConfig(workspaceOverride("repositoryId", "")))
	assertAgentConfigError(t, err, "workspace", "repositoryId is required")

	_, err = ParseAgentNodeConfig(workspaceAgentConfig(workspaceOverride("deployTarget", map[string]any{"service": "token-service", "environment": ""})))
	assertAgentConfigError(t, err, "workspace", "deployTarget needs both service and environment")
}

func TestAgentNodeConfig_Parse_NonPositiveWaitBounds_Fail(t *testing.T) {
	for _, field := range []string{"approvalTimeoutMs", "releaseConfirmTimeoutMs", "voidGraceMs", "maxReleaseRetries", "humanWaitTimeoutMs"} {
		for _, value := range []any{0, -1} {
			_, err := ParseAgentNodeConfig(workspaceAgentConfig(map[string]any{field: value}))
			assertAgentConfigError(t, err, field, field+" must be >= 1 when present")
		}
		_, err := ParseAgentNodeConfig(workspaceAgentConfig(map[string]any{field: 1.5}))
		assertAgentConfigError(t, err, field, field+" must be an integer")
	}
}

func TestAgentNodeConfig_Parse_InvalidGoalPointer_Fails(t *testing.T) {
	for _, pointer := range []string{"goal", "/goal/~2", "~"} {
		_, err := ParseAgentNodeConfig(workspaceAgentConfig(map[string]any{"goalPointer": pointer}))
		assertAgentConfigError(t, err, "goalPointer", "goalPointer is not a valid JSON Pointer")
	}
}

func assertAgentConfigError(t *testing.T, err error, field, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("ParseAgentNodeConfig() = nil, want an error on %s mentioning %q", field, want)
	}
	var ace *AgentConfigError
	if !errors.As(err, &ace) {
		t.Fatalf("ParseAgentNodeConfig() = %T %v, want *AgentConfigError", err, err)
	}
	if ace.Field != field {
		t.Errorf("Field = %q, want %q (error %v)", ace.Field, field, err)
	}
	if !strings.Contains(ace.Message, want) {
		t.Errorf("Message = %q, want it to mention %q", ace.Message, want)
	}
}

func TestResolveAgentGoal_PrepareOnlyAndFixAndDeploy_Resolved(t *testing.T) {
	cfg := AgentNodeConfig{GoalPointer: "/task/goal"}
	for _, want := range []AgentGoal{AgentGoalPrepareOnly, AgentGoalFixAndDeploy} {
		input := json.RawMessage(`{"task":{"goal":"` + string(want) + `","advisory":"GO-2025-3553"}}`)
		got, err := ResolveAgentGoal(cfg, input)
		if err != nil || got != want {
			t.Errorf("ResolveAgentGoal(%s) = %q, %v, want %q", input, got, err, want)
		}
	}
}

func TestResolveAgentGoal_PointerDoesNotResolve_ConfigError(t *testing.T) {
	cfg := AgentNodeConfig{GoalPointer: "/task/goal"}
	for _, input := range []string{`{"task":{}}`, `{"goal":"PREPARE_ONLY"}`, `{"task":["PREPARE_ONLY"]}`, `"PREPARE_ONLY"`} {
		_, err := ResolveAgentGoal(cfg, json.RawMessage(input))
		assertAgentConfigError(t, err, "goalPointer", "does not resolve in the node input")
	}
	_, err := ResolveAgentGoal(AgentNodeConfig{}, json.RawMessage(`{"goal":"PREPARE_ONLY"}`))
	assertAgentConfigError(t, err, "goalPointer", "goalPointer is not configured")
}

func TestResolveAgentGoal_ValueNotAGoal_ConfigError(t *testing.T) {
	cfg := AgentNodeConfig{GoalPointer: "/goal"}
	for _, input := range []string{`{"goal":"DEPLOY"}`, `{"goal":"prepare_only"}`, `{"goal":1}`, `{"goal":null}`, `{"goal":["FIX_AND_DEPLOY"]}`} {
		_, err := ResolveAgentGoal(cfg, json.RawMessage(input))
		assertAgentConfigError(t, err, "goalPointer", "is not one of PREPARE_ONLY, FIX_AND_DEPLOY or DELIVER_CLIP")
	}
}

func TestResolveAgentGoal_DeliverClip_Resolves(t *testing.T) {
	goal, err := ResolveAgentGoal(AgentNodeConfig{GoalPointer: "/goal"}, json.RawMessage(`{"goal":"DELIVER_CLIP"}`))
	if err != nil || goal != AgentGoalDeliverClip {
		t.Fatalf("ResolveAgentGoal() = %q, %v, want DELIVER_CLIP", goal, err)
	}
}

// workspaceDefinition is text_input -> agent -> text_output with the given agent config.
func workspaceDefinition(agentConfig json.RawMessage) domain.Definition {
	return domain.Definition{
		WorkflowID: "wf_agent_workspace",
		Nodes: []domain.Node{
			{ID: "node_in", Type: NodeTypeTextInput, Config: json.RawMessage(`{"inputKey":"task","required":true}`)},
			{ID: "node_agent", Type: NodeTypeAgent, Config: agentConfig},
			{ID: "node_out", Type: NodeTypeTextOutput, Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "node_in", SourceHandle: "text", Target: "node_agent", TargetHandle: "input"},
			{ID: "e2", Source: "node_agent", SourceHandle: "text", Target: "node_out", TargetHandle: "text"},
		},
	}
}

func newWorkspaceCompiler(repositories ...string) *Compiler {
	return NewCompiler(newFakeCatalog(), fixedClock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, WithWorkspaceRepositories(repositories))
}

func TestCompiler_AgentWorkspace_RegisteredRepository_CompilesAndKeepsConfigVerbatim(t *testing.T) {
	raw := workspaceAgentConfig(nil)
	compiled, err := newWorkspaceCompiler("other-repo", "vuln-demo").Compile(context.Background(), workspaceDefinition(raw))
	if err != nil {
		t.Fatalf("Compile() = %v, want nil", err)
	}
	var got domain.Node
	for _, node := range compiled.Definition.Nodes {
		if node.ID == "node_agent" {
			got = node
		}
	}
	if string(got.Config) != string(raw) {
		t.Errorf("compiled agent config = %s, want the submitted config verbatim %s", got.Config, raw)
	}
	cfg, err := ParseAgentNodeConfig(got.Config)
	if err != nil || cfg.Workspace == nil || cfg.GoalPointer != "/goal" || cfg.MaxReleaseRetries == nil {
		t.Errorf("compiled agent config lost workspace fields: %+v, %v", cfg, err)
	}
}

func TestCompiler_AgentWorkspace_UnregisteredRepository_Fails(t *testing.T) {
	for name, compiler := range map[string]*Compiler{
		"other repositories registered": newWorkspaceCompiler("other-repo"),
		"no repositories registered":    NewCompiler(newFakeCatalog(), fixedClock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := compiler.Compile(context.Background(), workspaceDefinition(workspaceAgentConfig(nil)))
			assertWorkspaceCompileError(t, err, "nodes[node_agent].config.workspace", `workspace.repositoryId "vuln-demo" is not a registered repository`)
		})
	}
}

func TestCompiler_AgentWorkspace_InvalidFields_FailAtSemantics(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string]any
		path      string
		want      string
	}{
		{"absolute allowed path", workspaceOverride("allowedPaths", []string{"/go.mod"}), "nodes[node_agent].config.workspace", "must be relative"},
		{"parent segment", workspaceOverride("allowedPaths", []string{"../go.mod"}), "nodes[node_agent].config.workspace", `must not contain "." or ".." segments`},
		{"duplicate path", workspaceOverride("allowedPaths", []string{"go.mod", "./go.mod/"}), "nodes[node_agent].config.workspace", `must not contain "." or ".." segments`},
		{"duplicate normalised path", workspaceOverride("allowedPaths", []string{"go.mod", "go.mod/"}), "nodes[node_agent].config.workspace", "repeats an earlier path"},
		{"short commit", workspaceOverride("baseCommit", "abc"), "nodes[node_agent].config.workspace", "baseCommit must be 40 lowercase hexadecimal digits"},
		{"zero approval timeout", map[string]any{"approvalTimeoutMs": 0}, "nodes[node_agent].config.approvalTimeoutMs", "approvalTimeoutMs must be >= 1 when present"},
		{"negative retries", map[string]any{"maxReleaseRetries": -2}, "nodes[node_agent].config.maxReleaseRetries", "maxReleaseRetries must be >= 1 when present"},
		{"relative goal pointer", map[string]any{"goalPointer": "goal"}, "nodes[node_agent].config.goalPointer", "goalPointer is not a valid JSON Pointer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newWorkspaceCompiler("vuln-demo").Compile(context.Background(), workspaceDefinition(workspaceAgentConfig(tc.overrides)))
			assertWorkspaceCompileError(t, err, tc.path, tc.want)
		})
	}
}

func assertWorkspaceCompileError(t *testing.T, err error, path, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Compile() = nil, want a semantics error at %s", path)
	}
	ce, ok := AsCompileError(err)
	if !ok {
		t.Fatalf("Compile() = %T %v, want *CompileError", err, err)
	}
	if ce.Stage != StageSemantics || len(ce.Errors) != 1 {
		t.Fatalf("Compile() stage %s errors %+v, want one error at the semantics stage", ce.Stage, ce.Errors)
	}
	got := ce.Errors[0]
	if got.Code != CodeSemanticValidationFailed || got.NodeID != "node_agent" || got.Path != path {
		t.Errorf("error = %+v, want %s on node_agent at %s", got, CodeSemanticValidationFailed, path)
	}
	if !strings.Contains(got.Message, want) {
		t.Errorf("Message = %q, want it to mention %q", got.Message, want)
	}
}
