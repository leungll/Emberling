package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the start of a release: the checks a release call passes before it is
// proposed and again when it is approved, the occupation of its deploy target, and the
// creation of a release Attempt with the facts the reconciliation later relies on. A
// release Tool is reconcilable: its outcome is polled under an operation key the Runtime
// chose, it is never called back, and only production's definite answer settles it.

// releaseCall is what the Tool call of a release Attempt carries beside the generic call:
// the operation key production deduplicates by, the occupation the Action holds and the
// frozen target the release goes to. It is rebuilt from committed facts by the poll path.
type releaseCall struct {
	operationKey string
	occupationID string
	target       *domain.WorkspaceDeployTarget
}

// releaseBounds are the release waits the Agent node bounds in the Run's immutable
// Definition version. Each is nil when the node sets none; the Runtime never substitutes
// a default, so a missing bound disables the step that needs it.
type releaseBounds struct {
	confirmTimeout   *time.Duration
	voidGrace        *time.Duration
	maxRetries       *int
	humanWaitTimeout *time.Duration
}

func releaseBoundsOf(plan *runtime.CompiledDefinition, nodeID string) (releaseBounds, error) {
	if plan == nil {
		return releaseBounds{}, errors.New("execution: read release bounds: the caller must resolve the run and its plan before the lock")
	}
	for _, node := range plan.Definition.Nodes {
		if node.ID != nodeID {
			continue
		}
		cfg, err := runtime.ParseAgentNodeConfig(node.Config)
		if err != nil {
			return releaseBounds{}, fmt.Errorf("execution: read release bounds of node %q: %w", nodeID, err)
		}
		var bounds releaseBounds
		if cfg.ReleaseConfirmTimeoutMs != nil {
			d := time.Duration(*cfg.ReleaseConfirmTimeoutMs) * time.Millisecond
			bounds.confirmTimeout = &d
		}
		if cfg.VoidGraceMs != nil {
			d := time.Duration(*cfg.VoidGraceMs) * time.Millisecond
			bounds.voidGrace = &d
		}
		if cfg.HumanWaitTimeoutMs != nil {
			d := time.Duration(*cfg.HumanWaitTimeoutMs) * time.Millisecond
			bounds.humanWaitTimeout = &d
		}
		bounds.maxRetries = cfg.MaxReleaseRetries
		return bounds, nil
	}
	return releaseBounds{}, fmt.Errorf("execution: read release bounds: node %q is not in the bound definition", nodeID)
}

// releaseArguments is the one argument a release call carries. The target, the base commit
// and the patch digest are never the model's to choose: they come from the frozen workspace
// and the named change request.
type releaseArguments struct {
	ChangeRequestID string `json:"changeRequestId"`
}

// codeReleaseTargetNotConfigured fails a release call of an Agent Run whose frozen
// workspace names no deploy target. It is a Definition defect, decided before any Attempt.
const codeReleaseTargetNotConfigured = "RELEASE_TARGET_NOT_CONFIGURED"

// checkReleaseActionLocked runs the release-specific pre-execution checks after the
// generic workspace checks passed, under the Run aggregate lock: the named change request
// must belong to this Agent Run and describe the workspace's newest patch, and the frozen
// deploy target must be free. Every refusal is handed back to the Agent before any Attempt,
// approval or occupation exists; a target held by another Action is read, never claimed,
// here. The same checks run again in the approval transaction, because the world can move
// while a person decides.
func checkReleaseActionLocked(ctx context.Context, tx store.Tx, c *agentActionClaim, view *registry.WorkspaceView) (toolActionVerdict, error) {
	if view.DeployTarget == nil {
		return failVerdict(domain.TerminationToolError, domain.ExecutionError{
			Code:    codeReleaseTargetNotConfigured,
			Message: fmt.Sprintf("tool %q releases to the workspace's deploy target, but the Agent node froze none", c.toolName),
		}), nil
	}
	var args releaseArguments
	if err := json.Unmarshal(c.decision.Arguments, &args); err != nil || args.ChangeRequestID == "" {
		return failVerdict(domain.TerminationInvalidAction, domain.ExecutionError{
			Code: "INVALID_ACTION", Message: fmt.Sprintf("tool %q needs a changeRequestId argument", c.toolName),
		}), nil
	}
	request, err := tx.ChangeRequests().Get(ctx, args.ChangeRequestID)
	switch {
	case errors.Is(err, domain.ErrNotFound), err == nil && request.AgentRunID != c.agentRun.ID:
		// A request of another Agent Run is treated as absent: the call names nothing this
		// Agent Run may release, and no other request is substituted.
		return failVerdict(domain.TerminationInvalidAction, domain.ExecutionError{
			Code: "INVALID_ACTION", Message: fmt.Sprintf("tool %q names a change request that does not belong to this Agent Run", c.toolName),
		}), nil
	case err != nil:
		return toolActionVerdict{}, err
	}
	if request.PatchDigest != string(view.PatchDigest) {
		return returnVerdict(domain.ReturnStalePatch, fmt.Sprintf(
			"The change request describes patch %s, but the workspace's newest patch is %s; describe the current patch before releasing it.",
			boundedDigest(request.PatchDigest), boundedDigest(string(view.PatchDigest)))), nil
	}
	targetKey := domain.ReleaseTargetKey(*view.DeployTarget)
	_, err = tx.ReleaseOccupations().GetActiveByTarget(ctx, targetKey)
	switch {
	case err == nil:
		return returnVerdict(domain.ReturnTargetOccupied, targetOccupiedMessage(*view.DeployTarget)), nil
	case errors.Is(err, domain.ErrNotFound):
		return toolActionVerdict{}, nil
	default:
		return toolActionVerdict{}, err
	}
}

