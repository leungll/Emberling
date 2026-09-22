//go:build integration

// Agent Loop integration tests. They cover the execution-layer guarantees a mock
// repository cannot prove (CLAUDE.md testing standard): the single initialisation
// transaction that expands a MANAGED_AGENT NodeRun into an Agent Run without ever
// creating a Node Attempt, the conditional READY -> RUNNING Turn claim that elects one
// model caller, the "model call strictly after COMMIT" ordering, and the failure
// transactions that terminate an Agent Run without leaving a Decision behind.
//
// Helpers here are prefixed `agent` so they never collide with the sibling `exec`- and
// store-prefixed helpers of this same package.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/agent"
	"github.com/leungll/Emberling/backend/internal/nodes/textinput"
	"github.com/leungll/Emberling/backend/internal/nodes/textoutput"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// agentTimeoutMs is the Agent deadline every fixture Definition below freezes.
const agentTimeoutMs = 120_000

// agentMaxTurns is the frozen turn bound of the fixture Definition.
const agentMaxTurns = 4

// agentInstructions is the frozen system prompt of the fixture Definition. It must never
// appear in a Context Version's messages (05 §2: Instructions are not copied into Context
// Messages).
const agentInstructions = "Answer with the lookup tool when a record is needed."

// agentQuestion is the Run input every fixture Run below starts from.
const agentQuestion = "who is ada"

type agentHarnessOptions struct {
	// Pool reuses an existing database instead of provisioning a fresh one. Together with
	// a new harness value it is a process restart: a second Backend image over the same
	// committed PostgreSQL facts, the only recovery source invariant #6 allows.
	Pool *pgxpool.Pool
	// SkipLookupTool leaves "lookup" unregistered in this Backend's Tool Registry,
	// reproducing registry drift between the Backend that saved a Definition and the one
	// that runs it.
	SkipLookupTool bool
	// Clock, when set, is shared with an earlier harness so a restart does not rewind time.
	Clock *execClock
	// LookupExecutor replaces the `lookup` Tool's registered Executor while keeping its
	// registered Metadata, so a test can observe or delay the one real Tool call the
	// Agent Loop makes without inventing a second stable Tool Name.
	LookupExecutor registry.ToolExecutor
	// Tools registers additional Tools beside `lookup`, for the case of a Tool that is
	// registered but absent from an Agent Run's frozen allowlist.
	Tools []registry.ToolRegistration
	// Notifier replaces the no-op EventNotifier. It is the only hook this service exposes
	// between a committed transaction and the in-process advancement that follows it.
	Notifier service.EventNotifier
}

// agentHarness wires one real PostgreSQL-backed ExecutionService with the `agent` Node
// Type, the `lookup` Tool and the deterministic Mock Model Provider registered.
type agentHarness struct {
	t   *testing.T
	ctx context.Context

	uow      store.UnitOfWork
	pool     *pgxpool.Pool
	compiler *runtime.Compiler
	clock    *execClock
	ids      *execForcedIDs
	provider *mockmodel.Provider
	svc      *service.ExecutionService
}

func newAgentHarness(t *testing.T, opts agentHarnessOptions) *agentHarness {
	t.Helper()
	ctx := context.Background()

	pool := opts.Pool
	if pool == nil {
		pool = testdb.Open(t)
	}
	uow := postgres.NewUnitOfWork(pool)

	nodeRegistry := registry.NewNodeRegistry()
	modelRegistry := registry.NewModelRegistry()
	toolRegistry := registry.NewToolRegistry()

	provider := mockmodel.NewProvider()
	if err := modelRegistry.Register(ctx, provider); err != nil {
		t.Fatalf("register mock model provider: %v", err)
	}
	for _, reg := range []registry.NodeRegistration{
		textinput.Registration(),
		textoutput.Registration(),
		agent.Registration(),
	} {
		if err := nodeRegistry.Register(reg); err != nil {
			t.Fatalf("register node type %s: %v", reg.Metadata.Type, err)
		}
	}
	if !opts.SkipLookupTool {
		registration := lookup.Registration()
		if opts.LookupExecutor != nil {
			registration.Executor = opts.LookupExecutor
		}
		if err := toolRegistry.Register(registration); err != nil {
			t.Fatalf("register tool %s: %v", lookup.ToolName, err)
		}
	}
	for _, registration := range opts.Tools {
		if err := toolRegistry.Register(registration); err != nil {
			t.Fatalf("register tool %s: %v", registration.Metadata.Name, err)
		}
	}

	clock := opts.Clock
	if clock == nil {
		// See execDeadlineClockStart (execution_test.go): this default clock
		// feeds real context.WithDeadline calls for Model/Tool calls, so it
		// must start at wall-clock time, not the fixed fixtureTime constant.
		clock = newExecClock(execDeadlineClockStart())
	}
	compiler := runtime.NewCompiler(nodeRegistry, clock)

	ids := newExecForcedIDs()
	svc := service.NewExecutionService(service.Deps{
		UoW:      uow,
		Nodes:    nodeRegistry,
		Models:   modelRegistry,
		Tools:    toolRegistry,
		Compiler: compiler,
		Clock:    clock,
		IDs:      ids,
		Notifier: opts.Notifier,
		// An ASYNC Tool's claim transaction issues an Attempt-scoped callback credential,
		// which needs a signing secret and a callback origin.
		Callback: service.CallbackConfig{
			BaseURL:       asyncBaseURL,
			SigningSecret: []byte(asyncSigningSecret),
			PendingTTL:    time.Hour,
		},
	})

	return &agentHarness{
		t: t, ctx: ctx,
		uow: uow, pool: pool, compiler: compiler,
		clock: clock, ids: ids, provider: provider, svc: svc,
	}
}

