// Package postgres implements the store ports with pgx and explicit SQL. It persists
// facts and performs conditional updates; it never calls a Provider, publishes SSE or
// decides the next Runtime step.
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leungll/Emberling/backend/internal/store"
)

// UnitOfWork owns transaction boundaries for the PostgreSQL Store.
type UnitOfWork struct {
	pool *pgxpool.Pool
}

// NewUnitOfWork returns a Unit of Work backed by the given connection pool.
func NewUnitOfWork(pool *pgxpool.Pool) *UnitOfWork {
	return &UnitOfWork{pool: pool}
}

// WithinTx runs fn inside a single transaction. The transaction is committed only when
// fn returns nil; any error, including a failed Event insert, rolls back every write
// made inside it.
func (u *UnitOfWork) WithinTx(ctx context.Context, fn func(ctx context.Context, tx store.Tx) error) error {
	pgxTx, err := u.pool.Begin(ctx)
	if err != nil {
		return mapError("begin transaction", err)
	}

	// Rollback after a successful Commit is a no-op, so this covers panics and early
	// returns without a second control path.
	defer func() { _ = pgxTx.Rollback(ctx) }()

	if err := fn(ctx, newTx(pgxTx)); err != nil {
		return err
	}

	if err := pgxTx.Commit(ctx); err != nil {
		return mapError("commit transaction", err)
	}
	return nil
}

// WithinReadTx runs fn inside one read-only transaction that observes a single
// consistent snapshot for every statement it issues (PostgreSQL REPEATABLE READ, not the
// default READ COMMITTED's fresh snapshot per statement). This is what the
// Snapshot-to-SSE handoff contract needs (lastSeq and the Snapshot are taken in the
// same consistent read): Run and NodeRuns are two separate statements, and
// under READ COMMITTED a commit landing between them could advance one but not the
// other. fn must not write: PostgreSQL rejects any write statement in a read-only
// transaction (SQLSTATE 25006).
func (u *UnitOfWork) WithinReadTx(ctx context.Context, fn func(ctx context.Context, tx store.Tx) error) error {
	pgxTx, err := u.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return mapError("begin read transaction", err)
	}
	defer func() { _ = pgxTx.Rollback(ctx) }()

	if err := fn(ctx, newTx(pgxTx)); err != nil {
		return err
	}

	if err := pgxTx.Commit(ctx); err != nil {
		return mapError("commit read transaction", err)
	}
	return nil
}

// tx binds the repositories to one active transaction.
type tx struct {
	definitions      *definitionRepository
	assets           *assetRepository
	artifacts        *artifactRepository
	runs             *runRepository
	nodeRuns         *nodeRunRepository
	nodeAttempts     *nodeAttemptRepository
	callbackBindings *callbackBindingRepository
	pendingCallbacks *pendingCallbackRepository
	events           *eventRepository

	agentRuns            *agentRunRepository
	agentTurns           *agentTurnRepository
	agentDecisions       *agentDecisionRepository
	agentActions         *agentActionRepository
	toolAttempts         *toolAttemptRepository
	agentContextVersions *agentContextVersionRepository
	agentStateVersions   *agentStateVersionRepository
	executionFacts       *executionFactRepository
}

func newTx(conn pgx.Tx) *tx {
	return &tx{
		definitions:      &definitionRepository{conn: conn},
		assets:           &assetRepository{conn: conn},
		artifacts:        &artifactRepository{conn: conn},
		runs:             &runRepository{conn: conn},
		nodeRuns:         &nodeRunRepository{conn: conn},
		nodeAttempts:     &nodeAttemptRepository{conn: conn},
		callbackBindings: &callbackBindingRepository{conn: conn},
		pendingCallbacks: &pendingCallbackRepository{conn: conn},
		events:           &eventRepository{conn: conn},

		agentRuns:            &agentRunRepository{conn: conn},
		agentTurns:           &agentTurnRepository{conn: conn},
		agentDecisions:       &agentDecisionRepository{conn: conn},
		agentActions:         &agentActionRepository{conn: conn},
		toolAttempts:         &toolAttemptRepository{conn: conn},
		agentContextVersions: &agentContextVersionRepository{conn: conn},
		agentStateVersions:   &agentStateVersionRepository{conn: conn},
		executionFacts:       &executionFactRepository{conn: conn},
	}
}

func (t *tx) Definitions() store.DefinitionRepository   { return t.definitions }
func (t *tx) Assets() store.AssetRepository             { return t.assets }
func (t *tx) Artifacts() store.ArtifactRepository       { return t.artifacts }
func (t *tx) Runs() store.RunRepository                 { return t.runs }
func (t *tx) NodeRuns() store.NodeRunRepository         { return t.nodeRuns }
func (t *tx) NodeAttempts() store.NodeAttemptRepository { return t.nodeAttempts }
func (t *tx) Events() store.EventRepository             { return t.events }

func (t *tx) CallbackBindings() store.CallbackBindingRepository { return t.callbackBindings }
func (t *tx) PendingCallbacks() store.PendingCallbackRepository { return t.pendingCallbacks }

func (t *tx) AgentRuns() store.AgentRunRepository           { return t.agentRuns }
func (t *tx) AgentTurns() store.AgentTurnRepository         { return t.agentTurns }
func (t *tx) AgentDecisions() store.AgentDecisionRepository { return t.agentDecisions }
func (t *tx) AgentActions() store.AgentActionRepository     { return t.agentActions }
func (t *tx) ToolAttempts() store.ToolAttemptRepository     { return t.toolAttempts }

func (t *tx) AgentContextVersions() store.AgentContextVersionRepository {
	return t.agentContextVersions
}
func (t *tx) AgentStateVersions() store.AgentStateVersionRepository { return t.agentStateVersions }
func (t *tx) ExecutionFacts() store.ExecutionFactRepository         { return t.executionFacts }

var (
	_ store.UnitOfWork = (*UnitOfWork)(nil)
	_ store.Tx         = (*tx)(nil)
)

// affectedRows returns the number of rows a conditional update changed. Every claim and
// conditional state change checks it: zero rows means another advancement path won.
func affectedRows(ctx context.Context, conn pgx.Tx, op, sql string, args ...any) (int64, error) {
	tag, err := conn.Exec(ctx, sql, args...)
	if err != nil {
		return 0, mapError(op, err)
	}
	return tag.RowsAffected(), nil
}

// errUnexpectedRows reports a conditional update that matched more rows than its
// identity predicate allows. It signals a schema or query defect, not a lost race.
func errUnexpectedRows(op string, id string, affected int64) error {
	return fmt.Errorf("store/postgres %s: id=%s affected %d rows, want at most 1", op, id, affected)
}
