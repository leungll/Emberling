//go:build integration

// Context Manifest tests: the claim transaction of an Agent Turn writes exactly one
// Manifest of the model request it is about to issue, derived from the request content
// and the source of each message. They share the agentHarness of agent_loop_test.go.
package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
)

const (
	manifestReportTool = "manifest_report"
	manifestReadTool   = "manifest_read"

	// Marker strings that enter the request through the instructions, a tool result, a
	// workspace file and the state. None of them may be copied into a Manifest.
	manifestInstructionSecret = "operator-token-7c1e-SECRET"
	manifestToolSecret        = "Bearer report-secret-key"
	manifestFileSecret        = "live_sk_file_secret"
	manifestStateSecret       = "state-note-secret"

	manifestArtifactID     = "art_7d3f9a1c2b4e5f60718293a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4"
	manifestArtifactSHA    = "7d3f9a1c2b4e5f60718293a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4"
	manifestArtifactSize   = 4096
	manifestReadPatchSeq   = 1
	manifestLongPathPrefix = "src/"
	manifestRepositoryID   = "manifest-fixture"
)

// manifestLongPath is a workspace path longer than the label bound, so the WORKSPACE_FILE
// label must be cut while the reference keeps the whole path.
var manifestLongPath = manifestLongPathPrefix + strings.Repeat("très-long-directory/", 12) + "file.go"

// manifestReportExecutor answers with a result that refers to an Execution Artifact and
// carries a secret-looking header value.
type manifestReportExecutor struct{}

func (manifestReportExecutor) Execute(context.Context, registry.ToolAction) (registry.ToolExecutionResult, error) {
	output := `{"summary":"scan finished","header":"` + manifestToolSecret + `",` +
		`"report":{"artifactId":"` + manifestArtifactID + `","mediaType":"text/plain",` +
		`"sizeBytes":` + itoa(manifestArtifactSize) + `,"sha256":"` + manifestArtifactSHA + `"}}`
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted,
		Result: &registry.ToolResult{Output: json.RawMessage(output)}}, nil
}

// manifestReadExecutor stands for a workspace read Tool. Its result carries the top-level
// path and patchSeq a workspace file is versioned by, unless the arguments ask it to leave
// the version out, which reproduces a workspace Tool result that names no file version.
type manifestReadExecutor struct{}

func (manifestReadExecutor) Execute(_ context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	var args struct {
		Path        string `json:"path"`
		OmitVersion bool   `json:"omitVersion"`
	}
	if err := json.Unmarshal(action.Arguments, &args); err != nil {
		return registry.ToolExecutionResult{}, err
	}
	content := `"content":"const apiKey = '` + manifestFileSecret + `'"`
	output := `{` + content + `}`
	if !args.OmitVersion {
		output = `{"path":"` + args.Path + `","patchSeq":` + itoa(manifestReadPatchSeq) + `,` + content + `}`
	}
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted,
		Result: &registry.ToolResult{Output: json.RawMessage(output)}}, nil
}

func itoa(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

func manifestToolRegistrations() []registry.ToolRegistration {
	schema := json.RawMessage(`{"type":"object"}`)
	return []registry.ToolRegistration{
		{
			Metadata: domain.ToolMetadata{
				Name: manifestReportTool, Description: "Report a scan", InputSchema: schema, OutputSchema: schema,
				SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
				ExecutionKind: domain.ToolExecutionSync,
			},
			Executor: manifestReportExecutor{},
		},
		{
			Metadata: domain.ToolMetadata{
				Name: manifestReadTool, Description: "Read a workspace file", InputSchema: schema, OutputSchema: schema,
				SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
				ExecutionKind: domain.ToolExecutionSync,
				UsesWorkspace: true,
			},
			Executor: manifestReadExecutor{},
		},
	}
}

// manifestDefinition is the fixture graph with the two Manifest Tools allowed and an
// instruction that carries a secret-looking token. The read Tool declares that it uses the
// workspace, and a workspace Tool is only admitted on an Agent Run that froze a workspace
// binding, so the Agent node binds one; the read double never touches the repository.
func manifestDefinition(workflowID string) domain.Definition {
	def := factDefinition(workflowID, manifestReportTool, manifestReadTool)
	for i, node := range def.Nodes {
		if node.ID == "node_agent" {
			var config map[string]any
			if err := json.Unmarshal(node.Config, &config); err != nil {
				panic(err)
			}
			config["instructions"] = "Use the token " + manifestInstructionSecret + " when calling the service."
			config["workspace"] = map[string]any{
				"repositoryId": manifestRepositoryID,
				"baseCommit":   strings.Repeat("a", 40),
				"allowedPaths": []string{"go.mod", "src/"},
			}
			encoded, err := json.Marshal(config)
			if err != nil {
				panic(err)
			}
			def.Nodes[i].Config = encoded
		}
	}
	return def
}

// scriptManifestLoop makes the model call the report Tool, then read a long path with a
// version, then read without a version, then finish. The first Decision patches the state
// with a secret-looking note so STATE carries one too.
func scriptManifestLoop(h *agentHarness) {
	var turn atomic.Int32
	h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		switch turn.Add(1) {
		case 1:
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioToolCall, ToolName: manifestReportTool,
				ToolArguments: json.RawMessage(`{}`), StatePatch: json.RawMessage(`{"note":"` + manifestStateSecret + `"}`)}
		case 2:
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioToolCall, ToolName: manifestReadTool,
				ToolArguments: json.RawMessage(`{"path":"` + manifestLongPath + `"}`)}
		case 3:
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioToolCall, ToolName: manifestReadTool,
				ToolArguments: json.RawMessage(`{"path":"go.mod","omitVersion":true}`)}
		default:
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: "done"}
		}
	}
}