func (h *agentHarness) saveDefinition(def domain.Definition) domain.Definition {
	h.t.Helper()
	plan, err := h.compiler.Compile(h.ctx, def)
	if err != nil {
		h.t.Fatalf("compile definition %s v%d: %v", def.WorkflowID, def.Version, err)
	}
	def.RunInputSchema = plan.RunInputSchema
	def.Validation = plan.Validation
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Definitions().Save(ctx, def)
	}); err != nil {
		h.t.Fatalf("save definition %s v%d: %v", def.WorkflowID, def.Version, err)
	}
	return def
}

func (h *agentHarness) createRun(workflowID string, version int, input string) domain.Run {
	h.t.Helper()
	run, err := h.svc.CreateRun(h.ctx, service.CreateRun{
		WorkflowID: workflowID, DefinitionVersion: version, Input: json.RawMessage(input),
	})
	if err != nil {
		h.t.Fatalf("create run: %v", err)
	}
	return run
}

func (h *agentHarness) advance(runID string) service.AdvanceOutcome {
	h.t.Helper()
	outcome, err := h.svc.Advance(h.ctx, runID)
	if err != nil {
		h.t.Fatalf("advance %s: %v", runID, err)
	}
	return outcome
}

func (h *agentHarness) execute(outcome service.AdvanceOutcome) {
	h.t.Helper()
	if err := h.svc.Execute(h.ctx, outcome); err != nil {
		h.t.Fatalf("execute node_run=%s: %v", outcome.NodeRunID, err)
	}
}

// claimAgentNode drives the fixture Run up to (and including) the transaction that claims
// the Agent NodeRun: the text_input node runs to completion first, then one more Advance
// claims the agent. The returned outcome is the Agent claim.
func (h *agentHarness) claimAgentNode(runID string) service.AdvanceOutcome {
	h.t.Helper()
	inputOutcome := h.advance(runID)
	if !inputOutcome.Claimed {
		h.t.Fatalf("advance did not claim the text_input node run")
	}
	h.execute(inputOutcome)

	agentOutcome := h.advance(runID)
	if !agentOutcome.Claimed {
		h.t.Fatalf("advance did not claim the agent node run")
	}
	return agentOutcome
}

// agentLoopDefinition is the fixture graph: text_input --text--> agent --text-->
// text_output, with one frozen Tool allowlist and one frozen Model ID. It mirrors the
// shared `wf_agent_lookup` contract fixture.
func agentLoopDefinition(workflowID string) domain.Definition {
	return agentLoopDefinitionMaxTurns(workflowID, agentMaxTurns)
}

