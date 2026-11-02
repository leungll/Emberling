//go:build integration

// Workspace tests: an Agent Run frozen to a repository binding reads and writes code
// through the workspace Tools, every accepted write becomes one cumulative patch row, and
// the workspace is rebuilt from those rows alone. They prove, against PostgreSQL and a real
// local git repository created for each test, that a write outside the allowed paths is
// handed back to the Agent before any Attempt exists, that the result transaction refuses a
// patch whose digest, size or touched paths the Runtime cannot verify, that a restarted
// process rebuilds the same workspace, that a crashed write is interrupted and retried, and
// that the binding commits with the Agent Run or not at all.
//
// They share the agentHarness of agent_loop_test.go.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/workspace"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const (
	wsRepositoryID = "vuln-demo"
	wsTokenPath    = "internal/auth/token.go"
	wsClaimsPath   = "internal/auth/claims.go"
	wsBaseToken    = "package auth\n\nconst Version = 3\n"
	wsNewToken     = "package auth\n\nconst Version = 4\n"
	wsClaimsSource = "package auth\n\ntype Claims struct{}\n"
	// wsSecretContent is written in every refused call: a tool message or error that
	// repeats it has leaked the call's arguments.
	wsSecretContent = "secret-file-content-7c1e"
	wsMaxTurns      = 8
	wsDoubleTool    = "fake_write"
)

// workspaceRepo is a git repository created for one test, with the files the Agent reads
// and writes and a symbolic link that resolves to the parent of any checkout.
type workspaceRepo struct {
	path   string
	commit string
}

func wsGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@localhost", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, stderr.String())
	}
	return strings.TrimSpace(out.String())
}

func newWorkspaceRepo(t *testing.T) *workspaceRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is required: %v", err)
	}
	dir := t.TempDir()
	wsGit(t, dir, "init", "-q")
	for rel, content := range map[string]string{
		"go.mod":    "module example.com/fixture\n\ngo 1.22\n",
		"README.md": "# fixture\n",
		wsTokenPath: wsBaseToken,
	} {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../../..", filepath.Join(dir, "internal", "auth", "escape")); err != nil {
		t.Fatal(err)
	}
	wsGit(t, dir, "add", "-A")
	wsGit(t, dir, "commit", "-q", "-m", "base")
	return &workspaceRepo{path: dir, commit: wsGit(t, dir, "rev-parse", "HEAD")}
}

// workspaceHarnessOptions selects what one Backend image registers. pool and clock make a
// second harness a restarted process over the same committed facts.
type workspaceHarnessOptions struct {
	repo          *workspaceRepo
	pool          *pgxpool.Pool
	clock         *execClock
	writeExecutor registry.ToolExecutor
	extraTools    []registry.ToolRegistration
	wrapUoW       func(store.UnitOfWork) store.UnitOfWork
}

func newWorkspaceHarness(t *testing.T, opts workspaceHarnessOptions) *agentHarness {
	t.Helper()
	repos := workspace.NewLocalGit(map[string]string{wsRepositoryID: opts.repo.path})
	tools := workspace.Registrations(repos)
	if opts.writeExecutor != nil {
		for i := range tools {
			if tools[i].Metadata.Name == workspace.WriteFileToolName {
				tools[i].Executor = opts.writeExecutor
			}
		}
	}
	tools = append(tools, opts.extraTools...)
	return newAgentHarness(t, agentHarnessOptions{
		Tools: tools, Pool: opts.pool, Clock: opts.clock, WrapUoW: opts.wrapUoW,
		WorkspaceRepositories: []string{wsRepositoryID},
	})
}

// workspaceDefinition is the fixture graph whose Agent is bound to the repository at
// commit, may change go.mod and internal/auth, and may call the given Tools. withWorkspace
// false leaves the binding out, which is the configuration defect one test needs.
func workspaceDefinition(workflowID, commit string, withWorkspace bool, tools ...string) domain.Definition {
	config := map[string]any{
		"instructions": agentInstructions,
		"modelId":      mockmodel.ModelID,
		"allowedTools": tools,
		"maxTurns":     wsMaxTurns,
		"timeoutMs":    120000,
	}
	if withWorkspace {
		config["workspace"] = map[string]any{
			"repositoryId": wsRepositoryID,
			"baseCommit":   commit,
			"allowedPaths": []string{"go.mod", "internal/auth/"},
			"deployTarget": map[string]string{"service": "orders-api", "environment": "staging"},
		}
	}
	encoded, _ := json.Marshal(config)
	def := agentLoopDefinitionMaxTurns(workflowID, wsMaxTurns)
	for i, node := range def.Nodes {
		if node.ID == "node_agent" {
			def.Nodes[i].Config = encoded
		}
	}
	return def
}

var wsAllTools = []string{workspace.ReadFileToolName, workspace.ListFilesToolName, workspace.WriteFileToolName}