// runManifestLoop drives the four-round loop to completion and returns the Agent Run.
func runManifestLoop(t *testing.T, workflowID string) (*agentHarness, domain.Run, domain.AgentRun) {
	t.Helper()
	h := newAgentHarness(t, agentHarnessOptions{Tools: manifestToolRegistrations(), WorkspaceRepositories: []string{manifestRepositoryID}})
	scriptManifestLoop(h)
	def := h.saveDefinition(manifestDefinition(workflowID))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)
	h.execute(outcome)
	agentRun := getAgentRunByNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Fatalf("agent run termination = %v, want FINAL_RESPONSE", agentRun.Termination)
	}
	return h, run, agentRun
}

func getAgentRunByNodeRun(ctx context.Context, t *testing.T, uow store.UnitOfWork, nodeRunID string) domain.AgentRun {
	t.Helper()
	var agentRun domain.AgentRun
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		agentRun, err = tx.AgentRuns().GetByNodeRunID(ctx, nodeRunID)
		return err
	}); err != nil {
		t.Fatalf("get agent run of node run %s: %v", nodeRunID, err)
	}
	return agentRun
}

func listManifests(ctx context.Context, t *testing.T, uow store.UnitOfWork, agentRunID string) []domain.AgentTurnManifest {
	t.Helper()
	var manifests []domain.AgentTurnManifest
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		manifests, err = tx.AgentTurnManifests().ListByAgentRunID(ctx, agentRunID)
		return err
	}); err != nil {
		t.Fatalf("list manifests of %s: %v", agentRunID, err)
	}
	return manifests
}

func getManifest(ctx context.Context, t *testing.T, uow store.UnitOfWork, turnID string) (domain.AgentTurnManifest, error) {
	t.Helper()
	var manifest domain.AgentTurnManifest
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		manifest, err = tx.AgentTurnManifests().GetByTurnID(ctx, turnID)
		return err
	})
	return manifest, err
}

