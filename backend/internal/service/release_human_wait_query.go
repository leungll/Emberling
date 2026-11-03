package service

import (
	"context"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// HumanWaitReason says why a release was handed to a person: production never accounted for
// the operation (OUTCOME_UNKNOWN), or it knew the operation but had not confirmed it by the
// confirm deadline (NOT_CONFIRMED).
type HumanWaitReason string

const (
	HumanWaitReasonOutcomeUnknown HumanWaitReason = "OUTCOME_UNKNOWN"
	HumanWaitReasonNotConfirmed   HumanWaitReason = "NOT_CONFIRMED"
)

// HumanWaitView is one OPEN human wait with what a person needs to decide whether to stop
// waiting: the release target, the operation that holds it, the confirm deadline that
// passed and why the release has no answer. It is a read-only projection of committed facts.
type HumanWaitView struct {
	Wait            domain.HumanWait
	ToolName        string
	WorkflowID      string
	NodeRunID       string
	AgentRunID      string
	TargetKey       string
	OperationKey    string
	ConfirmDeadline *time.Time
	Reason          HumanWaitReason
}

// maxHumanWaitListItems bounds the human wait list read.
const maxHumanWaitListItems = 200

// ListOpenHumanWaits returns the OPEN human waits a person can still act on, earliest
// deadline first, in one consistent read. A wait whose Run has already ended is left out:
// stopping it would change nothing, and its own deadline closes it.
func (s *QueryService) ListOpenHumanWaits(ctx context.Context) ([]HumanWaitView, error) {
	var views []HumanWaitView
	err := s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		waits, err := tx.HumanWaits().ListOpen(ctx, maxHumanWaitListItems)
		if err != nil {
			return err
		}
		views = make([]HumanWaitView, 0, len(waits))
		for _, wait := range waits {
			view, live, err := humanWaitView(ctx, tx, wait)
			if err != nil {
				return err
			}
			if live {
				views = append(views, view)
			}
		}
		return nil
	})
	return views, err
}

// humanWaitView builds the view of one wait; live is false when its Run has ended.
func humanWaitView(ctx context.Context, tx store.Tx, wait domain.HumanWait) (view HumanWaitView, live bool, err error) {
	action, err := tx.AgentActions().Get(ctx, wait.ActionID)
	if err != nil {
		return HumanWaitView{}, false, err
	}
	turn, err := tx.AgentTurns().Get(ctx, action.TurnID)
	if err != nil {
		return HumanWaitView{}, false, err
	}
	agentRun, err := tx.AgentRuns().Get(ctx, turn.AgentRunID)
	if err != nil {
		return HumanWaitView{}, false, err
	}
	run, err := tx.Runs().Get(ctx, wait.RunID)
	if err != nil {
		return HumanWaitView{}, false, err
	}
	if run.Status.IsTerminal() {
		return HumanWaitView{}, false, nil
	}
	view = HumanWaitView{
		Wait: wait, WorkflowID: run.WorkflowID, NodeRunID: agentRun.NodeRunID, AgentRunID: agentRun.ID,
		Reason: HumanWaitReasonNotConfirmed,
	}
	occupations, err := tx.ReleaseOccupations().ListByActionID(ctx, action.ID)
	if err != nil {
		return HumanWaitView{}, false, err
	}
	if n := len(occupations); n > 0 {
		view.TargetKey = occupations[n-1].TargetKey
		view.OperationKey = occupations[n-1].OperationKey
	}
	attempts, err := tx.ToolAttempts().ListByActionID(ctx, action.ID)
	if err != nil {
		return HumanWaitView{}, false, err
	}
	if n := len(attempts); n > 0 {
		latest := attempts[n-1]
		view.ToolName = latest.ToolName
		view.ConfirmDeadline = latest.ConfirmDeadline
		if latest.OutcomeUnknownSince != nil {
			view.Reason = HumanWaitReasonOutcomeUnknown
		}
		if view.OperationKey == "" && latest.OperationKey != nil {
			view.OperationKey = *latest.OperationKey
		}
	}
	return view, true, nil
}
