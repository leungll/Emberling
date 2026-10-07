package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the Agent Loop's use cases: the transaction that expands a claimed
// MANAGED_AGENT NodeRun into a durable Agent Run, and
// the transaction pair that turns one READY Turn into one committed Decision. Everything
// here obeys the same two rules as the Node path: a state transition and its Event commit
// together, and the Provider is called only after the prerequisite
// transaction has committed.

// agentTurnRequest is the bounded, immutable description of the model call a READY Turn
// grants the right to make (AgentTurn.Request). It names the
// frozen inputs by reference -- the Model ID and the Context/State Versions the Turn reads
// -- instead of copying the messages into the Turn row.
//
// Design decision (the data and execution models fix the field's existence and
// immutability but not its content): a copy of the messages would be a second, divergent
// transcript of an immutable chain that already has one authority, and would put user
// document text into a row that Trace projects. Naming the versions keeps the Turn
// recoverable (the Context and State a retry would read are exactly the ones named here)
// and bounded. Provider credentials, headers, the model's ConfigSchema and any
// Provider-internal structure are out of scope for this field by construction.
type agentTurnRequest struct {
	ModelID        string   `json:"modelId"`
	ContextVersion int      `json:"contextVersion"`
	StateVersion   int      `json:"stateVersion"`
	AllowedTools   []string `json:"allowedTools"`
	MessageCount   int      `json:"messageCount"`
}

// agentStartedPayload is AGENT_STARTED (Event fields: agentRunId, modelId,
// maxTurns, deadline).
type agentStartedPayload struct {
	AgentRunID string    `json:"agentRunId"`
	ModelID    string    `json:"modelId"`
	MaxTurns   int       `json:"maxTurns"`
	Deadline   time.Time `json:"deadline"`
}

// agentTurnPayload is AGENT_TURN_READY and AGENT_TURN_STARTED. ClaimSource is present
// only on AGENT_TURN_STARTED: a READY Turn has no claimant
// yet.
type agentTurnPayload struct {
	AgentRunID     string              `json:"agentRunId"`
	TurnID         string              `json:"turnId"`
	TurnNo         int                 `json:"turnNo"`
	ContextVersion int                 `json:"contextVersion"`
	StateVersion   int                 `json:"stateVersion"`
	ClaimSource    *domain.ClaimSource `json:"claimSource,omitempty"`
}

// frozenJSON normalises one optional frozen Agent config field for persistence. The
// agent_runs configuration columns are NOT NULL (migrations/00001_initial.sql), so "the
// Definition froze no value here" still has to be a JSON value: it is the literal `null`,
// uniformly, for modelConfig and for each of the three Schemas.
//
// `{}` is deliberately not used: for a Schema column it would mean "accepts anything",
// which is a contract the Definition never stated, and it would make an absent
// modelConfig indistinguishable from one a Definition explicitly froze as empty.
func frozenJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`null`)
	}
	return raw
}

// frozenValue is the inverse of frozenJSON: it reports the frozen value, or nil when the
// column holds the `null` placeholder, so a caller building a model request never passes
// `null` on as if it were a Schema or a parameter object.
func frozenValue(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	return raw
}

// -----------------------------------------------------------------------------------
// Agent Run initialisation
// -----------------------------------------------------------------------------------

