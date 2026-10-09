//go:build integration

// Package integration: this file covers the persisted execution facts and the frozen
// generation limit against a real PostgreSQL — a fact round-trips with its binding and
// provenance, a second fact of the same type for the same Tool Attempt fails and rolls
// the whole transaction back, the newest fact about a subject wins, the generation
// limit is stored as frozen configuration, and the calls already made are derived from
// persisted Tool Attempts rather than a counter.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/migrations"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// ---------------------------------------------------------------------------
// Execution facts
// ---------------------------------------------------------------------------

func TestExecutionFactStore_InsertAndRead_RoundTripsFact(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_1", "turn_1", "decision_1", "action_1")
	createToolAttempt(ctx, t, uow, newToolAttempt("tool_attempt_1", "action_1", 1))

	want := newExecutionFact("fact_gen_1", f.runID, "ar_1", "tool_attempt_1", "image_generated", "asset_out_1")
	want.Binding = json.RawMessage(`{"photoAssetId":"asset_photo_1","settingsDigest":"sha256:ab"}`)

	stored := insertExecutionFact(ctx, t, uow, want)
	if stored.ID != want.ID || stored.RunID != f.runID || stored.AgentRunID != "ar_1" ||
		stored.ToolAttemptID != "tool_attempt_1" || stored.FactType != "image_generated" ||
		stored.SubjectRef != "asset_out_1" {
		t.Fatalf("Insert returned %+v, want identity of %+v", stored, want)
	}
	assertSameJSON(t, "returned binding", want.Binding, stored.Binding)
	if stored.Verdict != nil || stored.BasisFactID != nil {
		t.Fatalf("a generation fact carries no verdict or basis, got %v/%v", stored.Verdict, stored.BasisFactID)
	}
	if !stored.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("created_at: got %s, want %s", stored.CreatedAt, want.CreatedAt)
	}

	// A fact without binding values is stored as the empty object, never as NULL.
	noBinding := newExecutionFact("fact_gen_2", f.runID, "ar_1", "tool_attempt_1", "image_described", "asset_out_1")
	noBinding.CreatedAt = fixtureTime.Add(time.Second)
	insertExecutionFact(ctx, t, uow, noBinding)

	facts := listFactsByAgentRun(ctx, t, uow, "ar_1")
	if len(facts) != 2 || facts[0].ID != "fact_gen_1" || facts[1].ID != "fact_gen_2" {
		t.Fatalf("ListByAgentRun: want [fact_gen_1 fact_gen_2] oldest first, got %+v", facts)
	}
	assertSameJSON(t, "stored binding", want.Binding, facts[0].Binding)
	assertSameJSON(t, "empty binding", json.RawMessage(`{}`), facts[1].Binding)

	if other := listFactsByAgentRun(ctx, t, uow, "ar_other"); len(other) != 0 {
		t.Fatalf("ListByAgentRun for an unknown Agent Run: want empty, got %+v", other)
	}
}

// The same Tool result must not record the same fact twice. The duplicate is reported
// with a stable error, and rolling back discards every write of that transaction,
// including the first fact it inserted.
func TestExecutionFactStore_DuplicateToolAttemptAndType_RollsBackWholeTransaction(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_1", "turn_1", "decision_1", "action_1")
	createToolAttempt(ctx, t, uow, newToolAttempt("tool_attempt_1", "action_1", 1))

	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.ExecutionFacts().Insert(ctx,
			newExecutionFact("fact_1", f.runID, "ar_1", "tool_attempt_1", "image_generated", "asset_out_1")); err != nil {
			t.Fatalf("first Insert: %v", err)
		}
		_, err := tx.ExecutionFacts().Insert(ctx,
			newExecutionFact("fact_dup", f.runID, "ar_1", "tool_attempt_1", "image_generated", "asset_out_2"))
		return err
	})
	if !errors.Is(err, domain.ErrExecutionFactDuplicate) {
		t.Fatalf("duplicate (tool_attempt_id, fact_type): want domain.ErrExecutionFactDuplicate, got %v", err)
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate fact must still match domain.ErrConflict, got %v", err)
	}

	if facts := listFactsByAgentRun(ctx, t, uow, "ar_1"); len(facts) != 0 {
		t.Fatalf("facts after the rolled-back transaction: want none, got %+v", facts)
	}

	// A different fact type for the same Tool Attempt is a distinct fact.
	insertExecutionFact(ctx, t, uow,
		newExecutionFact("fact_a", f.runID, "ar_1", "tool_attempt_1", "image_generated", "asset_out_1"))
	insertExecutionFact(ctx, t, uow,
		newExecutionFact("fact_b", f.runID, "ar_1", "tool_attempt_1", "image_described", "asset_out_1"))
	if facts := listFactsByAgentRun(ctx, t, uow, "ar_1"); len(facts) != 2 {
		t.Fatalf("two fact types for one Tool Attempt: want 2 facts, got %+v", facts)
	}
}

