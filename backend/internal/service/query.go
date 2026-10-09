package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// defaultEventsLimit and maxEventsLimit bound QueryService.Events pagination
// (the limit is bounded by the server).
const (
	defaultEventsLimit = 200
	maxEventsLimit     = 1000
)

// QueryService serves the read-only Snapshot, Event replay and Node/Attempt detail
// queries the API and SSE handlers need. It
// never mutates Execution State.
type QueryService struct {
	deps Deps
}

// NewQueryService builds a QueryService from the shared Deps skeleton.
func NewQueryService(deps Deps) *QueryService {
	return &QueryService{deps: deps.withDefaults()}
}

// RunSnapshot is the Run, its NodeRuns and the LastSeq they are mutually consistent with
// — the Snapshot-to-SSE handoff contract: a client resumes the Event stream at exactly
// LastSeq.
type RunSnapshot struct {
	Run      domain.Run
	NodeRuns []domain.NodeRun
	LastSeq  int64
}

// NodeRunDetail is one NodeRun together with its Attempts, ordered by AttemptNo.
// CallbackBindings carries the Callback Binding of
// every Attempt that has one, keyed by Attempt ID, so the API layer can project
// {id, providerId, externalTaskId, createdAt} without a second round trip; a sync
// Attempt (or an async Attempt not yet dispatched) simply has no entry.
type NodeRunDetail struct {
	NodeRun          domain.NodeRun
	Attempts         []domain.NodeAttempt
	CallbackBindings map[string]domain.CallbackBinding
}

// Snapshot reads the Run, its NodeRuns and LastSeq inside one transaction so they are
// mutually consistent: LastSeq comes from the Run row read in the same transaction as the
// NodeRuns, so a client that opens SSE with afterSeq=LastSeq neither misses nor
// re-receives an Event.
func (s *QueryService) Snapshot(ctx context.Context, runID string) (RunSnapshot, error) {
	var snapshot RunSnapshot
	err := s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		// Read the Run first: a stray duplicate Event is tolerated by SSE dedup, but a
		// gap is not, so Run (the source of LastSeq) must never be the one read from the
		// newer half of two straddled snapshots.
		run, err := tx.Runs().Get(ctx, runID)
		if err != nil {
			return err
		}
		nodeRuns, err := tx.NodeRuns().ListByRun(ctx, runID)
		if err != nil {
			return err
		}
		snapshot = RunSnapshot{Run: run, NodeRuns: nodeRuns, LastSeq: run.LastSeq}
		return nil
	})
	if err != nil {
		return RunSnapshot{}, fmt.Errorf("service query.Snapshot: run=%s: %w", runID, err)
	}
	return snapshot, nil
}

// Events replays committed Events with seq > afterSeq, ascending. limit is clamped to
// [1, maxEventsLimit] and defaults to defaultEventsLimit when <= 0.
// It returns domain.ErrNotFound when the Run does not
// exist, distinguishing "no Run" from "Run exists, no Events yet".
func (s *QueryService) Events(ctx context.Context, runID string, afterSeq int64, limit int) ([]domain.Event, error) {
	switch {
	case limit <= 0:
		limit = defaultEventsLimit
	case limit > maxEventsLimit:
		limit = maxEventsLimit
	}

	var events []domain.Event
	err := s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		// Read the Run first (existence check), same reasoning as Snapshot: a stray
		// duplicate Event is tolerable, a gap from straddled snapshots is not.
		if _, err := tx.Runs().Get(ctx, runID); err != nil {
			return err
		}
		var err error
		events, err = tx.Events().ListAfter(ctx, runID, afterSeq, limit)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("service query.Events: run=%s: %w", runID, err)
	}
	return events, nil
}

// NodeRunDetail returns one NodeRun and its Attempts. It returns domain.ErrNotFound when
// nodeRunID belongs to a different Run than runID, so a caller can never read another
// Run's facts by guessing or reusing a NodeRun id.
func (s *QueryService) NodeRunDetail(ctx context.Context, runID, nodeRunID string) (NodeRunDetail, error) {
	var detail NodeRunDetail
	err := s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		nodeRun, err := tx.NodeRuns().Get(ctx, nodeRunID)
		if err != nil {
			return err
		}
		if nodeRun.RunID != runID {
			return domain.ErrNotFound
		}
		attempts, err := tx.NodeAttempts().ListByNodeRun(ctx, nodeRunID)
		if err != nil {
			return err
		}
		attemptIDs := make([]string, len(attempts))
		for i, a := range attempts {
			attemptIDs[i] = a.ID
		}
		bindings, err := tx.CallbackBindings().ListByTargets(ctx, domain.CallbackTargetNodeAttempt, attemptIDs)
		if err != nil {
			return err
		}
		byAttempt := make(map[string]domain.CallbackBinding, len(bindings))
		for _, b := range bindings {
			byAttempt[b.TargetID] = b
		}
		detail = NodeRunDetail{NodeRun: nodeRun, Attempts: attempts, CallbackBindings: byAttempt}
		return nil
	})
	if err != nil {
		return NodeRunDetail{}, fmt.Errorf("service query.NodeRunDetail: run=%s nodeRun=%s: %w", runID, nodeRunID, err)
	}
	return detail, nil
}