func manifestItemsOfKind(items []domain.ManifestItem, kind domain.ManifestItemKind) []domain.ManifestItem {
	var out []domain.ManifestItem
	for _, item := range items {
		if item.Kind == kind {
			out = append(out, item)
		}
	}
	return out
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// TestAgentTurnManifest_EachClaimedTurn_HasExactlyOneManifest: after a four-round loop
// every Turn has one Manifest, and the Agent Run holds exactly as many Manifests as Turns.
func TestAgentTurnManifest_EachClaimedTurn_HasExactlyOneManifest(t *testing.T) {
	h, _, agentRun := runManifestLoop(t, "wf-manifest-one-per-turn")

	turns := listTurnsOfAgentRun(h.ctx, t, h.uow, agentRun.ID)
	if len(turns) != 4 {
		t.Fatalf("turns = %d, want 4", len(turns))
	}
	manifests := listManifests(h.ctx, t, h.uow, agentRun.ID)
	if len(manifests) != len(turns) {
		t.Fatalf("manifests = %d, want one per Turn (%d)", len(manifests), len(turns))
	}
	for i, turn := range turns {
		if manifests[i].TurnID != turn.ID {
			t.Errorf("manifests[%d].TurnID = %s, want %s (Turn order)", i, manifests[i].TurnID, turn.ID)
		}
		manifest, err := getManifest(h.ctx, t, h.uow, turn.ID)
		if err != nil {
			t.Errorf("turn %d: manifest: %v", turn.TurnNo, err)
			continue
		}
		if manifest.AgentRunID != agentRun.ID || len(manifest.Items) == 0 {
			t.Errorf("turn %d: manifest = %+v, want items of agent run %s", turn.TurnNo, manifest, agentRun.ID)
		}
	}
}

// TestAgentTurnManifest_TwoClaimsOfOneTurn_OnlyWinnerWrites: two advancement paths claim
// the same READY Turn under an explicit barrier. The loser's transaction writes nothing,
// so the Turn ends with exactly one Manifest, and the READY Turn the committed Action
// created has none because nobody has claimed it.
func TestAgentTurnManifest_TwoClaimsOfOneTurn_OnlyWinnerWrites(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	// The single model call decides a Tool call, so the committed Action creates a second
	// READY Turn that nobody claims within this test.
	h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioToolCall, ToolName: lookup.ToolName,
			ToolArguments: json.RawMessage(`{"key":"ember"}`)}
	}
	def := h.saveDefinition(agentLoopDefinition("wf-manifest-claim-race"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	var generateCalls atomic.Int32
	inFlight := make(chan struct{}, 2)
	release := make(chan struct{})
	h.provider.BeforeReturn = func(context.Context) {
		if generateCalls.Add(1) == 1 {
			inFlight <- struct{}{}
			<-release
		}
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- h.svc.AdvanceAgentTurn(h.ctx, outcome.AgentTurnID, domain.ClaimImmediate)
		}()
	}
	close(start)
	<-inFlight
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("advance agent turn: %v", err)
		}
	}
	if got := generateCalls.Load(); got != 1 {
		t.Fatalf("model Generate calls = %d, want exactly 1", got)
	}

	agentRun := getAgentRunByNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	manifests := listManifests(h.ctx, t, h.uow, agentRun.ID)
	if len(manifests) != 1 || manifests[0].TurnID != outcome.AgentTurnID {
		t.Fatalf("manifests after the race = %+v, want exactly the claimed Turn's", manifests)
	}
	turns := listTurnsOfAgentRun(h.ctx, t, h.uow, agentRun.ID)
	if len(turns) != 2 || turns[1].Status != domain.AgentTurnReady {
		t.Fatalf("turns = %+v, want the claimed Turn and one unclaimed READY Turn", turns)
	}
	if _, err := getManifest(h.ctx, t, h.uow, turns[1].ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unclaimed READY Turn's manifest: err = %v, want ErrNotFound", err)
	}
}

// TestAgentTurnManifest_IncludedItems_DigestsMatchRecordedRequest compares the last Turn's
// Manifest with the request the Provider actually received for that Turn, one assertion
// per kind: the digest and length of an included item are those of the request bytes, and
// the omitted artifact carries the digest and size the reference declared.
func TestAgentTurnManifest_IncludedItems_DigestsMatchRecordedRequest(t *testing.T) {
	h, _, agentRun := runManifestLoop(t, "wf-manifest-digests")

	requests := h.provider.Requests()
	turns := listTurnsOfAgentRun(h.ctx, t, h.uow, agentRun.ID)
	if len(requests) != 4 || len(turns) != 4 {
		t.Fatalf("requests/turns = %d/%d, want 4/4", len(requests), len(turns))
	}
	request := requests[3]
	manifest, err := getManifest(h.ctx, t, h.uow, turns[3].ID)
	if err != nil {
		t.Fatalf("manifest of turn 4: %v", err)
	}

	assertOne := func(kind domain.ManifestItemKind, content []byte) {
		t.Helper()
		got := manifestItemsOfKind(manifest.Items, kind)
		if len(got) != 1 {
			t.Fatalf("%s: %d items, want 1 (items: %+v)", kind, len(got), manifest.Items)
		}
		if got[0].SHA256 != sha256Hex(content) || got[0].Length != len(content) {
			t.Errorf("%s: sha256/length = %s/%d, want %s/%d of the request bytes",
				kind, got[0].SHA256, got[0].Length, sha256Hex(content), len(content))
		}
		if !got[0].Included {
			t.Errorf("%s: included = false, want true", kind)
		}
	}
	assertOne(domain.ManifestInstructions, []byte(request.Instructions))
	assertOne(domain.ManifestState, request.State)

	var user, report, file []byte
	var toolResults [][]byte
	for _, message := range request.Messages {
		switch {
		case message.Role == "user":
			user = message.Content
		case message.Role == "tool" && message.ToolName != nil && *message.ToolName == manifestReportTool:
			report = message.Content
			toolResults = append(toolResults, message.Content)
		case message.Role == "tool" && strings.Contains(string(message.Content), `"patchSeq"`):
			file = message.Content
		case message.Role == "tool":
			toolResults = append(toolResults, message.Content)
		}
	}
	assertOne(domain.ManifestTaskInput, user)
	assertOne(domain.ManifestWorkspaceFile, file)

	results := manifestItemsOfKind(manifest.Items, domain.ManifestToolResult)
	if len(results) != 2 || len(toolResults) != 2 {
		t.Fatalf("TOOL_RESULT items/messages = %d/%d, want 2/2", len(results), len(toolResults))
	}
	for i := range results {
		if results[i].SHA256 != sha256Hex(toolResults[i]) || results[i].Length != len(toolResults[i]) {
			t.Errorf("TOOL_RESULT[%d]: sha256/length = %s/%d, want %s/%d",
				i, results[i].SHA256, results[i].Length, sha256Hex(toolResults[i]), len(toolResults[i]))
		}
	}
	if results[0].SHA256 != sha256Hex(report) {
		t.Errorf("first TOOL_RESULT is not the report Tool's message")
	}

	artifacts := manifestItemsOfKind(manifest.Items, domain.ManifestArtifact)
	if len(artifacts) != 1 || artifacts[0].SHA256 != manifestArtifactSHA || artifacts[0].Length != manifestArtifactSize {
		t.Errorf("ARTIFACT = %+v, want the declared digest %s and size %d", artifacts, manifestArtifactSHA, manifestArtifactSize)
	}
}