// startAgentRun is the MANAGED_AGENT half of startAttempt: the caller has already won the
// NodeRun's READY->RUNNING claim, holds the Run aggregate lock and has recorded the
// NodeRun's resolved input. In this same transaction it creates the Agent Run frozen from
// the Definition, Context and State Version 0, Turn 1 READY, and the Events
// AGENT_STARTED and AGENT_TURN_READY.
//
// No Node Attempt is created. An Agent NodeRun's model calls are recorded as Agent Turns;
// an Attempt would be a second, empty record of the same execution and would give the
// retry and timeout machinery a handle on a NodeRun whose rounds it does not own.
//
// Before anything is created, every stable Tool Name in the frozen allowlist and the
// frozen Model ID must resolve in the current Registry: silently dropping a missing
// entry and starting a shortened Agent is forbidden. A failure to
// resolve fails the Agent NodeRun in this same transaction and creates no Agent Run.
func (s *ExecutionService) startAgentRun(
	ctx context.Context,
	tx store.Tx,
	lock *store.RunLock,
	run domain.Run,
	nr domain.NodeRun,
	node domain.Node,
	ports map[string]json.RawMessage,
	inputBytes json.RawMessage,
	config map[string]any,
	now time.Time,
	out *AdvanceOutcome,
) error {
	cfg, err := runtime.ParseAgentNodeConfig(node.Config)
	if err != nil {
		// The frozen Definition passed this same parse at Save time, so this is registry
		// drift's cousin: a stored config the current build can no longer read. Fail the
		// NodeRun explicitly rather than starting an Agent from a half-parsed config.
		return s.failAgentNodeRun(ctx, tx, lock, run, nr, nil,
			domain.ExecutionError{Code: "AGENT_CONFIG_INVALID", Message: err.Error()}, now)
	}

	if _, _, ok := s.deps.Models.Get(cfg.ModelID); !ok {
		return s.failAgentNodeRun(ctx, tx, lock, run, nr, nil, domain.ExecutionError{
			Code:    "MODEL_NOT_REGISTERED",
			Message: fmt.Sprintf("model %q is not registered", cfg.ModelID),
		}, now)
	}
	for _, toolName := range cfg.AllowedTools {
		if _, ok := s.deps.Tools.Get(toolName); !ok {
			return s.failAgentNodeRun(ctx, tx, lock, run, nr, nil, domain.ExecutionError{
				Code:    "TOOL_NOT_REGISTERED",
				Message: fmt.Sprintf("tool %q of the Agent's frozen allowlist is not registered", toolName),
			}, now)
		}
	}

	inputPort, err := s.agentInputPort(nr.NodeType)
	if err != nil {
		return s.failAgentNodeRun(ctx, tx, lock, run, nr, nil,
			domain.ExecutionError{Code: "NODE_TYPE_NOT_REGISTERED", Message: err.Error()}, now)
	}
	messages, err := runtime.InitialAgentContext(ports[inputPort])
	if err != nil {
		return s.failAgentNodeRun(ctx, tx, lock, run, nr, nil,
			domain.ExecutionError{Code: "AGENT_INPUT_INVALID", Message: err.Error()}, now)
	}
	messageBytes, err := json.Marshal(messages)
	if err != nil {
		return fmt.Errorf("execution: encode agent context version 0 for node %q: %w", nr.NodeID, err)
	}

	agentRunID := s.deps.IDs.NewID(domain.IDPrefixAgentRun)
	deadline := now.Add(time.Duration(cfg.TimeoutMs) * time.Millisecond)
	if err := tx.AgentRuns().Create(ctx, domain.AgentRun{
		ID:            agentRunID,
		NodeRunID:     nr.ID,
		Instructions:  cfg.Instructions,
		ModelID:       cfg.ModelID,
		ModelConfig:   frozenJSON(cfg.ModelConfig),
		AllowedTools:  cfg.AllowedTools,
		ContextSchema: frozenJSON(cfg.ContextSchema),
		StateSchema:   frozenJSON(cfg.StateSchema),
		OutputSchema:  frozenJSON(cfg.OutputSchema),
		MaxTurns:      cfg.MaxTurns,
		// The Agent Run starts pointing at the facts this same transaction creates: Turn
		// 1, Context Version 0 and State Version 0.
		CurrentTurnNo:         1,
		CurrentContextVersion: 0,
		CurrentStateVersion:   0,
		StartedAt:             now,
		Deadline:              deadline,
	}); err != nil {
		return err
	}

	if err := tx.AgentContextVersions().Create(ctx, domain.AgentContextVersion{
		ID:         s.deps.IDs.NewID(domain.IDPrefixAgentContextVersion),
		AgentRunID: agentRunID,
		Version:    0,
		Messages:   messageBytes,
		CreatedAt:  now,
	}); err != nil {
		return err
	}
	if err := tx.AgentStateVersions().Create(ctx, domain.AgentStateVersion{
		ID:         s.deps.IDs.NewID(domain.IDPrefixAgentStateVersion),
		AgentRunID: agentRunID,
		Version:    0,
		Value:      json.RawMessage(`{}`),
		CreatedAt:  now,
	}); err != nil {
		return err
	}

	turnID := s.deps.IDs.NewID(domain.IDPrefixAgentTurn)
	requestBytes, err := json.Marshal(agentTurnRequest{
		ModelID:        cfg.ModelID,
		ContextVersion: 0,
		StateVersion:   0,
		AllowedTools:   cfg.AllowedTools,
		MessageCount:   len(messages),
	})
	if err != nil {
		return fmt.Errorf("execution: encode agent turn request for node %q: %w", nr.NodeID, err)
	}
	if err := tx.AgentTurns().Create(ctx, domain.AgentTurn{
		ID:         turnID,
		AgentRunID: agentRunID,
		TurnNo:     1,
		Status:     domain.AgentTurnReady,
		Request:    requestBytes,
	}); err != nil {
		return err
	}

	// NODE_STARTED's Agent variant names the Agent Run
	// instead of an Attempt number, because this NodeRun has no Attempt.
	if err := s.appendEvent(ctx, tx, lock, run.ID, &nr.ID, domain.EventNodeStarted, now, nodeStartedPayload{
		AgentRunID: &agentRunID,
		Input:      summarize(inputBytes),
	}); err != nil {
		return err
	}
	if err := s.appendEvent(ctx, tx, lock, run.ID, &nr.ID, domain.EventAgentStarted, now, agentStartedPayload{
		AgentRunID: agentRunID,
		ModelID:    cfg.ModelID,
		MaxTurns:   cfg.MaxTurns,
		Deadline:   deadline,
	}); err != nil {
		return err
	}
	if err := s.appendEvent(ctx, tx, lock, run.ID, &nr.ID, domain.EventAgentTurnReady, now, agentTurnPayload{
		AgentRunID: agentRunID, TurnID: turnID, TurnNo: 1, ContextVersion: 0, StateVersion: 0,
	}); err != nil {
		return err
	}

	if err := tx.Runs().UpdateAggregate(ctx, lock, domain.RunRunning, now); err != nil {
		return err
	}

	*out = AdvanceOutcome{
		Claimed:     true,
		RunID:       run.ID,
		NodeRunID:   nr.ID,
		NodeType:    nr.NodeType,
		AgentRunID:  agentRunID,
		AgentTurnID: turnID,
		Input: registry.NodeInput{
			RunID:     run.ID,
			NodeRunID: nr.ID,
			Ports:     ports,
			RunInput:  run.Input,
		},
		Config: config,
	}
	return nil
}