// AgentTrace is one Agent NodeRun's Agent Run together with its Turns in persisted order.
// It is a read-only expansion of committed facts: the
// service reads, the API projects, and neither may mutate or truncate the authoritative
// Execution State the Runtime recovers from.
type AgentTrace struct {
	AgentRun domain.AgentRun
	Turns    []AgentTraceTurn
	// Facts is the Agent Run's execution fact ledger and Budget its generation budget,
	// both read in the same transaction as the Turns.
	Facts  AgentTraceFacts
	Budget AgentGenerationBudget
}

// AgentTraceTurn is one round: the Turn, the single Decision and Action it committed (both
// absent while the Turn is still READY or RUNNING, and on a FAILED Turn that never
// decided), and the Tool Attempts that Action made. CallbackBindings holds the Binding of
// every Tool Attempt that has one, keyed by Tool Attempt ID, mirroring NodeRunDetail: a
// synchronous Tool call simply has no entry.
type AgentTraceTurn struct {
	Turn             domain.AgentTurn
	Decision         *domain.AgentDecision
	Action           *domain.AgentAction
	ToolAttempts     []domain.ToolAttempt
	CallbackBindings map[string]domain.CallbackBinding
}

// AgentTrace reads one Agent NodeRun's Agent Run, Turns, Decisions, Actions, Tool
// Attempts, execution facts and generation budget in a single read transaction, so the
// returned rounds, ledger and budget are mutually consistent.
//
// It returns domain.ErrNotFound when the NodeRun does not exist, belongs to a different
// Run than runID, or has no Agent Run: from this query's point of view all three mean
// there is no Agent Trace resource at that path, and none of them may leak the existence
// of another Run's NodeRun.
func (s *QueryService) AgentTrace(ctx context.Context, runID, nodeRunID string) (AgentTrace, error) {
	var trace AgentTrace
	err := s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		nodeRun, err := tx.NodeRuns().Get(ctx, nodeRunID)
		if err != nil {
			return err
		}
		if nodeRun.RunID != runID {
			return domain.ErrNotFound
		}
		// The Agent Run row is the persisted fact that this NodeRun is a MANAGED_AGENT
		// one that has started; a NodeRun of any other Node Type has none, and neither
		// does an Agent NodeRun whose start transaction has not committed yet.
		agentRun, err := tx.AgentRuns().GetByNodeRunID(ctx, nodeRunID)
		if err != nil {
			return err
		}
		turns, err := tx.AgentTurns().ListByAgentRunID(ctx, agentRun.ID)
		if err != nil {
			return err
		}

		rounds := make([]AgentTraceTurn, 0, len(turns))
		for _, turn := range turns {
			round := AgentTraceTurn{Turn: turn}

			action, err := tx.AgentActions().GetByTurnID(ctx, turn.ID)
			switch {
			case errors.Is(err, domain.ErrNotFound):
				// A READY or RUNNING Turn, or a FAILED one, committed no Decision and no
				// Action: the round is projected as it was persisted, not filled in.
				rounds = append(rounds, round)
				continue
			case err != nil:
				return err
			}
			round.Action = &action

			decision, err := tx.AgentDecisions().Get(ctx, action.DecisionID)
			if err != nil {
				return err
			}
			round.Decision = &decision

			attempts, err := tx.ToolAttempts().ListByActionID(ctx, action.ID)
			if err != nil {
				return err
			}
			round.ToolAttempts = attempts

			attemptIDs := make([]string, len(attempts))
			for i, a := range attempts {
				attemptIDs[i] = a.ID
			}
			bindings, err := tx.CallbackBindings().ListByTargets(ctx, domain.CallbackTargetToolAttempt, attemptIDs)
			if err != nil {
				return err
			}
			byAttempt := make(map[string]domain.CallbackBinding, len(bindings))
			for _, b := range bindings {
				byAttempt[b.TargetID] = b
			}
			round.CallbackBindings = byAttempt

			rounds = append(rounds, round)
		}
		facts, err := agentTraceFacts(ctx, tx, agentRun.ID)
		if err != nil {
			return err
		}
		budget, err := s.agentGenerationBudget(ctx, tx, agentRun)
		if err != nil {
			return err
		}
		trace = AgentTrace{AgentRun: agentRun, Turns: rounds, Facts: facts, Budget: budget}
		return nil
	})
	if err != nil {
		return AgentTrace{}, fmt.Errorf("service query.AgentTrace: run=%s nodeRun=%s: %w", runID, nodeRunID, err)
	}
	return trace, nil
}