// agentLoopDefinitionMaxTurns is agentLoopDefinition with the frozen turn bound chosen by
// the caller, for the round-limit transaction.
func agentLoopDefinitionMaxTurns(workflowID string, maxTurns int) domain.Definition {
	config := `{
		"instructions": "` + agentInstructions + `",
		"modelId": "` + mockmodel.ModelID + `",
		"allowedTools": ["` + lookup.ToolName + `"],
		"maxTurns": ` + strconv.Itoa(maxTurns) + `,
		"timeoutMs": 120000
	}`
	return domain.Definition{
		WorkflowID:  workflowID,
		Version:     1,
		Name:        "Agent Lookup",
		Description: "text_input -> agent -> text_output",
		Nodes: []domain.Node{
			{ID: "node_input", Type: "text_input", Name: "Question",
				Config: json.RawMessage(`{"inputKey":"question","required":true}`)},
			{ID: "node_agent", Type: "agent", Name: "Agent", Config: json.RawMessage(config)},
			{ID: "node_output", Type: "text_output", Name: "Output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "node_input", SourceHandle: "text", Target: "node_agent", TargetHandle: "input"},
			{ID: "e2", Source: "node_agent", SourceHandle: "text", Target: "node_output", TargetHandle: "text"},
		},
		CreatedAt: fixtureTime,
	}
}

func agentNodeRun(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID string) domain.NodeRun {
	t.Helper()
	var nodeRuns []domain.NodeRun
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		nodeRuns, err = tx.NodeRuns().ListByRun(ctx, runID)
		return err
	}); err != nil {
		t.Fatalf("list node runs of %s: %v", runID, err)
	}
	return execNodeRunByNodeID(t, nodeRuns, "node_agent")
}

func agentRunOfNodeRun(ctx context.Context, t *testing.T, uow store.UnitOfWork, nodeRunID string) (domain.AgentRun, bool) {
	t.Helper()
	var agentRun domain.AgentRun
	found := true
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		agentRun, err = tx.AgentRuns().GetByNodeRunID(ctx, nodeRunID)
		if errors.Is(err, domain.ErrNotFound) {
			found = false
			return nil
		}
		return err
	}); err != nil {
		t.Fatalf("get agent run of node run %s: %v", nodeRunID, err)
	}
	return agentRun, found
}

func agentAttemptCount(ctx context.Context, t *testing.T, uow store.UnitOfWork, nodeRunID string) int {
	t.Helper()
	var attempts []domain.NodeAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		attempts, err = tx.NodeAttempts().ListByNodeRun(ctx, nodeRunID)
		return err
	}); err != nil {
		t.Fatalf("list attempts of node run %s: %v", nodeRunID, err)
	}
	return len(attempts)
}

func agentContextVersion(ctx context.Context, t *testing.T, uow store.UnitOfWork, agentRunID string, version int) domain.AgentContextVersion {
	t.Helper()
	var got domain.AgentContextVersion
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.AgentContextVersions().GetByRunAndVersion(ctx, agentRunID, version)
		return err
	}); err != nil {
		t.Fatalf("get context version %d of agent run %s: %v", version, agentRunID, err)
	}
	return got
}

func agentStateVersion(ctx context.Context, t *testing.T, uow store.UnitOfWork, agentRunID string, version int) domain.AgentStateVersion {
	t.Helper()
	var got domain.AgentStateVersion
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.AgentStateVersions().GetByRunAndVersion(ctx, agentRunID, version)
		return err
	}); err != nil {
		t.Fatalf("get state version %d of agent run %s: %v", version, agentRunID, err)
	}
	return got
}

func agentRunRow(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID string) domain.Run {
	t.Helper()
	var run domain.Run
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		run, err = tx.Runs().Get(ctx, runID)
		return err
	}); err != nil {
		t.Fatalf("get run %s: %v", runID, err)
	}
	return run
}

// agentEventTypesFor returns the ordered Event types recorded for one NodeRun.
func agentEventTypesFor(events []domain.Event, nodeRunID string) []domain.EventType {
	var out []domain.EventType
	for _, ev := range events {
		if ev.NodeRunID != nil && *ev.NodeRunID == nodeRunID {
			out = append(out, ev.Type)
		}
	}
	return out
}

func agentEventPayload(t *testing.T, events []domain.Event, typ domain.EventType) map[string]any {
	t.Helper()
	for _, ev := range events {
		if ev.Type != typ {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("decode %s payload: %v", typ, err)
		}
		return payload
	}
	t.Fatalf("no %s event among %d events", typ, len(events))
	return nil
}

func agentCountEventType(types []domain.EventType, want domain.EventType) int {
	count := 0
	for _, got := range types {
		if got == want {
			count++
		}
	}
	return count
}

// ---------------------------------------------------------------------------
// Agent Run initialisation
// ---------------------------------------------------------------------------