func wsCall(tool string, args map[string]any) mockmodel.Scenario {
	encoded, _ := json.Marshal(args)
	return mockmodel.Scenario{ToolName: tool, ToolArguments: encoded}
}

func wsWrite(path, content string) mockmodel.Scenario {
	return wsCall(workspace.WriteFileToolName, map[string]any{"path": path, "content": content})
}

func wsRead(path string) mockmodel.Scenario {
	return wsCall(workspace.ReadFileToolName, map[string]any{"path": path})
}

// startWorkspaceRun scripts the TOOL_CALLs (the model answers FINAL once they are used up),
// saves def, creates the Run and claims its Agent NodeRun.
func startWorkspaceRun(h *agentHarness, def domain.Definition, calls ...mockmodel.Scenario) (domain.Run, service.AdvanceOutcome) {
	h.t.Helper()
	factScriptToolCalls(h, calls...)
	saved := h.saveDefinition(def)
	run := h.createRun(saved.WorkflowID, saved.Version, `{"question":"`+agentQuestion+`"}`)
	return run, h.claimAgentNode(run.ID)
}

func wsActionOfTurn(t *testing.T, h *agentHarness, agentRunID string, turnNo int) domain.AgentAction {
	t.Helper()
	turn := agentTurnByNo(h.ctx, t, h.uow, agentRunID, turnNo)
	action, found := agentActionOfTurn(h.ctx, t, h.uow, turn.ID)
	if !found {
		t.Fatalf("no action exists for turn %d", turnNo)
	}
	return action
}

func wsPatches(t *testing.T, h *agentHarness, agentRunID string) []domain.WorkspacePatch {
	t.Helper()
	var patches []domain.WorkspacePatch
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		patches, err = tx.WorkspacePatches().ListSummariesByAgentRun(ctx, agentRunID, 100)
		return err
	}); err != nil {
		t.Fatalf("list patches: %v", err)
	}
	return patches
}

func wsLatestPatch(t *testing.T, h *agentHarness, agentRunID string) *domain.WorkspacePatch {
	t.Helper()
	var latest *domain.WorkspacePatch
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		latest, err = tx.WorkspacePatches().Latest(ctx, agentRunID)
		return err
	}); err != nil {
		t.Fatalf("latest patch: %v", err)
	}
	return latest
}

func wsWorkspaceRow(t *testing.T, h *agentHarness, agentRunID string) (domain.AgentWorkspace, bool) {
	t.Helper()
	var ws domain.AgentWorkspace
	err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		ws, err = tx.AgentWorkspaces().GetByAgentRunID(ctx, agentRunID)
		return err
	})
	if errors.Is(err, domain.ErrNotFound) {
		return domain.AgentWorkspace{}, false
	}
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	return ws, true
}

// wsToolMessage returns the decoded tool message the round of Context Version version
// appended, after checking it links to the Action.
func wsToolMessage(t *testing.T, h *agentHarness, agentRunID string, version int, actionID string) map[string]any {
	t.Helper()
	messages := agentContextMessages(t, agentContextVersion(h.ctx, t, h.uow, agentRunID, version).Messages)
	if len(messages) < 2 {
		t.Fatalf("context version %d has %d messages, want the Decision and the tool message", version, len(messages))
	}
	tool := messages[len(messages)-1]
	if tool.Role != runtime.AgentRoleTool || tool.ToolActionID == nil || *tool.ToolActionID != actionID {
		t.Fatalf("last message = role %s action %v, want the tool message of action %s", tool.Role, tool.ToolActionID, actionID)
	}
	var content map[string]any
	if err := json.Unmarshal(tool.Content, &content); err != nil {
		t.Fatalf("decode tool message %s: %v", tool.Content, err)
	}
	return content
}