// A review fact points at the generation fact it reviewed. The reference is a foreign
// key, so a fact cannot claim provenance from a fact that was never committed.
func TestExecutionFactStore_BasisChain_ReferencesCommittedFact(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	seedToolAttempts(ctx, t, uow, f, "ar_1", []string{"generate_image", "review_asset"})

	insertExecutionFact(ctx, t, uow,
		newExecutionFact("fact_gen", f.runID, "ar_1", "tool_attempt_ar_1_1", "image_generated", "asset_out_1"))

	basis := "fact_gen"
	passed := true
	review := newExecutionFact("fact_review", f.runID, "ar_1", "tool_attempt_ar_1_2", "asset_reviewed", "asset_out_1")
	review.Verdict = &passed
	review.BasisFactID = &basis
	review.Binding = json.RawMessage(`{"photoAssetId":"asset_photo_1","policyVersion":"v1"}`)
	review.CreatedAt = fixtureTime.Add(time.Second)
	insertExecutionFact(ctx, t, uow, review)

	got := findLatestFact(ctx, t, uow, f.runID, "asset_reviewed", "asset_out_1")
	if got == nil || got.ID != "fact_review" {
		t.Fatalf("FindLatest review fact: got %+v, want fact_review", got)
	}
	if got.Verdict == nil || !*got.Verdict {
		t.Fatalf("review verdict: got %v, want true", got.Verdict)
	}
	if got.BasisFactID == nil || *got.BasisFactID != "fact_gen" {
		t.Fatalf("review basis: got %v, want fact_gen", got.BasisFactID)
	}
	// Basis resolution finds the generation fact by type and the reviewed subject.
	gen := findLatestFact(ctx, t, uow, f.runID, "image_generated", got.SubjectRef)
	if gen == nil || gen.ID != *got.BasisFactID || gen.ToolAttemptID != "tool_attempt_ar_1_1" {
		t.Fatalf("following the basis chain: got %+v, want fact_gen from tool_attempt_ar_1_1", gen)
	}

	missing := "fact_never_committed"
	dangling := newExecutionFact("fact_dangling", f.runID, "ar_1", "tool_attempt_ar_1_2", "asset_rechecked", "asset_out_1")
	dangling.BasisFactID = &missing
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.ExecutionFacts().Insert(ctx, dangling)
		return err
	})
	if !errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrExecutionFactDuplicate) {
		t.Fatalf("basis pointing at no fact: want domain.ErrConflict (not a duplicate), got %v", err)
	}
}

