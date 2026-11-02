package service

import (
	"context"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// agentRequestSources is where the content of one model request came from: the persisted
// messages the request was mapped from and the State Version it carries. The claim
// transaction keeps them beside the request so the Context Manifest can be derived from
// the same committed facts, without a second read after COMMIT.
type agentRequestSources struct {
	messages     []runtime.AgentContextMessage
	stateVersion domain.AgentStateVersion
}

// writeTurnManifest records, inside the transaction that won the Turn claim, what the
// model request about to be issued carries and what it leaves out. It derives the items
// from the request content and the message sources only; the one storage read it adds is
// the committed Tool Attempt behind each tool message, which is the reference a tool
// result is recorded by.
//
// The Manifest is written once: the conditional READY->RUNNING claim admits a single
// transaction here, and the table's PRIMARY KEY on the Turn rejects any second row.
func (s *ExecutionService) writeTurnManifest(
	ctx context.Context,
	tx store.Tx,
	agentRun domain.AgentRun,
	nodeRunID, turnID string,
	request registry.ModelRequest,
	sources agentRequestSources,
	now time.Time,
) error {
	messages := make([]runtime.ManifestMessage, 0, len(sources.messages))
	for _, message := range sources.messages {
		entry := runtime.ManifestMessage{
			Role:         message.Role,
			Content:      message.Content,
			ToolName:     message.ToolName,
			ToolActionID: message.ToolActionID,
		}
		if message.Role == runtime.AgentRoleTool {
			if message.ToolName != nil {
				if reg, ok := s.deps.Tools.Get(*message.ToolName); ok {
					entry.ToolUsesWorkspace = reg.Metadata.UsesWorkspace
				}
			}
			if message.ToolActionID != nil {
				attempt, found, err := resultAttemptOfAction(ctx, tx, *message.ToolActionID)
				if err != nil {
					return err
				}
				if found {
					entry.AttemptID = attempt.ID
					entry.AttemptNo = attempt.AttemptNo
				}
			}
		}
		messages = append(messages, entry)
	}

	items := runtime.DeriveTurnManifest(runtime.ManifestInput{
		AgentRunID:     agentRun.ID,
		NodeRunID:      nodeRunID,
		Instructions:   request.Instructions,
		Messages:       messages,
		StateVersionID: sources.stateVersion.ID,
		StateVersion:   sources.stateVersion.Version,
		State:          request.State,
	})
	return tx.AgentTurnManifests().Create(ctx, domain.AgentTurnManifest{
		TurnID:     turnID,
		AgentRunID: agentRun.ID,
		Items:      items,
		CreatedAt:  now,
	})
}

// resultAttemptOfAction resolves the Tool Attempt whose outcome a tool message of the
// given Action carries: the SUCCEEDED Attempt when there is one, otherwise the latest
// Attempt (a refusal returned to the Agent after a failed call). An Action whose refusal
// was decided before any Attempt existed has none, and reports found=false.
func resultAttemptOfAction(ctx context.Context, tx store.Tx, actionID string) (domain.ToolAttempt, bool, error) {
	attempts, err := tx.ToolAttempts().ListByActionID(ctx, actionID)
	if err != nil {
		return domain.ToolAttempt{}, false, err
	}
	if len(attempts) == 0 {
		return domain.ToolAttempt{}, false, nil
	}
	latest := attempts[0]
	for _, attempt := range attempts {
		if attempt.Status == domain.ToolAttemptSucceeded {
			return attempt, true, nil
		}
		if attempt.AttemptNo > latest.AttemptNo {
			latest = attempt
		}
	}
	return latest, true, nil
}
