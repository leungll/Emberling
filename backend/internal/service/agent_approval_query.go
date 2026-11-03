package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// ApprovalView is one approval with what a person needs to decide it: the Tool the Action
// calls, the committed arguments the approval covers, and where the Action lives. It is a
// read-only projection; the approval row stays the authority.
type ApprovalView struct {
	Approval   domain.Approval
	ToolName   string
	Arguments  json.RawMessage
	WorkflowID string
	NodeRunID  string
}

// maxApprovalListItems bounds the approval list read.
const maxApprovalListItems = 200

// ListApprovals returns the approvals in one status, oldest request first, each with its
// Tool and arguments, in a single consistent read.
func (s *QueryService) ListApprovals(ctx context.Context, status domain.ApprovalStatus) ([]ApprovalView, error) {
	if !status.IsValid() {
		return nil, &RunListQueryError{Message: fmt.Sprintf("status must be one of PENDING, APPROVED, REJECTED or EXPIRED, got %q", status)}
	}
	var views []ApprovalView
	err := s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		approvals, err := tx.Approvals().ListByStatus(ctx, status, maxApprovalListItems)
		if err != nil {
			return err
		}
		views = make([]ApprovalView, 0, len(approvals))
		for _, approval := range approvals {
			view, err := approvalView(ctx, tx, approval)
			if err != nil {
				return err
			}
			views = append(views, view)
		}
		return nil
	})
	return views, err
}

// GetApproval returns one approval with its Tool and arguments, or domain.ErrNotFound.
func (s *QueryService) GetApproval(ctx context.Context, approvalID string) (ApprovalView, error) {
	var view ApprovalView
	err := s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		approval, err := tx.Approvals().Get(ctx, approvalID)
		if err != nil {
			return err
		}
		view, err = approvalView(ctx, tx, approval)
		return err
	})
	return view, err
}

func approvalView(ctx context.Context, tx store.Tx, approval domain.Approval) (ApprovalView, error) {
	action, err := tx.AgentActions().Get(ctx, approval.ActionID)
	if err != nil {
		return ApprovalView{}, err
	}
	decision, err := tx.AgentDecisions().Get(ctx, action.DecisionID)
	if err != nil {
		return ApprovalView{}, err
	}
	run, err := tx.Runs().Get(ctx, approval.RunID)
	if err != nil {
		return ApprovalView{}, err
	}
	agentRun, err := tx.AgentRuns().Get(ctx, approval.AgentRunID)
	if err != nil {
		return ApprovalView{}, err
	}
	view := ApprovalView{Approval: approval, Arguments: decision.Arguments, WorkflowID: run.WorkflowID, NodeRunID: agentRun.NodeRunID}
	if decision.ToolName != nil {
		view.ToolName = *decision.ToolName
	}
	return view, nil
}