// Precondition checks and basis resolution read the newest fact about a subject. Newer
// created_at wins; equal created_at falls back to the id so the choice is deterministic.
func TestExecutionFactStore_FindLatest_ReturnsNewestForSubject(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	seedToolAttempts(ctx, t, uow, f, "ar_1", []string{"review_asset", "review_asset", "review_asset", "review_asset"})

	failed, passed := false, true
	older := newExecutionFact("fact_r1", f.runID, "ar_1", "tool_attempt_ar_1_1", "asset_reviewed", "asset_a")
	older.Verdict = &failed
	newer := newExecutionFact("fact_r2", f.runID, "ar_1", "tool_attempt_ar_1_2", "asset_reviewed", "asset_a")
	newer.Verdict = &passed
	newer.CreatedAt = fixtureTime.Add(time.Minute)
	sameTimeHigherID := newExecutionFact("fact_r3", f.runID, "ar_1", "tool_attempt_ar_1_3", "asset_reviewed", "asset_b")
	sameTimeHigherID.CreatedAt = fixtureTime.Add(time.Minute)
	sameTimeLowerID := newExecutionFact("fact_r0", f.runID, "ar_1", "tool_attempt_ar_1_4", "asset_reviewed", "asset_b")
	sameTimeLowerID.CreatedAt = fixtureTime.Add(time.Minute)
	// Insert the newer fact first so insertion order cannot explain the result.
	for _, fact := range []domain.ExecutionFact{newer, older, sameTimeHigherID, sameTimeLowerID} {
		insertExecutionFact(ctx, t, uow, fact)
	}

	got := findLatestFact(ctx, t, uow, f.runID, "asset_reviewed", "asset_a")
	if got == nil || got.ID != "fact_r2" || got.Verdict == nil || !*got.Verdict {
		t.Fatalf("FindLatest asset_a: got %+v, want fact_r2 with verdict true", got)
	}
	if got := findLatestFact(ctx, t, uow, f.runID, "asset_reviewed", "asset_b"); got == nil || got.ID != "fact_r3" {
		t.Fatalf("FindLatest asset_b with equal created_at: got %+v, want fact_r3 (higher id)", got)
	}
	if got := findLatestFact(ctx, t, uow, f.runID, "asset_reviewed", "asset_missing"); got != nil {
		t.Fatalf("FindLatest for an unknown subject: want nil, got %+v", got)
	}
	if got := findLatestFact(ctx, t, uow, f.runID, "image_generated", "asset_a"); got != nil {
		t.Fatalf("FindLatest for another fact type: want nil, got %+v", got)
	}
	if got := findLatestFact(ctx, t, uow, "run_other", "asset_reviewed", "asset_a"); got != nil {
		t.Fatalf("FindLatest in another Run: want nil, got %+v", got)
	}

	// The requirement query returns the same order, optionally narrowed to one subject,
	// and never more than the requested bound.
	all := listFactsForRequirement(ctx, t, uow, f.runID, "asset_reviewed", nil, 10)
	if ids := factIDs(all); len(ids) != 4 || ids[0] != "fact_r3" || ids[1] != "fact_r2" ||
		ids[2] != "fact_r0" || ids[3] != "fact_r1" {
		t.Fatalf("ListForRequirement without subject: want [fact_r3 fact_r2 fact_r0 fact_r1], got %v", ids)
	}
	subject := "asset_a"
	if ids := factIDs(listFactsForRequirement(ctx, t, uow, f.runID, "asset_reviewed", &subject, 10)); len(ids) != 2 ||
		ids[0] != "fact_r2" || ids[1] != "fact_r1" {
		t.Fatalf("ListForRequirement for asset_a: want [fact_r2 fact_r1], got %v", ids)
	}
	if ids := factIDs(listFactsForRequirement(ctx, t, uow, f.runID, "asset_reviewed", nil, 1)); len(ids) != 1 ||
		ids[0] != "fact_r3" {
		t.Fatalf("ListForRequirement with limit 1: want [fact_r3], got %v", ids)
	}
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.ExecutionFacts().ListForRequirement(ctx, f.runID, "asset_reviewed", nil, 0)
		return err
	})
	if err == nil {
		t.Fatal("ListForRequirement with a zero limit: want an error, got an unbounded read")
	}
}