// TestAgentTurnManifest_ArtifactReference_OmittedWithReasonAndRef: from the Turn after
// the report Tool ran, the artifact its result referred to is an omitted item with the
// REFERENCE_ONLY reason and the Artifact ID as its reference, which is what the artifact
// content route takes; included items carry no omitted reason.
func TestAgentTurnManifest_ArtifactReference_OmittedWithReasonAndRef(t *testing.T) {
	h, _, agentRun := runManifestLoop(t, "wf-manifest-omitted")

	turns := listTurnsOfAgentRun(h.ctx, t, h.uow, agentRun.ID)
	first, err := getManifest(h.ctx, t, h.uow, turns[0].ID)
	if err != nil {
		t.Fatalf("manifest of turn 1: %v", err)
	}
	if got := manifestItemsOfKind(first.Items, domain.ManifestArtifact); len(got) != 0 {
		t.Errorf("turn 1 (no tool message yet) lists artifacts: %+v", got)
	}

	second, err := getManifest(h.ctx, t, h.uow, turns[1].ID)
	if err != nil {
		t.Fatalf("manifest of turn 2: %v", err)
	}
	artifacts := manifestItemsOfKind(second.Items, domain.ManifestArtifact)
	if len(artifacts) != 1 {
		t.Fatalf("turn 2 ARTIFACT items = %+v, want 1", artifacts)
	}
	art := artifacts[0]
	if art.Included || art.OmittedReason == nil || *art.OmittedReason != domain.ManifestReferenceOnly {
		t.Errorf("artifact item = %+v, want omitted with REFERENCE_ONLY", art)
	}
	if art.Ref != manifestArtifactID {
		t.Errorf("artifact ref = %q, want the Artifact ID %q", art.Ref, manifestArtifactID)
	}
	for _, item := range second.Items {
		if item.Included && item.OmittedReason != nil {
			t.Errorf("included item %s carries omittedReason %q", item.Kind, *item.OmittedReason)
		}
		if !item.Included && item.Kind != domain.ManifestArtifact {
			t.Errorf("non-artifact item %s is omitted", item.Kind)
		}
	}
}

