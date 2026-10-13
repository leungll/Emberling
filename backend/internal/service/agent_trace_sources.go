package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// AgentActionSources records how an Agent Action was claimed and how it ended, as its
// committed Events report it. A field is nil until the corresponding Event exists.
type AgentActionSources struct {
	Claim      *domain.ClaimSource
	Completion *domain.CompletionSource
	Failure    *domain.FailureSource
}

// agentTraceSourceEvents are the Events whose payload names a claim, completion or
// failure source. Each Turn and each Action has at most one of each, because the
// conditional update that writes it has a single winner.
var agentTraceSourceEvents = []domain.EventType{
	domain.EventAgentTurnStarted,
	domain.EventAgentActionStarted,
	domain.EventAgentActionCompleted,
	domain.EventAgentActionFailed,
}

// agentTraceSourcePayload is the subset of the four payloads the projection reads.
type agentTraceSourcePayload struct {
	TurnID           string                   `json:"turnId"`
	ActionID         string                   `json:"actionId"`
	ClaimSource      *domain.ClaimSource      `json:"claimSource"`
	CompletionSource *domain.CompletionSource `json:"completionSource"`
	FailureSource    *domain.FailureSource    `json:"failureSource"`
}

// agentTraceSources reads the claim and outcome sources of an Agent NodeRun's Turns and
// Actions from its committed Events; nothing is stored a second time. The first Event by
// seq wins, so a later Event can never relabel a Turn or Action. turnCount bounds the
// read: every Turn contributes at most one Event of each source type.
func agentTraceSources(ctx context.Context, tx store.Tx, runID, nodeRunID string, turnCount int) (map[string]domain.ClaimSource, map[string]AgentActionSources, error) {
	turnClaims := map[string]domain.ClaimSource{}
	actions := map[string]AgentActionSources{}
	if turnCount == 0 {
		return turnClaims, actions, nil
	}
	events, err := tx.Events().ListForNodeRun(ctx, runID, nodeRunID, agentTraceSourceEvents, len(agentTraceSourceEvents)*turnCount)
	if err != nil {
		return nil, nil, err
	}
	for _, ev := range events {
		var p agentTraceSourcePayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return nil, nil, fmt.Errorf("decode %s event %s payload: %w", ev.Type, ev.ID, err)
		}
		src := actions[p.ActionID]
		switch ev.Type {
		case domain.EventAgentTurnStarted:
			if _, seen := turnClaims[p.TurnID]; !seen && p.ClaimSource != nil {
				turnClaims[p.TurnID] = *p.ClaimSource
			}
			continue
		case domain.EventAgentActionStarted:
			if src.Claim == nil {
				src.Claim = p.ClaimSource
			}
		case domain.EventAgentActionCompleted:
			if src.Completion == nil {
				src.Completion = p.CompletionSource
			}
		case domain.EventAgentActionFailed:
			if src.Failure == nil {
				src.Failure = p.FailureSource
			}
		default:
			// ListForNodeRun was asked for the four types above only.
			continue
		}
		actions[p.ActionID] = src
	}
	return turnClaims, actions, nil
}