// assertScopeDenied proves the write Action of Turn turnNo was handed back as SCOPE_DENIED
// in its claim transaction: FAILED with the code, no Attempt, AGENT_ACTION_FAILED with
// returnedToAgent and the AUTHORIZATION source, a REJECTED tool message that does not
// repeat the content, no patch row, and exactly one new Turn after it.
func assertScopeDenied(t *testing.T, h *agentHarness, run domain.Run, outcome service.AdvanceOutcome, turnNo int) {
	t.Helper()
	agentRunID := factAgentRunID(t, h, outcome)
	action := wsActionOfTurn(t, h, agentRunID, turnNo)
	if action.Status != domain.AgentActionFailed || action.Error == nil || action.Error.Code != string(domain.ReturnScopeDenied) {
		t.Fatalf("write action = status %s error %+v, want FAILED with SCOPE_DENIED", action.Status, action.Error)
	}
	if attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID); len(attempts) != 0 {
		t.Errorf("tool attempts of the refused write = %+v, want none", attempts)
	}
	if patches := wsPatches(t, h, agentRunID); len(patches) != 0 {
		t.Errorf("workspace patches after the refused write = %+v, want none", patches)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	if started := agentPayloadsFor(t, events, domain.EventAgentActionStarted, "actionId", action.ID); len(started) != 0 {
		t.Errorf("AGENT_ACTION_STARTED for the refused write = %v, want none", started)
	}
	failed := agentOnlyPayloadFor(t, events, domain.EventAgentActionFailed, "actionId", action.ID)
	if failed["returnedToAgent"] != true || failed["failureSource"] != string(domain.FailureAuthorization) {
		t.Errorf("AGENT_ACTION_FAILED = %v, want returnedToAgent with the AUTHORIZATION source", failed)
	}
	message := wsToolMessage(t, h, agentRunID, turnNo, action.ID)
	if message["status"] != "REJECTED" || message["code"] != string(domain.ReturnScopeDenied) {
		t.Errorf("tool message = %v, want REJECTED SCOPE_DENIED", message)
	}
	if text, _ := message["message"].(string); strings.Contains(text, wsSecretContent) {
		t.Errorf("tool message repeats the file content: %s", text)
	}
	if !agentTurnNoExists(h.ctx, t, h.uow, agentRunID, turnNo+1) {
		t.Errorf("no Turn %d follows the refused write", turnNo+1)
	}
	if agentTurnNoExists(h.ctx, t, h.uow, agentRunID, turnNo+2) {
		t.Errorf("more than one Turn follows the refused write")
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Errorf("agent termination = %s, want FINAL_RESPONSE after the Agent answered in the next Turn", agentTermination(agentRun))
	}
}

// ---------------------------------------------------------------------------
// Scope refusals at claim
// ---------------------------------------------------------------------------

func TestWorkspaceWrite_OutsideAllowedPaths_ReturnedAsScopeDeniedWithoutAttemptOrPatch(t *testing.T) {
	repo := newWorkspaceRepo(t)
	h := newWorkspaceHarness(t, workspaceHarnessOptions{repo: repo})
	run, outcome := startWorkspaceRun(h, workspaceDefinition("wf-ws-scope", repo.commit, true, wsAllTools...),
		wsWrite("README.md", wsSecretContent))
	h.execute(outcome)
	assertScopeDenied(t, h, run, outcome, 1)
	if content, _ := os.ReadFile(filepath.Join(repo.path, "README.md")); string(content) != "# fixture\n" {
		t.Errorf("the registered repository was modified")
	}
}

func TestWorkspaceWrite_ParentSegmentPath_ReturnedAsScopeDenied(t *testing.T) {
	repo := newWorkspaceRepo(t)
	h := newWorkspaceHarness(t, workspaceHarnessOptions{repo: repo})
	run, outcome := startWorkspaceRun(h, workspaceDefinition("wf-ws-parent", repo.commit, true, wsAllTools...),
		wsWrite("internal/auth/../../main.go", wsSecretContent))
	h.execute(outcome)
	assertScopeDenied(t, h, run, outcome, 1)
}

func TestWorkspaceWrite_AbsolutePath_ReturnedAsScopeDenied(t *testing.T) {
	repo := newWorkspaceRepo(t)
	h := newWorkspaceHarness(t, workspaceHarnessOptions{repo: repo})
	run, outcome := startWorkspaceRun(h, workspaceDefinition("wf-ws-absolute", repo.commit, true, wsAllTools...),
		wsWrite("/internal/auth/token.go", wsSecretContent))
	h.execute(outcome)
	assertScopeDenied(t, h, run, outcome, 1)
}

// TestWorkspaceWrite_SymlinkEscape_ExecutorRefusesWithoutPatch covers a path that is inside
// the allowed paths as written but resolves, through a symbolic link the repository
// carries, outside the checkout. The claim cannot see the link, so the executor refuses:
// the call fails, nothing is written anywhere and no patch is recorded.
func TestWorkspaceWrite_SymlinkEscape_ExecutorRefusesWithoutPatch(t *testing.T) {
	repo := newWorkspaceRepo(t)
	h := newWorkspaceHarness(t, workspaceHarnessOptions{repo: repo})
	escaped := "escaped-" + filepath.Base(repo.path) + ".txt"
	run, outcome := startWorkspaceRun(h, workspaceDefinition("wf-ws-symlink", repo.commit, true, wsAllTools...),
		wsWrite("internal/auth/escape/"+escaped, wsSecretContent))
	h.execute(outcome)

	agentRunID := factAgentRunID(t, h, outcome)
	action := wsActionOfTurn(t, h, agentRunID, 1)
	if action.Status != domain.AgentActionFailed || action.Error == nil || action.Error.Code != "TOOL_ERROR" {
		t.Fatalf("write action = status %s error %+v, want FAILED with TOOL_ERROR", action.Status, action.Error)
	}
	if !strings.Contains(action.Error.Message, "outside the checkout") {
		t.Errorf("action error = %q, want the executor's refusal", action.Error.Message)
	}
	if patches := wsPatches(t, h, agentRunID); len(patches) != 0 {
		t.Errorf("workspace patches = %+v, want none", patches)
	}
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "**", escaped))
	if len(matches) != 0 {
		t.Errorf("a file was written outside a checkout: %v", matches)
	}
	if got := agentRunRow(h.ctx, t, h.uow, run.ID).Status; got != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", got)
	}
}