func targetOccupiedMessage(target domain.WorkspaceDeployTarget) string {
	return fmt.Sprintf("Another release currently occupies %s/%s; it must settle before this target accepts a new release.",
		boundedToolName(target.Service), boundedToolName(target.Environment))
}

// boundedDigest keeps a digest short enough for a tool message; a digest carries no secret.
func boundedDigest(digest string) string {
	if digest == "" {
		return "(none)"
	}
	if len(digest) > 24 {
		return digest[:24] + "..."
	}
	return digest
}

// startReleaseAttemptLocked creates the next release Attempt of the Action c holds, inside
// the transaction whose Run aggregate lock the caller holds: it occupies the frozen deploy
// target when the Tool declares that it does (an INSERT the partial unique index may refuse,
// in which case nothing else is written and occupied is true), pauses the Agent's clock for
// the release, creates the Attempt with its operation key, confirm deadline and this
// process's runner identity, creates the Callback Binding that routes polls to it under the
// release provider and the operation key, writes the key onto the occupation and appends
// AGENT_ACTION_STARTED with the key and the occupation. No callback credential is issued:
// production never calls back, and the operation key is not a secret.
//
// It serves the first Attempt and every re-release after a confirmed void alike: the
// Attempt number continues from the Attempts the Action already has, and an occupation the
// Action already holds is kept and re-keyed instead of acquired again.
func (s *ExecutionService) startReleaseAttemptLocked(ctx context.Context, tx store.Tx, c *agentActionClaim, inputs toolActionInputs, now time.Time) (call *agentToolCall, occupied bool, err error) {
	if inputs.workspace == nil || inputs.workspace.DeployTarget == nil {
		return nil, false, fmt.Errorf("execution: start release attempt of action %s: the release has no frozen deploy target", c.action.ID)
	}
	target := *inputs.workspace.DeployTarget
	bounds, err := releaseBoundsOf(c.plan, c.nodeRun.NodeID)
	if err != nil {
		return nil, false, err
	}
	previous, err := tx.ToolAttempts().ListByActionID(ctx, c.action.ID)
	if err != nil {
		return nil, false, err
	}
	attemptNo := len(previous) + 1
	operationKey := domain.ReleaseOperationKey(c.action.ID, attemptNo)

	var occupationID string
	if c.reg.Metadata.OccupiesReleaseTarget {
		if c.occupation == nil {
			occupation := domain.ReleaseOccupation{
				ID:           s.deps.IDs.NewID(domain.IDPrefixReleaseOccupation),
				TargetKey:    domain.ReleaseTargetKey(target),
				RunID:        c.nodeRun.RunID,
				ActionID:     c.action.ID,
				OperationKey: operationKey,
				Status:       domain.ReleaseOccupationActive,
				AcquiredAt:   now,
			}
			acquired, err := tx.ReleaseOccupations().Acquire(ctx, occupation)
			if err != nil {
				return nil, false, err
			}
			if !acquired {
				return nil, true, nil
			}
			c.occupation = &occupation
		} else if err := tx.ReleaseOccupations().SetOperationKey(ctx, c.occupation.ID, operationKey); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				return nil, false, errAgentTurnSuperseded
			}
			return nil, false, err
		}
		occupationID = c.occupation.ID
	}

	// The Agent waits on production from here on, so its clock stops; a re-release finds
	// it already paused for the release and leaves the pause as it is.
	if c.agentRun.PausedAt == nil {
		if c.agentRun, err = s.pauseAgentLocked(ctx, tx, c.lock, c.nodeRun, c.agentRun, domain.PauseRelease, now); err != nil {
			return nil, false, err
		}
	}

	attemptID := s.deps.IDs.NewID(domain.IDPrefixToolAttempt)
	runnerID := s.deps.RunnerID
	var confirmDeadline *time.Time
	if bounds.confirmTimeout != nil {
		deadline := now.Add(*bounds.confirmTimeout)
		confirmDeadline = &deadline
	}
	if err := tx.ToolAttempts().Create(ctx, domain.ToolAttempt{
		ID:              attemptID,
		ActionID:        c.action.ID,
		AttemptNo:       attemptNo,
		ToolName:        c.toolName,
		Status:          domain.ToolAttemptStarted,
		Input:           c.decision.Arguments,
		RunnerID:        &runnerID,
		StartedAt:       now,
		OperationKey:    &operationKey,
		ConfirmDeadline: confirmDeadline,
	}); err != nil {
		return nil, false, err
	}
	// UNIQUE (provider_id, external_task_id) makes the database the authority that one
	// operation key routes to exactly one Attempt.
	if err := tx.CallbackBindings().Create(ctx, domain.CallbackBinding{
		ID:             s.deps.IDs.NewID(domain.IDPrefixCallbackBinding),
		ProviderID:     domain.ReleaseProviderID,
		ExternalTaskID: operationKey,
		TargetType:     domain.CallbackTargetToolAttempt,
		TargetID:       attemptID,
		CreatedAt:      now,
	}); err != nil {
		return nil, false, err
	}
	payload := agentActionStartedPayload{
		AgentRunID:    c.agentRun.ID,
		TurnID:        c.turn.ID,
		ActionID:      c.action.ID,
		ToolAttemptID: &attemptID,
		ClaimSource:   c.claimSource,
		OperationKey:  &operationKey,
	}
	if occupationID != "" {
		payload.OccupationID = &occupationID
	}
	if err := s.appendEvent(ctx, tx, c.lock, c.nodeRun.RunID, &c.nodeRun.ID, domain.EventAgentActionStarted, now, payload); err != nil {
		return nil, false, err
	}
	if err := tx.Runs().UpdateAggregate(ctx, c.lock, c.lock.Status(), now); err != nil {
		return nil, false, err
	}

	c.commit = agentCommit{runID: c.nodeRun.RunID, lastSeq: c.lock.LastSeq()}
	// The release wait does not count against the Agent's time limit, and while the Agent
	// is paused its stored deadline is frozen and may already lie in the past; the call is
	// bounded by this Attempt's own confirm deadline instead, an absolute instant written
	// with it. Definition validation makes the confirm bound mandatory for a Tool that
	// occupies a release target, so the Agent deadline remains only as a defensive bound.
	callDeadline := c.agentRun.Deadline
	if confirmDeadline != nil {
		callDeadline = *confirmDeadline
	}
	return &agentToolCall{
		runID:        c.nodeRun.RunID,
		nodeRunID:    c.nodeRun.ID,
		agentRunID:   c.agentRun.ID,
		turnID:       c.turn.ID,
		actionID:     c.action.ID,
		attemptID:    attemptID,
		toolName:     c.toolName,
		executor:     c.reg.Executor,
		outputSchema: c.reg.Metadata.OutputSchema,
		records:      c.reg.Metadata.Records,
		action: registry.ToolAction{
			RunID:      c.nodeRun.RunID,
			AgentRunID: c.agentRun.ID,
			TurnID:     c.turn.ID,
			ActionID:   c.action.ID,
			ToolName:   c.toolName,
			AttemptNo:  attemptNo,
			Arguments:  c.decision.Arguments,
			Workspace:  inputs.workspace,
		},
		async:    true,
		deadline: callDeadline,
		release:  &releaseCall{operationKey: operationKey, occupationID: occupationID, target: &target},
	}, false, nil
}

// activeOccupationOfAction returns the ACTIVE occupation the Action holds, or nil when it
// holds none (the Tool occupies no target, or the occupation was already released).
func activeOccupationOfAction(ctx context.Context, tx store.Tx, actionID string) (*domain.ReleaseOccupation, error) {
	occupations, err := tx.ReleaseOccupations().ListByActionID(ctx, actionID)
	if err != nil {
		return nil, err
	}
	for i := range occupations {
		if occupations[i].Status == domain.ReleaseOccupationActive {
			return &occupations[i], nil
		}
	}
	return nil, nil
}

// releaseOccupationLocked releases the ACTIVE occupation of the Action with the given
// reason, in the caller's transaction. An Action without one changes nothing.
func releaseOccupationLocked(ctx context.Context, tx store.Tx, actionID string, reason domain.ReleaseReason, now time.Time) error {
	occupation, err := activeOccupationOfAction(ctx, tx, actionID)
	if err != nil || occupation == nil {
		return err
	}
	if err := tx.ReleaseOccupations().Release(ctx, occupation.ID, reason, now); err != nil {
		if errors.Is(err, domain.ErrStaleClaim) {
			return errAgentTurnSuperseded
		}
		return err
	}
	return nil
}