// A node that declares fact inputs receives one Run's facts of one type, oldest first and
// bounded; facts of another type or another Run never leak into that read.
func TestExecutionFactStore_ListByRunAndType_OldestFirstScopedAndBounded(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	seedToolAttempts(ctx, t, uow, f, "ar_1", []string{"generate_image", "generate_image", "generate_image", "review_asset"})
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.Runs().Create(ctx, domain.Run{
			ID: "run_other", WorkflowID: f.workflowID, DefinitionVersion: 1, Status: domain.RunRunning,
			Input: json.RawMessage(`{}`), StartedAt: fixtureTime, UpdatedAt: fixtureTime,
		}); err != nil {
			return err
		}
		return tx.NodeRuns().Create(ctx, newNodeRun("nr_other", "run_other", "node_input"))
	}); err != nil {
		t.Fatalf("seed another Run: %v", err)
	}
	seedToolAttemptsOn(ctx, t, uow, "nr_other", "ar_other", []string{"generate_image"})

	newest := newExecutionFact("fact_g_newest", f.runID, "ar_1", "tool_attempt_ar_1_1", "image_generated", "asset_c")
	newest.CreatedAt = fixtureTime.Add(2 * time.Minute)
	sameTimeHigherID := newExecutionFact("fact_g_b", f.runID, "ar_1", "tool_attempt_ar_1_2", "image_generated", "asset_b")
	sameTimeHigherID.CreatedAt = fixtureTime.Add(time.Minute)
	sameTimeLowerID := newExecutionFact("fact_g_a", f.runID, "ar_1", "tool_attempt_ar_1_3", "image_generated", "asset_a")
	sameTimeLowerID.CreatedAt = fixtureTime.Add(time.Minute)
	otherType := newExecutionFact("fact_r_1", f.runID, "ar_1", "tool_attempt_ar_1_4", "asset_reviewed", "asset_a")
	otherRun := newExecutionFact("fact_g_other_run", "run_other", "ar_other", "tool_attempt_ar_other_1", "image_generated", "asset_x")
	// Insert the newest first so insertion order cannot explain the result.
	for _, fact := range []domain.ExecutionFact{newest, sameTimeHigherID, otherType, otherRun, sameTimeLowerID} {
		insertExecutionFact(ctx, t, uow, fact)
	}

	if ids := factIDs(listFactsByRunAndType(ctx, t, uow, f.runID, "image_generated", 10)); len(ids) != 3 ||
		ids[0] != "fact_g_a" || ids[1] != "fact_g_b" || ids[2] != "fact_g_newest" {
		t.Fatalf("ListByRunAndType: want [fact_g_a fact_g_b fact_g_newest], got %v", ids)
	}
	if ids := factIDs(listFactsByRunAndType(ctx, t, uow, f.runID, "image_generated", 2)); len(ids) != 2 ||
		ids[0] != "fact_g_a" || ids[1] != "fact_g_b" {
		t.Fatalf("ListByRunAndType with limit 2: want the two oldest [fact_g_a fact_g_b], got %v", ids)
	}
	if ids := factIDs(listFactsByRunAndType(ctx, t, uow, "run_other", "image_generated", 10)); len(ids) != 1 ||
		ids[0] != "fact_g_other_run" {
		t.Fatalf("ListByRunAndType for another Run: want [fact_g_other_run], got %v", ids)
	}
	if facts := listFactsByRunAndType(ctx, t, uow, f.runID, "video_generated", 10); facts == nil || len(facts) != 0 {
		t.Fatalf("ListByRunAndType for a type without facts: want an empty list, got %#v", facts)
	}
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.ExecutionFacts().ListByRunAndType(ctx, f.runID, "image_generated", 0)
		return err
	})
	if err == nil {
		t.Fatal("ListByRunAndType with a zero limit: want an error, got an unbounded read")
	}
}

// ---------------------------------------------------------------------------
// Generation limit: frozen value and the calls already made
// ---------------------------------------------------------------------------

func TestAgentRunStore_MaxGenerationCalls_RoundTripsNullAndValue(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	createNodeRun(ctx, t, uow, newNodeRun("nr_limited", f.runID, "node_limited"))

	unlimited := newAgentRun("ar_unlimited", f.nodeRunID)
	createAgentRun(ctx, t, uow, unlimited)

	limit := 12
	limited := newAgentRun("ar_limited", "nr_limited")
	limited.MaxGenerationCalls = &limit
	createAgentRun(ctx, t, uow, limited)

	if got := getAgentRun(ctx, t, uow, "ar_unlimited").MaxGenerationCalls; got != nil {
		t.Fatalf("unconfigured generation limit: want nil, got %d", *got)
	}
	got := getAgentRun(ctx, t, uow, "ar_limited").MaxGenerationCalls
	if got == nil || *got != 12 {
		t.Fatalf("frozen generation limit: want 12, got %v", got)
	}

	if _, err := pool.Exec(ctx, `UPDATE agent_runs SET max_generation_calls = -1 WHERE id = 'ar_limited'`); err == nil {
		t.Fatal("negative generation limit: want CHECK violation, got accepted")
	}
}