// ---------------------------------------------------------------------------
// Result transaction verification of the patch
// ---------------------------------------------------------------------------

// patchDouble is a workspace-writing Tool whose executor returns whatever patch the test
// chose, so the result transaction's own verification is what stands between a defective
// executor and a recorded patch.
type patchDouble struct {
	patch registry.WorkspacePatch
}

func (d *patchDouble) Execute(context.Context, registry.ToolAction) (registry.ToolExecutionResult, error) {
	patch := d.patch
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{
		Output: json.RawMessage(`{"ok":true}`), WorkspacePatch: &patch,
	}}, nil
}

func patchDoubleRegistration(executor registry.ToolExecutor) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:               wsDoubleTool,
			Description:        "A workspace writer whose patch the test chooses",
			InputSchema:        json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`),
			OutputSchema:       json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`),
			SideEffect:         domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			ExecutionKind:      domain.ToolExecutionSync,
			Records:            []domain.ToolRecordKind{domain.ToolRecordWorkspacePatch},
			UsesWorkspace:      true,
			WorkspaceWritePath: "/path",
		},
		Executor: executor,
	}
}

func wsPatchFor(path, added string) string {
	return "diff --git a/" + path + " b/" + path + "\nnew file mode 100644\n--- /dev/null\n+++ b/" + path + "\n@@ -0,0 +1 @@\n+" + added + "\n"
}

func wsVerifiedPatch(text string) registry.WorkspacePatch {
	return registry.WorkspacePatch{Patch: text, PatchDigest: domain.WorkspacePatchDigest(text), TouchedPaths: domain.WorkspacePatchTouchedPaths(text)}
}