// agentInputPort reports the single required input port of a MANAGED_AGENT Node Type,
// read from its registered Metadata: which port carries the Agent's question is the Node
// Type's registered contract, not a constant this package may restate
// (CLAUDE.md "Extensions and external calls").
func (s *ExecutionService) agentInputPort(nodeType string) (string, error) {
	reg, ok := s.deps.Nodes.Get(nodeType)
	if !ok {
		return "", fmt.Errorf("node type %q is not registered", nodeType)
	}
	for _, port := range reg.Metadata.Inputs {
		if port.Required {
			return port.Name, nil
		}
	}
	return "", fmt.Errorf("node type %q declares no required input port", nodeType)
}

// failAgentNodeRun fails an Agent NodeRun inside a transaction whose Run aggregate lock
// the caller already holds. It is the shared tail of every Agent failure: the
// initialisation checks above, and the model-result transaction below.
//
// It is deliberately not ExecutionService.failNode: that one is keyed on a Node Attempt
// (it loads the Attempt to find the NodeRun and to fill nodeFailedPayload.AttemptNo) and
// applies runtime.DecideRetry, and an Agent NodeRun has neither. An Agent NodeRun failure
// is terminal: the retry unit inside an Agent Run is the Turn or the Tool Attempt, and
// the Agent termination table maps every non-FINAL_RESPONSE termination
// straight to a FAILED Agent NodeRun.
//
// agentRunID is nil when no Agent Run exists yet, which is exactly what makes the
// initialisation failure distinguishable in Trace from a failure inside a running Agent.
func (s *ExecutionService) failAgentNodeRun(
	ctx context.Context,
	tx store.Tx,
	lock *store.RunLock,
	run domain.Run,
	nr domain.NodeRun,
	agentRunID *string,
	execError domain.ExecutionError,
	now time.Time,
) error {
	// An Agent NodeRun waiting on an ASYNC Tool's callback is still inside its Agent Loop,
	// so the Agent timeout fails it from WAITING_CALLBACK; every other Agent
	// failure fails it from RUNNING. The conditional update still decides the winner.
	from := domain.NodeRunRunning
	if nr.Status == domain.NodeRunWaitingCallback {
		from = domain.NodeRunWaitingCallback
	}
	if err := tx.NodeRuns().MarkFailed(ctx, nr.ID, from, now, execError); err != nil {
		return err
	}
	if err := s.appendEvent(ctx, tx, lock, run.ID, &nr.ID, domain.EventNodeFailed, now, nodeFailedPayload{
		AgentRunID: agentRunID, Error: execError,
	}); err != nil {
		return err
	}
	if err := tx.Runs().SetError(ctx, lock, execError); err != nil {
		return err
	}

	allNodeRuns, err := tx.NodeRuns().ListByRun(ctx, run.ID)
	if err != nil {
		return err
	}
	nodeStatuses := make(map[string]domain.NodeRunStatus, len(allNodeRuns))
	for _, other := range allNodeRuns {
		nodeStatuses[other.NodeID] = other.Status
	}
	// AllNodeIDs and OutputNodeID are deliberately left zero, for the same reason
	// failNode's registry-drift branch may leave them zero: nodeStatuses already records
	// the NodeRun this call just marked FAILED, and AggregateRunStatus's hasFailed
	// short-circuit yields RunFailed before it consults either field.
	newStatus := runtime.AggregateRunStatus(runtime.RunAggregateInput{
		NodeStatuses:   nodeStatuses,
		OutputProduced: len(run.Output) > 0,
	})
	if ev, changed := runtime.NextRunTransitionEvent(lock.Status(), newStatus); changed {
		errCopy := execError
		if err := s.appendEvent(ctx, tx, lock, run.ID, nil, ev.Type, now, runTransitionPayload{
			From: lock.Status(), To: newStatus, Error: &errCopy,
		}); err != nil {
			return err
		}
	}
	return tx.Runs().UpdateAggregate(ctx, lock, newStatus, now)
}