// TestManagedAgentNodeRun_FirstClaim_CreatesAgentRunAndReadyTurnNoNodeAttempt proves the
// one transaction docs/06-execution-model.md §1.7 requires when an Agent NodeRun first
// becomes RUNNING: a unique Agent Run frozen from the Definition, Context/State Version 0,
// Turn 1 READY, AGENT_STARTED and AGENT_TURN_READY -- and, per 05 §1.8, no Node Attempt
// at all.
func TestManagedAgentNodeRun_FirstClaim_CreatesAgentRunAndReadyTurnNoNodeAttempt(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	def := h.saveDefinition(agentLoopDefinition("wf-agent-first-claim"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)

	outcome := h.claimAgentNode(run.ID)
	if outcome.NodeType != "agent" {
		t.Fatalf("claimed node type = %q, want agent", outcome.NodeType)
	}
	if outcome.AttemptID != "" {
		t.Errorf("agent claim carries attempt id %q, want none: a MANAGED_AGENT NodeRun creates no Node Attempt", outcome.AttemptID)
	}

	nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID)
	if got := agentAttemptCount(h.ctx, t, h.uow, nodeRun.ID); got != 0 {
		t.Errorf("node attempts for the agent node run = %d, want 0", got)
	}
	if nodeRun.AttemptCount != 0 {
		t.Errorf("agent node run attempt_count = %d, want 0", nodeRun.AttemptCount)
	}
	if nodeRun.Status != domain.NodeRunRunning {
		t.Errorf("agent node run status = %s, want RUNNING", nodeRun.Status)
	}

	agentRun, found := agentRunOfNodeRun(h.ctx, t, h.uow, nodeRun.ID)
	if !found {
		t.Fatalf("no agent run was created for agent node run %s", nodeRun.ID)
	}
	if agentRun.Instructions != agentInstructions {
		t.Errorf("frozen instructions = %q, want %q", agentRun.Instructions, agentInstructions)
	}
	if agentRun.ModelID != mockmodel.ModelID {
		t.Errorf("frozen modelId = %q, want %q", agentRun.ModelID, mockmodel.ModelID)
	}
	if len(agentRun.AllowedTools) != 1 || agentRun.AllowedTools[0] != lookup.ToolName {
		t.Errorf("frozen allowedTools = %v, want [%s]", agentRun.AllowedTools, lookup.ToolName)
	}
	if agentRun.MaxTurns != agentMaxTurns {
		t.Errorf("frozen maxTurns = %d, want %d", agentRun.MaxTurns, agentMaxTurns)
	}
	if agentRun.CurrentTurnNo != 1 || agentRun.CurrentContextVersion != 0 || agentRun.CurrentStateVersion != 0 {
		t.Errorf("pointers = (turn %d, context %d, state %d), want (1, 0, 0)",
			agentRun.CurrentTurnNo, agentRun.CurrentContextVersion, agentRun.CurrentStateVersion)
	}
	if got := agentRun.Deadline.Sub(agentRun.StartedAt); got != agentTimeoutMs*time.Millisecond {
		t.Errorf("deadline - startedAt = %s, want %s", got, agentTimeoutMs*time.Millisecond)
	}
	if agentRun.Termination != nil || agentRun.TerminatedAt != nil {
		t.Errorf("a freshly created agent run is already terminated: %v", agentRun.Termination)
	}

	contextV0 := agentContextVersion(h.ctx, t, h.uow, agentRun.ID, 0)
	if contextV0.SourceTurnID != nil {
		t.Errorf("context version 0 sourceTurnId = %v, want nil", *contextV0.SourceTurnID)
	}
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(contextV0.Messages, &messages); err != nil {
		t.Fatalf("decode context version 0 messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("context version 0 holds %d messages, want exactly 1", len(messages))
	}
	if messages[0].Role != "user" {
		t.Errorf("context version 0 message role = %q, want user", messages[0].Role)
	}
	var contentText string
	if err := json.Unmarshal(messages[0].Content, &contentText); err != nil {
		t.Fatalf("decode context version 0 message content: %v", err)
	}
	if contentText != agentQuestion {
		t.Errorf("context version 0 message content = %q, want the validated input port text", contentText)
	}

	stateV0 := agentStateVersion(h.ctx, t, h.uow, agentRun.ID, 0)
	assertSameJSON(t, "state version 0", json.RawMessage(`{}`), stateV0.Value)

	readyTurns := listReadyTurns(h.ctx, t, h.uow)
	if len(readyTurns) != 1 {
		t.Fatalf("ready turns = %d, want exactly 1", len(readyTurns))
	}
	turn := readyTurns[0]
	if turn.AgentRunID != agentRun.ID || turn.TurnNo != 1 {
		t.Errorf("ready turn = (agent run %s, turn no %d), want (%s, 1)", turn.AgentRunID, turn.TurnNo, agentRun.ID)
	}
	if len(turn.Request) == 0 {
		t.Errorf("turn 1 has no immutable Request")
	}
	if turn.StartedAt != nil {
		t.Errorf("a READY turn has started_at = %v, want nil: nobody holds the model call right yet", *turn.StartedAt)
	}

	events := listEvents(h.ctx, t, h.uow, run.ID)
	types := agentEventTypesFor(events, nodeRun.ID)
	if len(types) < 2 ||
		types[len(types)-2] != domain.EventAgentStarted ||
		types[len(types)-1] != domain.EventAgentTurnReady {
		t.Errorf("agent node run events = %v, want them to end with AGENT_STARTED, AGENT_TURN_READY", types)
	}
	started := agentEventPayload(t, events, domain.EventAgentStarted)
	if started["agentRunId"] != agentRun.ID {
		t.Errorf("AGENT_STARTED agentRunId = %v, want %s", started["agentRunId"], agentRun.ID)
	}
	if started["modelId"] != mockmodel.ModelID {
		t.Errorf("AGENT_STARTED modelId = %v, want %s", started["modelId"], mockmodel.ModelID)
	}
	turnReady := agentEventPayload(t, events, domain.EventAgentTurnReady)
	if turnReady["turnId"] != turn.ID {
		t.Errorf("AGENT_TURN_READY turnId = %v, want %s", turnReady["turnId"], turn.ID)
	}
}

// TestManagedAgentNodeRun_FirstClaim_UnresolvableTool_FailsNodeRunWithoutAgentRun proves
// docs/06-execution-model.md §1.7: the Agent Run may only be created once every stable
// Tool Name in the frozen allowlist resolves. A Backend restarted without the `lookup`
// Tool must fail the Agent NodeRun explicitly instead of silently dropping the missing
// entry and starting the Agent with a shortened allowlist.
func TestManagedAgentNodeRun_FirstClaim_UnresolvableTool_FailsNodeRunWithoutAgentRun(t *testing.T) {
	first := newAgentHarness(t, agentHarnessOptions{})
	def := first.saveDefinition(agentLoopDefinition("wf-agent-tool-drift"))
	run := first.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)

	inputOutcome := first.advance(run.ID)
	first.execute(inputOutcome)

	// A second Backend over the same committed facts, whose Tool Registry no longer has
	// `lookup`.
	restarted := newAgentHarness(t, agentHarnessOptions{
		Pool: first.pool, SkipLookupTool: true, Clock: first.clock,
	})

	outcome := restarted.advance(run.ID)
	if outcome.Claimed {
		t.Errorf("advance reported a usable claim for an agent whose allowlist cannot be resolved: %+v", outcome)
	}

	nodeRun := agentNodeRun(restarted.ctx, t, restarted.uow, run.ID)
	if nodeRun.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nodeRun.Status)
	}
	if _, found := agentRunOfNodeRun(restarted.ctx, t, restarted.uow, nodeRun.ID); found {
		t.Errorf("an agent run was created even though the frozen Tool allowlist could not be resolved")
	}
	if turns := listReadyTurns(restarted.ctx, t, restarted.uow); len(turns) != 0 {
		t.Errorf("ready turns = %d, want 0", len(turns))
	}
	if got := agentAttemptCount(restarted.ctx, t, restarted.uow, nodeRun.ID); got != 0 {
		t.Errorf("node attempts for the agent node run = %d, want 0", got)
	}

	persisted := agentRunRow(restarted.ctx, t, restarted.uow, run.ID)
	if persisted.Status != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", persisted.Status)
	}
	if persisted.Error == nil {
		t.Fatalf("failed run records no error")
	}

	events := listEvents(restarted.ctx, t, restarted.uow, run.ID)
	types := agentEventTypesFor(events, nodeRun.ID)
	if agentCountEventType(types, domain.EventNodeFailed) != 1 {
		t.Errorf("agent node run events = %v, want exactly one NODE_FAILED", types)
	}
	if agentCountEventType(types, domain.EventAgentStarted) != 0 {
		t.Errorf("agent node run events = %v, want no AGENT_STARTED: no Agent Run exists", types)
	}
	failed := agentEventPayload(t, events, domain.EventNodeFailed)
	execError, ok := failed["error"].(map[string]any)
	if !ok {
		t.Fatalf("NODE_FAILED payload carries no error object: %v", failed)
	}
	if message, _ := execError["message"].(string); message == "" {
		t.Errorf("NODE_FAILED error has no message: %v", execError)
	}
}