// assertPatchRejected proves the double's Action failed as an invalid workspace patch in
// the result transaction: Attempt and Action FAILED with WORKSPACE_PATCH_INVALID, no
// patch row, the Agent ended as TOOL_ERROR.
func assertPatchRejected(t *testing.T, h *agentHarness, run domain.Run, outcome service.AdvanceOutcome, wantReason string) {
	t.Helper()
	agentRunID := factAgentRunID(t, h, outcome)
	action := wsActionOfTurn(t, h, agentRunID, 1)
	if action.Status != domain.AgentActionFailed || action.Error == nil || action.Error.Code != "WORKSPACE_PATCH_INVALID" {
		t.Fatalf("action = status %s error %+v, want FAILED with WORKSPACE_PATCH_INVALID", action.Status, action.Error)
	}
	if !strings.Contains(action.Error.Message, wantReason) {
		t.Errorf("action error = %q, want it to mention %q", action.Error.Message, wantReason)
	}
	attempt := agentOnlyToolAttempt(t, h, action.ID)
	if attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != "WORKSPACE_PATCH_INVALID" || len(attempt.Result) != 0 {
		t.Errorf("attempt = %+v, want FAILED with WORKSPACE_PATCH_INVALID and no result", attempt)
	}
	if patches := wsPatches(t, h, agentRunID); len(patches) != 0 {
		t.Errorf("workspace patches = %+v, want none", patches)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationToolError {
		t.Errorf("agent termination = %s, want TOOL_ERROR", agentTermination(agentRun))
	}
	if got := agentRunRow(h.ctx, t, h.uow, run.ID).Status; got != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", got)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	failed := agentOnlyPayloadFor(t, events, domain.EventAgentActionFailed, "actionId", action.ID)
	if failed["failureSource"] != string(domain.FailureSyncExecution) {
		t.Errorf("AGENT_ACTION_FAILED failureSource = %v, want SYNC_EXECUTION", failed["failureSource"])
	}
}

func startDoubleRun(t *testing.T, workflowID string, patch registry.WorkspacePatch) (*agentHarness, domain.Run, service.AdvanceOutcome) {
	t.Helper()
	repo := newWorkspaceRepo(t)
	h := newWorkspaceHarness(t, workspaceHarnessOptions{repo: repo, extraTools: []registry.ToolRegistration{patchDoubleRegistration(&patchDouble{patch: patch})}})
	run, outcome := startWorkspaceRun(h, workspaceDefinition(workflowID, repo.commit, true, wsDoubleTool),
		wsCall(wsDoubleTool, map[string]any{"path": wsClaimsPath, "content": "x"}))
	h.execute(outcome)
	return h, run, outcome
}

func TestWorkspacePatch_ExecutorTouchesPathOutsideAllowed_ResultTransactionRejects(t *testing.T) {
	// The declared path passes the claim; the patch the executor returns touches README.md.
	h, run, outcome := startDoubleRun(t, "wf-ws-double-scope", wsVerifiedPatch(wsPatchFor("README.md", "pwned")))
	assertPatchRejected(t, h, run, outcome, "outside the allowed paths")
}

func TestWorkspacePatch_DigestMismatch_ResultTransactionRejects(t *testing.T) {
	patch := wsVerifiedPatch(wsPatchFor(wsClaimsPath, "package auth"))
	patch.PatchDigest = strings.Repeat("0", 64)
	h, run, outcome := startDoubleRun(t, "wf-ws-double-digest", patch)
	assertPatchRejected(t, h, run, outcome, "digest")
}

func TestWorkspacePatch_TouchedPathsDisagreeWithText_ResultTransactionRejects(t *testing.T) {
	patch := wsVerifiedPatch(wsPatchFor(wsClaimsPath, "package auth"))
	patch.TouchedPaths = []string{"go.mod"}
	h, run, outcome := startDoubleRun(t, "wf-ws-double-touched", patch)
	assertPatchRejected(t, h, run, outcome, "touched paths")
}

func TestWorkspacePatch_OverSizeBound_ServiceRejectsBeforeStore(t *testing.T) {
	text := wsPatchFor(wsClaimsPath, strings.Repeat("x", domain.MaxWorkspacePatchBytes))
	h, run, outcome := startDoubleRun(t, "wf-ws-double-size", wsVerifiedPatch(text))
	assertPatchRejected(t, h, run, outcome, "over the")
}

// ---------------------------------------------------------------------------
// Write, read, restart
// ---------------------------------------------------------------------------

func TestWorkspace_WriteThenRead_ReadsNewContentWithIncrementedPatchSeq(t *testing.T) {
	repo := newWorkspaceRepo(t)
	h := newWorkspaceHarness(t, workspaceHarnessOptions{repo: repo})
	run, outcome := startWorkspaceRun(h, workspaceDefinition("wf-ws-write-read", repo.commit, true, wsAllTools...),
		wsRead(wsTokenPath), wsWrite(wsTokenPath, wsNewToken), wsRead(wsTokenPath), wsWrite(wsClaimsPath, wsClaimsSource), wsRead(wsClaimsPath))
	h.execute(outcome)
	agentRunID := factAgentRunID(t, h, outcome)

	ws, ok := wsWorkspaceRow(t, h, agentRunID)
	if !ok {
		t.Fatalf("the Agent Run froze no workspace")
	}
	if ws.RepositoryID != wsRepositoryID || ws.BaseCommit != repo.commit || strings.Join(ws.AllowedPaths, ",") != "go.mod,internal/auth" ||
		ws.DeployTarget == nil || ws.DeployTarget.Service != "orders-api" {
		t.Errorf("frozen workspace = %+v, want the Definition's binding with normalised allowed paths", ws)
	}

	readResult := func(turnNo int) map[string]any {
		attempt := agentOnlyToolAttempt(t, h, wsActionOfTurn(t, h, agentRunID, turnNo).ID)
		if attempt.Status != domain.ToolAttemptSucceeded {
			t.Fatalf("turn %d attempt = %+v, want SUCCEEDED", turnNo, attempt)
		}
		var out map[string]any
		if err := json.Unmarshal(attempt.Result, &out); err != nil {
			t.Fatalf("decode result of turn %d: %v", turnNo, err)
		}
		return out
	}
	if r := readResult(1); r["content"] != wsBaseToken || r["patchSeq"] != float64(0) || r["path"] != wsTokenPath {
		t.Errorf("first read = %v, want the base content at patchSeq 0", r)
	}
	if r := readResult(3); r["content"] != wsNewToken || r["patchSeq"] != float64(1) {
		t.Errorf("read after the first write = %v, want the new content at patchSeq 1", r)
	}
	if r := readResult(5); r["content"] != wsClaimsSource || r["patchSeq"] != float64(2) {
		t.Errorf("read after the second write = %v, want the new file at patchSeq 2", r)
	}

	patches := wsPatches(t, h, agentRunID)
	if len(patches) != 2 || patches[0].Seq != 1 || patches[1].Seq != 2 {
		t.Fatalf("patches = %+v, want seq 1 and 2", patches)
	}
	if strings.Join(patches[0].TouchedPaths, ",") != wsTokenPath || strings.Join(patches[1].TouchedPaths, ",") != wsClaimsPath+","+wsTokenPath {
		t.Errorf("touched paths = %v / %v, want the token file, then both files", patches[0].TouchedPaths, patches[1].TouchedPaths)
	}
	for i, turnNo := range []int{2, 4} {
		attempt := agentOnlyToolAttempt(t, h, wsActionOfTurn(t, h, agentRunID, turnNo).ID)
		if patches[i].ToolAttemptID != attempt.ID {
			t.Errorf("patch %d names attempt %s, want the write attempt %s of turn %d", patches[i].Seq, patches[i].ToolAttemptID, attempt.ID, turnNo)
		}
		var out map[string]any
		_ = json.Unmarshal(attempt.Result, &out)
		if out["patchDigest"] != patches[i].PatchDigest {
			t.Errorf("write result of turn %d reports digest %v, want the recorded %s", turnNo, out["patchDigest"], patches[i].PatchDigest)
		}
	}
	latest := wsLatestPatch(t, h, agentRunID)
	if latest == nil || latest.PatchDigest != domain.WorkspacePatchDigest(latest.Patch) || !strings.Contains(latest.Patch, "const Version = 4") || !strings.Contains(latest.Patch, "type Claims struct") {
		t.Errorf("latest patch = %+v, want the cumulative diff of both writes", latest)
	}

	// The Trace projection carries the binding and the summaries, never the text.
	query := service.NewQueryService(service.Deps{UoW: h.uow})
	trace, err := query.AgentTrace(h.ctx, run.ID, outcome.NodeRunID)
	if err != nil {
		t.Fatalf("agent trace: %v", err)
	}
	if trace.Workspace == nil || trace.Workspace.Workspace.BaseCommit != repo.commit || len(trace.Workspace.Patches) != 2 || trace.Workspace.PatchesTruncated {
		t.Fatalf("trace workspace = %+v, want the binding with two patches", trace.Workspace)
	}
	for _, p := range trace.Workspace.Patches {
		if p.Patch != "" {
			t.Errorf("trace patch %d carries the patch text", p.Seq)
		}
	}
	if content, _ := os.ReadFile(filepath.Join(repo.path, filepath.FromSlash(wsTokenPath))); string(content) != wsBaseToken {
		t.Errorf("the registered repository was modified")
	}
}

// TestWorkspace_Restart_RebuildsSameWorkspaceFromDatabase: the first process writes once
// and stops before the next Turn runs; a second process -- a new service over the same
// database, with its own model script -- continues the same Agent Run. Its read_file sees
// the write at patchSeq 1 and its write_file records patchSeq 2 on top of patch 1: the
// workspace came from the rows alone.
func TestWorkspace_Restart_RebuildsSameWorkspaceFromDatabase(t *testing.T) {
	repo := newWorkspaceRepo(t)
	first := newWorkspaceHarness(t, workspaceHarnessOptions{repo: repo})
	run, outcome := startWorkspaceRun(first, workspaceDefinition("wf-ws-restart", repo.commit, true, wsAllTools...),
		wsWrite(wsTokenPath, wsNewToken))
	// Execute runs Turn 1 and its write; the READY Turn 2 it committed is offered to the
	// queue, where the first process leaves it.
	if err := first.svc.Execute(first.ctx, outcome); err != nil {
		t.Fatalf("execute: %v", err)
	}
	turn2ID, ok := first.turns.pop()
	if !ok {
		t.Fatalf("no READY Turn was offered after the write")
	}
	agentRunID := factAgentRunID(t, first, outcome)
	if latest := wsLatestPatch(t, first, agentRunID); latest == nil || latest.Seq != 1 {
		t.Fatalf("latest patch before the restart = %+v, want seq 1", latest)
	}

	second := newWorkspaceHarness(t, workspaceHarnessOptions{repo: repo, pool: first.pool, clock: first.clock})
	factScriptToolCalls(second, wsRead(wsTokenPath), wsWrite(wsClaimsPath, wsClaimsSource))
	if err := second.svc.AdvanceAgentTurn(second.ctx, turn2ID, domain.ClaimReconciler); err != nil {
		t.Fatalf("advance turn 2 on the restarted process: %v", err)
	}
	if err := second.drainTurns(second.ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}

	readAttempt := agentOnlyToolAttempt(t, second, wsActionOfTurn(t, second, agentRunID, 2).ID)
	var read map[string]any
	if err := json.Unmarshal(readAttempt.Result, &read); err != nil || readAttempt.Status != domain.ToolAttemptSucceeded {
		t.Fatalf("read after restart = %+v, %v", readAttempt, err)
	}
	if read["content"] != wsNewToken || read["patchSeq"] != float64(1) {
		t.Errorf("read after restart = %v, want the written content at patchSeq 1", read)
	}
	patches := wsPatches(t, second, agentRunID)
	if len(patches) != 2 || patches[1].Seq != 2 || strings.Join(patches[1].TouchedPaths, ",") != wsClaimsPath+","+wsTokenPath {
		t.Fatalf("patches after restart = %+v, want seq 2 cumulative over seq 1", patches)
	}
	if latest := wsLatestPatch(t, second, agentRunID); !strings.Contains(latest.Patch, "const Version = 4") || !strings.Contains(latest.Patch, "type Claims struct") {
		t.Errorf("latest patch after restart = %q, want both writes", latest.Patch)
	}
	if got := agentRunRow(second.ctx, t, second.uow, run.ID).Status; got == domain.RunFailed {
		t.Errorf("run failed after the restart")
	}
}

// ---------------------------------------------------------------------------
// Crash during write_file
// ---------------------------------------------------------------------------

type panicWrite struct{}

func (panicWrite) Execute(context.Context, registry.ToolAction) (registry.ToolExecutionResult, error) {
	panic("simulated crash while write_file is in flight")
}

// TestWorkspaceWrite_CrashMidCall_OrphanReturnedAsInterruptedAndRetried: the process dies
// inside write_file after the claim committed. The restarted process hands the STARTED
// Attempt to the orphan use case, which, because write_file declares no side effect and a
// SAFE repeat, fails it as EXECUTION_INTERRUPTED (not uncertain) and gives the Agent a new
// Turn; the Agent calls write_file again and that call records patch 1. The crashed
// Attempt never recorded anything.
func TestWorkspaceWrite_CrashMidCall_OrphanReturnedAsInterruptedAndRetried(t *testing.T) {
	repo := newWorkspaceRepo(t)
	crashed := newWorkspaceHarness(t, workspaceHarnessOptions{repo: repo, writeExecutor: panicWrite{}})
	run, outcome := startWorkspaceRun(crashed, workspaceDefinition("wf-ws-crash", repo.commit, true, wsAllTools...),
		wsWrite(wsTokenPath, wsNewToken))
	func() {
		defer func() {
			if recover() == nil {
				t.Fatalf("the simulated crash did not happen")
			}
		}()
		crashed.execute(outcome)
	}()
	agentRunID := factAgentRunID(t, crashed, outcome)
	action := wsActionOfTurn(t, crashed, agentRunID, 1)
	attempts := agentToolAttempts(crashed.ctx, t, crashed.uow, action.ID)
	if action.Status != domain.AgentActionRunning || len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptStarted {
		t.Fatalf("after the crash: action %s with attempts %+v, want RUNNING with one STARTED attempt", action.Status, attempts)
	}
	if patches := wsPatches(t, crashed, agentRunID); len(patches) != 0 {
		t.Fatalf("the crashed write recorded a patch: %+v", patches)
	}

	restarted := newWorkspaceHarness(t, workspaceHarnessOptions{repo: repo, pool: crashed.pool, clock: crashed.clock})
	factScriptToolCalls(restarted, wsWrite(wsTokenPath, wsNewToken))
	if err := restarted.svc.HandleOrphanToolAttempt(restarted.ctx, attempts[0].ID); err != nil {
		t.Fatalf("handle orphan: %v", err)
	}
	attempt := getToolAttempt(t, restarted, attempts[0].ID)
	if attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != "EXECUTION_INTERRUPTED" || attempt.Uncertain == nil || *attempt.Uncertain {
		t.Fatalf("orphaned attempt = %+v, want FAILED EXECUTION_INTERRUPTED with uncertain=false", attempt)
	}
	message := wsToolMessage(t, restarted, agentRunID, 1, action.ID)
	if message["status"] != "EXECUTION_INTERRUPTED" {
		t.Errorf("tool message = %v, want EXECUTION_INTERRUPTED", message)
	}
	if !agentTurnNoExists(restarted.ctx, t, restarted.uow, agentRunID, 2) {
		t.Fatalf("no Turn 2 follows the interrupted write")
	}

	// The retry: Turn 2 is READY in the database; the restarted process advances it.
	turn2 := agentTurnByNo(restarted.ctx, t, restarted.uow, agentRunID, 2)
	if err := restarted.svc.AdvanceAgentTurn(restarted.ctx, turn2.ID, domain.ClaimReconciler); err != nil {
		t.Fatalf("advance turn 2: %v", err)
	}
	if err := restarted.drainTurns(restarted.ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	patches := wsPatches(t, restarted, agentRunID)
	if len(patches) != 1 || patches[0].Seq != 1 {
		t.Fatalf("patches after the retry = %+v, want exactly seq 1", patches)
	}
	retried := agentOnlyToolAttempt(t, restarted, wsActionOfTurn(t, restarted, agentRunID, 2).ID)
	if retried.Status != domain.ToolAttemptSucceeded || patches[0].ToolAttemptID != retried.ID {
		t.Errorf("retried attempt = %+v, patch names %s; want the retry's SUCCEEDED attempt", retried, patches[0].ToolAttemptID)
	}
	if got := agentRunRow(restarted.ctx, t, restarted.uow, run.ID).Status; got == domain.RunFailed {
		t.Errorf("run failed after the retry")
	}
}

// ---------------------------------------------------------------------------
// Configuration defects
// ---------------------------------------------------------------------------

func TestWorkspaceTool_AgentWithoutWorkspace_FailsAtClaimWithExplicitError(t *testing.T) {
	repo := newWorkspaceRepo(t)
	h := newWorkspaceHarness(t, workspaceHarnessOptions{repo: repo})
	run, outcome := startWorkspaceRun(h, workspaceDefinition("wf-ws-unbound", repo.commit, false, wsAllTools...),
		wsRead(wsTokenPath))
	h.execute(outcome)

	agentRunID := factAgentRunID(t, h, outcome)
	if _, ok := wsWorkspaceRow(t, h, agentRunID); ok {
		t.Fatalf("an Agent Run without a workspace binding froze one")
	}
	action := wsActionOfTurn(t, h, agentRunID, 1)
	if action.Status != domain.AgentActionFailed || action.Error == nil || action.Error.Code != "WORKSPACE_NOT_CONFIGURED" {
		t.Fatalf("action = status %s error %+v, want FAILED with WORKSPACE_NOT_CONFIGURED", action.Status, action.Error)
	}
	if attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID); len(attempts) != 0 {
		t.Errorf("tool attempts = %+v, want none", attempts)
	}
	if agentTurnNoExists(h.ctx, t, h.uow, agentRunID, 2) {
		t.Errorf("a Turn 2 exists: the defect was handed to the Agent instead of ending the Run")
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationToolError {
		t.Errorf("agent termination = %s, want TOOL_ERROR", agentTermination(agentRun))
	}
	if got := agentRunRow(h.ctx, t, h.uow, run.ID).Status; got != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", got)
	}
}

// failingAppendUoW fails the append of one Event type inside every transaction, so the
// Agent Run initialisation fails after the Agent Run and the workspace were inserted.
type failingAppendUoW struct {
	store.UnitOfWork
	failOn domain.EventType
}

func (u failingAppendUoW) WithinTx(ctx context.Context, fn func(ctx context.Context, tx store.Tx) error) error {
	return u.UnitOfWork.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return fn(ctx, failingAppendTx{Tx: tx, failOn: u.failOn})
	})
}