// TestAgentTurnManifest_SensitiveContent_AbsentFromStoredJSON reads the stored items
// column of every Manifest and asserts that none of the marker strings that entered the
// request through the instructions, a tool result, a workspace file or the state appears
// in it.
func TestAgentTurnManifest_SensitiveContent_AbsentFromStoredJSON(t *testing.T) {
	h, _, agentRun := runManifestLoop(t, "wf-manifest-secrets")

	rows, err := h.pool.Query(h.ctx, `SELECT items::text FROM agent_turn_manifests WHERE agent_run_id = $1`, agentRun.ID)
	if err != nil {
		t.Fatalf("read stored manifests: %v", err)
	}
	defer rows.Close()
	var stored []string
	for rows.Next() {
		var items string
		if err := rows.Scan(&items); err != nil {
			t.Fatalf("scan: %v", err)
		}
		stored = append(stored, items)
	}
	if len(stored) != 4 {
		t.Fatalf("stored manifests = %d, want 4", len(stored))
	}
	for _, items := range stored {
		for _, secret := range []string{manifestInstructionSecret, "Bearer", "report-secret-key",
			manifestFileSecret, "apiKey", manifestStateSecret, "scan finished", agentQuestion} {
			if strings.Contains(items, secret) {
				t.Errorf("stored manifest carries request content %q: %s", secret, items)
			}
		}
	}
}

// TestAgentTurnManifest_LongLabel_TruncatedWithEllipsis: the workspace file read at a
// path longer than the label bound is labelled with exactly the bound's worth of runes
// ending in an ellipsis, while its reference keeps the whole path.
func TestAgentTurnManifest_LongLabel_TruncatedWithEllipsis(t *testing.T) {
	h, _, agentRun := runManifestLoop(t, "wf-manifest-long-label")

	turns := listTurnsOfAgentRun(h.ctx, t, h.uow, agentRun.ID)
	manifest, err := getManifest(h.ctx, t, h.uow, turns[2].ID)
	if err != nil {
		t.Fatalf("manifest of turn 3: %v", err)
	}
	files := manifestItemsOfKind(manifest.Items, domain.ManifestWorkspaceFile)
	if len(files) != 1 {
		t.Fatalf("WORKSPACE_FILE items = %+v, want 1", files)
	}
	label := files[0].Label
	if got := utf8.RuneCountInString(label); got != domain.ManifestLabelMaxRunes {
		t.Errorf("label runes = %d, want %d", got, domain.ManifestLabelMaxRunes)
	}
	if !strings.HasSuffix(label, "…") || !strings.HasPrefix(manifestLongPath, strings.TrimSuffix(label, "…")) {
		t.Errorf("label %q is not the path cut and ended with an ellipsis", label)
	}
	if files[0].Ref != manifestLongPath {
		t.Errorf("ref = %q, want the full path", files[0].Ref)
	}
}

// TestAgentTurnManifest_WorkspaceToolMessage_FileOnlyWithPathAndPatchSeq: of the two
// messages of the workspace Tool, the one that named path and patchSeq is a
// WORKSPACE_FILE versioned by the patch sequence; the one without them is a TOOL_RESULT
// referring to its Tool Attempt.
func TestAgentTurnManifest_WorkspaceToolMessage_FileOnlyWithPathAndPatchSeq(t *testing.T) {
	h, _, agentRun := runManifestLoop(t, "wf-manifest-workspace-file")

	turns := listTurnsOfAgentRun(h.ctx, t, h.uow, agentRun.ID)
	manifest, err := getManifest(h.ctx, t, h.uow, turns[3].ID)
	if err != nil {
		t.Fatalf("manifest of turn 4: %v", err)
	}

	files := manifestItemsOfKind(manifest.Items, domain.ManifestWorkspaceFile)
	if len(files) != 1 || files[0].Ref != manifestLongPath || files[0].Version != itoa(manifestReadPatchSeq) {
		t.Fatalf("WORKSPACE_FILE items = %+v, want one at %s@%d", files, manifestLongPath, manifestReadPatchSeq)
	}

	// The third round's Action is the read without a version; its Attempt is the
	// reference of the TOOL_RESULT item.
	action, found := agentActionOfTurn(h.ctx, t, h.uow, turns[2].ID)
	if !found {
		t.Fatalf("turn 3 has no action")
	}
	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 {
		t.Fatalf("turn 3 attempts = %d, want 1", len(attempts))
	}
	results := manifestItemsOfKind(manifest.Items, domain.ManifestToolResult)
	var versionless *domain.ManifestItem
	for i := range results {
		if results[i].Ref == attempts[0].ID {
			versionless = &results[i]
		}
	}
	if versionless == nil {
		t.Fatalf("no TOOL_RESULT refers to attempt %s: %+v", attempts[0].ID, results)
	}
	if versionless.Version != itoa(attempts[0].AttemptNo) || versionless.Label != manifestReadTool+" result" {
		t.Errorf("versionless read = %+v, want version %d and label %q", versionless, attempts[0].AttemptNo, manifestReadTool+" result")
	}
}