// The calls already made are derived from persisted Tool Attempts of the counted Tools,
// in every Attempt status, within one Agent Run only. An Action that never got an
// Attempt does not count.
func TestToolAttemptStore_CountByAgentRunForTools_CountsEveryAttemptOfListedTools(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	// Turns 1-4 call generate_image, turn 5 calls review_asset.
	seedToolAttempts(ctx, t, uow, f, "ar_1",
		[]string{"generate_image", "generate_image", "generate_image", "generate_image", "review_asset"})

	// Move the generation Attempts through every status: STARTED stays, then
	// DISPATCHED, SUCCEEDED and FAILED.
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.ToolAttempts().MarkDispatched(ctx, "tool_attempt_ar_1_2", fixtureTime); err != nil {
			return err
		}
		if err := tx.ToolAttempts().MarkSucceeded(ctx, "tool_attempt_ar_1_3", domain.ToolAttemptStarted, fixtureTime,
			json.RawMessage(`{"assetId":"asset_out_3"}`)); err != nil {
			return err
		}
		return tx.ToolAttempts().MarkFailed(ctx, "tool_attempt_ar_1_4", domain.ToolAttemptStarted, fixtureTime,
			domain.ExecutionError{Code: "TOOL_ERROR", Message: "provider rejected the request"})
	}); err != nil {
		t.Fatalf("advance Tool Attempts: %v", err)
	}

	// A sixth Turn whose Action was never claimed has no Attempt.
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return seedTurnAction(ctx, tx, "ar_1", 6)
	}); err != nil {
		t.Fatalf("seed unclaimed Action: %v", err)
	}

	// Another Agent Run's generation calls belong to its own limit.
	createNodeRun(ctx, t, uow, newNodeRun("nr_other", f.runID, "node_other"))
	seedToolAttemptsOn(ctx, t, uow, "nr_other", "ar_2", []string{"generate_image"})

	cases := []struct {
		name  string
		agent string
		tools []string
		want  int
	}{
		{"generation tool in every status", "ar_1", []string{"generate_image"}, 4},
		{"several counted tools", "ar_1", []string{"generate_image", "review_asset"}, 5},
		{"tool never called", "ar_1", []string{"generate_video"}, 0},
		{"no counted tools", "ar_1", nil, 0},
		{"other Agent Run", "ar_2", []string{"generate_image"}, 1},
	}
	for _, tc := range cases {
		var got int
		if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			got, err = tx.ToolAttempts().CountByAgentRunForTools(ctx, tc.agent, tc.tools)
			return err
		}); err != nil {
			t.Fatalf("%s: CountByAgentRunForTools: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: CountByAgentRunForTools(%s, %v) = %d, want %d", tc.name, tc.agent, tc.tools, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Migration on top of existing data
// ---------------------------------------------------------------------------

// An Agent Run created before the generation limit existed reads back as unlimited,
// and the facts table is usable once the forward migration applies.
func TestMigrations_ExecutionFacts_AppliesOnTopOfExistingAgentRuns(t *testing.T) {
	ctx := context.Background()
	pool := testdb.OpenUnmigrated(t)

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose dialect: %v", err)
	}
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	if err := goose.UpToContext(ctx, db, ".", 2); err != nil {
		t.Fatalf("apply schema before execution facts: %v", err)
	}

	uow := postgres.NewUnitOfWork(pool)
	f := seedRun(ctx, t, uow)
	// Raw SQL: the Store already maps max_generation_calls, which does not exist yet here.
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_runs (id, node_run_id, instructions, model_id, model_config, allowed_tools,
		                        context_schema, state_schema, output_schema, max_turns, current_turn_no,
		                        current_context_version, current_state_version, started_at, deadline)
		VALUES ('ar_legacy', $1, 'legacy', 'mock/deterministic-1', '{}', '["lookup"]',
		        '{}', '{}', '{}', 4, 1, 0, 0, $2, $2)`, f.nodeRunID, fixtureTime); err != nil {
		t.Fatalf("insert legacy Agent Run: %v", err)
	}

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate on top of existing Agent Runs: %v", err)
	}

	if got := getAgentRun(ctx, t, uow, "ar_legacy").MaxGenerationCalls; got != nil {
		t.Fatalf("legacy Agent Run generation limit: want nil (unlimited), got %d", *got)
	}
	if facts := listFactsByAgentRun(ctx, t, uow, "ar_legacy"); len(facts) != 0 {
		t.Fatalf("legacy Agent Run facts: want none, got %+v", facts)
	}
}

// ---------------------------------------------------------------------------
// Fixtures and helpers
// ---------------------------------------------------------------------------

func newExecutionFact(id, runID, agentRunID, toolAttemptID, factType, subjectRef string) domain.ExecutionFact {
	return domain.ExecutionFact{
		ID:            id,
		RunID:         runID,
		AgentRunID:    agentRunID,
		ToolAttemptID: toolAttemptID,
		FactType:      factType,
		SubjectRef:    subjectRef,
		CreatedAt:     fixtureTime,
	}
}

// seedToolAttempts creates an Agent Run on the fixture NodeRun with one Turn, Decision,
// Action and STARTED Tool Attempt per entry of toolNames. The Attempt of Turn n is
// "tool_attempt_<agentRunID>_<n>".
func seedToolAttempts(ctx context.Context, t *testing.T, uow store.UnitOfWork, f fixture, agentRunID string, toolNames []string) {
	t.Helper()
	seedToolAttemptsOn(ctx, t, uow, f.nodeRunID, agentRunID, toolNames)
}

func seedToolAttemptsOn(ctx context.Context, t *testing.T, uow store.UnitOfWork, nodeRunID, agentRunID string, toolNames []string) {
	t.Helper()
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.AgentRuns().Create(ctx, newAgentRun(agentRunID, nodeRunID)); err != nil {
			return err
		}
		for i, toolName := range toolNames {
			turnNo := i + 1
			if err := seedTurnAction(ctx, tx, agentRunID, turnNo); err != nil {
				return err
			}
			attempt := newToolAttempt(seedID("tool_attempt", agentRunID, turnNo), seedID("action", agentRunID, turnNo), 1)
			attempt.ToolName = toolName
			if err := tx.ToolAttempts().Create(ctx, attempt); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed Tool Attempts for %s: %v", agentRunID, err)
	}
}

// seedTurnAction creates Turn turnNo with its Decision and READY Action.
func seedTurnAction(ctx context.Context, tx store.Tx, agentRunID string, turnNo int) error {
	turnID := seedID("turn", agentRunID, turnNo)
	decisionID := seedID("decision", agentRunID, turnNo)
	if err := tx.AgentTurns().Create(ctx, newAgentTurn(turnID, agentRunID, turnNo)); err != nil {
		return err
	}
	if err := tx.AgentDecisions().Create(ctx, newAgentDecision(decisionID, turnID)); err != nil {
		return err
	}
	return tx.AgentActions().Create(ctx, newAgentAction(seedID("action", agentRunID, turnNo), turnID, decisionID))
}

func seedID(prefix, agentRunID string, turnNo int) string {
	return prefix + "_" + agentRunID + "_" + strconv.Itoa(turnNo)
}

func createNodeRun(ctx context.Context, t *testing.T, uow store.UnitOfWork, nr domain.NodeRun) {
	t.Helper()
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeRuns().Create(ctx, nr)
	}); err != nil {
		t.Fatalf("create NodeRun %s: %v", nr.ID, err)
	}
}

func insertExecutionFact(ctx context.Context, t *testing.T, uow store.UnitOfWork, fact domain.ExecutionFact) domain.ExecutionFact {
	t.Helper()
	var stored domain.ExecutionFact
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		stored, err = tx.ExecutionFacts().Insert(ctx, fact)
		return err
	}); err != nil {
		t.Fatalf("insert execution fact %s: %v", fact.ID, err)
	}
	return stored
}

func findLatestFact(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID, factType, subjectRef string) *domain.ExecutionFact {
	t.Helper()
	var fact *domain.ExecutionFact
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		fact, err = tx.ExecutionFacts().FindLatest(ctx, runID, factType, subjectRef)
		return err
	}); err != nil {
		t.Fatalf("FindLatest %s/%s/%s: %v", runID, factType, subjectRef, err)
	}
	return fact
}

func listFactsForRequirement(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID, factType string, subjectRef *string, limit int) []domain.ExecutionFact {
	t.Helper()
	var facts []domain.ExecutionFact
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		facts, err = tx.ExecutionFacts().ListForRequirement(ctx, runID, factType, subjectRef, limit)
		return err
	}); err != nil {
		t.Fatalf("ListForRequirement %s/%s: %v", runID, factType, err)
	}
	return facts
}

func listFactsByAgentRun(ctx context.Context, t *testing.T, uow store.UnitOfWork, agentRunID string) []domain.ExecutionFact {
	t.Helper()
	var facts []domain.ExecutionFact
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		facts, err = tx.ExecutionFacts().ListByAgentRun(ctx, agentRunID)
		return err
	}); err != nil {
		t.Fatalf("ListByAgentRun %s: %v", agentRunID, err)
	}
	return facts
}

func factIDs(facts []domain.ExecutionFact) []string {
	ids := make([]string, 0, len(facts))
	for _, fact := range facts {
		ids = append(ids, fact.ID)
	}
	return ids
}

func listFactsByRunAndType(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID, factType string, limit int) []domain.ExecutionFact {
	t.Helper()
	var facts []domain.ExecutionFact
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		facts, err = tx.ExecutionFacts().ListByRunAndType(ctx, runID, factType, limit)
		return err
	}); err != nil {
		t.Fatalf("ListByRunAndType %s/%s: %v", runID, factType, err)
	}
	return facts
}