type failingAppendTx struct {
	store.Tx
	failOn domain.EventType
}

func (t failingAppendTx) Events() store.EventRepository {
	return failingAppendEvents{EventRepository: t.Tx.Events(), failOn: t.failOn}
}

type failingAppendEvents struct {
	store.EventRepository
	failOn domain.EventType
}

var errWorkspaceInjectedAppend = errors.New("injected: event append failed")

func (e failingAppendEvents) Append(ctx context.Context, lock *store.RunLock, ev domain.Event) (domain.Event, error) {
	if ev.Type == e.failOn {
		return domain.Event{}, errWorkspaceInjectedAppend
	}
	return e.EventRepository.Append(ctx, lock, ev)
}

// TestAgentWorkspace_StartTransactionFails_LeavesNoWorkspaceRow: the initialisation
// transaction inserts the Agent Run and its workspace and then fails on AGENT_STARTED.
// Neither row survives, and a later process starts the same NodeRun cleanly.
func TestAgentWorkspace_StartTransactionFails_LeavesNoWorkspaceRow(t *testing.T) {
	repo := newWorkspaceRepo(t)
	failing := newWorkspaceHarness(t, workspaceHarnessOptions{repo: repo, wrapUoW: func(inner store.UnitOfWork) store.UnitOfWork {
		return failingAppendUoW{UnitOfWork: inner, failOn: domain.EventAgentStarted}
	}})
	factScriptToolCalls(failing, wsRead(wsTokenPath))
	def := failing.saveDefinition(workspaceDefinition("wf-ws-start-fails", repo.commit, true, wsAllTools...))
	run := failing.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	inputOutcome := failing.advance(run.ID)
	failing.execute(inputOutcome)
	if _, err := failing.svc.Advance(failing.ctx, run.ID); !errors.Is(err, errWorkspaceInjectedAppend) {
		t.Fatalf("agent claim = %v, want the injected append failure", err)
	}

	var agentRuns, workspaces int
	if err := failing.pool.QueryRow(failing.ctx, `SELECT count(*) FROM agent_runs`).Scan(&agentRuns); err != nil {
		t.Fatal(err)
	}
	if err := failing.pool.QueryRow(failing.ctx, `SELECT count(*) FROM agent_workspaces`).Scan(&workspaces); err != nil {
		t.Fatal(err)
	}
	if agentRuns != 0 || workspaces != 0 {
		t.Fatalf("after the failed start: %d agent runs, %d workspaces; want none of either", agentRuns, workspaces)
	}

	// The same NodeRun starts cleanly on a process without the fault, and freezes the
	// binding once.
	healthy := newWorkspaceHarness(t, workspaceHarnessOptions{repo: repo, pool: failing.pool, clock: failing.clock})
	outcome := healthy.advance(run.ID)
	if !outcome.Claimed || outcome.AgentRunID == "" {
		t.Fatalf("agent claim on the healthy process = %+v, want a claim with an Agent Run", outcome)
	}
	if ws, ok := wsWorkspaceRow(t, healthy, outcome.AgentRunID); !ok || ws.BaseCommit != repo.commit {
		t.Errorf("workspace after the healthy start = %+v, %v; want the frozen binding", ws, ok)
	}
	if err := healthy.pool.QueryRow(healthy.ctx, `SELECT count(*) FROM agent_workspaces`).Scan(&workspaces); err != nil || workspaces != 1 {
		t.Errorf("agent_workspaces rows = %d, %v; want exactly 1", workspaces, err)
	}
}
