package domain

import "testing"

func TestNodeRunStatus_Transition_AllowsStateMachineEdges(t *testing.T) {
	cases := []struct {
		name string
		from NodeRunStatus
		to   NodeRunStatus
	}{
		{"claim ready work", NodeRunReady, NodeRunRunning},
		{"sync completion", NodeRunRunning, NodeRunSucceeded},
		{"async dispatch", NodeRunRunning, NodeRunWaitingCallback},
		{"retries exhausted", NodeRunRunning, NodeRunFailed},
		{"plain async resume", NodeRunWaitingCallback, NodeRunSucceeded},
		{"agent tool resume", NodeRunWaitingCallback, NodeRunRunning},
		{"waiting timeout", NodeRunWaitingCallback, NodeRunFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.from.CanTransitionTo(tc.to) {
				t.Fatalf("NodeRun %s -> %s: want allowed, got rejected", tc.from, tc.to)
			}
		})
	}
}

func TestNodeRunStatus_Transition_RejectsIllegalEdges(t *testing.T) {
	cases := []struct {
		name string
		from NodeRunStatus
		to   NodeRunStatus
	}{
		{"ready cannot skip execution", NodeRunReady, NodeRunSucceeded},
		{"ready cannot wait", NodeRunReady, NodeRunWaitingCallback},
		{"terminal success is final", NodeRunSucceeded, NodeRunRunning},
		{"terminal failure is final", NodeRunFailed, NodeRunReady},
		{"no rerun after success", NodeRunSucceeded, NodeRunFailed},
		{"running never returns to ready", NodeRunRunning, NodeRunReady},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.from.CanTransitionTo(tc.to) {
				t.Fatalf("NodeRun %s -> %s: want rejected, got allowed", tc.from, tc.to)
			}
		})
	}
}

func TestNodeRunStatus_Transition_UnknownStatusIsRejected(t *testing.T) {
	// SKIPPED and CANCELLED are Phase 2. They must not be reachable through the MVP
	// state machine even if a caller constructs the string.
	for _, phase2 := range []NodeRunStatus{"SKIPPED", "CANCELLED"} {
		if phase2.IsValid() {
			t.Fatalf("NodeRunStatus %q: want invalid in MVP, got valid", phase2)
		}
		if NodeRunRunning.CanTransitionTo(phase2) {
			t.Fatalf("NodeRun RUNNING -> %s: want rejected, got allowed", phase2)
		}
		if phase2.CanTransitionTo(NodeRunFailed) {
			t.Fatalf("NodeRun %s -> FAILED: want rejected, got allowed", phase2)
		}
	}
}

func TestRunStatus_IsValid_ExcludesPhase2AndNodeRunTerminal(t *testing.T) {
	for _, valid := range []RunStatus{RunRunning, RunPaused, RunCompleted, RunFailed} {
		if !valid.IsValid() {
			t.Fatalf("RunStatus %q: want valid, got invalid", valid)
		}
	}
	// A Run has no SUCCEEDED status, and CANCELLED is Phase 2.
	for _, invalid := range []RunStatus{"SUCCEEDED", "CANCELLED", "READY", ""} {
		if invalid.IsValid() {
			t.Fatalf("RunStatus %q: want invalid, got valid", invalid)
		}
	}
}

func TestNodeAttemptStatus_Transition_DispatchedOnlyResolvesToTerminal(t *testing.T) {
	if !NodeAttemptStarted.CanTransitionTo(NodeAttemptDispatched) {
		t.Fatal("NodeAttempt STARTED -> DISPATCHED: want allowed, got rejected")
	}
	if !NodeAttemptDispatched.CanTransitionTo(NodeAttemptSucceeded) {
		t.Fatal("NodeAttempt DISPATCHED -> SUCCEEDED: want allowed, got rejected")
	}
	if NodeAttemptDispatched.CanTransitionTo(NodeAttemptStarted) {
		t.Fatal("NodeAttempt DISPATCHED -> STARTED: want rejected, got allowed")
	}
	if NodeAttemptSucceeded.CanTransitionTo(NodeAttemptFailed) {
		t.Fatal("NodeAttempt SUCCEEDED -> FAILED: want rejected, got allowed")
	}
}

func TestAgentTurnStatus_Transition_ClaimedTurnCannotReturnToReady(t *testing.T) {
	if !AgentTurnReady.CanTransitionTo(AgentTurnRunning) {
		t.Fatal("AgentTurn READY -> RUNNING: want allowed, got rejected")
	}
	if AgentTurnRunning.CanTransitionTo(AgentTurnReady) {
		t.Fatal("AgentTurn RUNNING -> READY: want rejected, got allowed")
	}
	if AgentTurnCompleted.CanTransitionTo(AgentTurnRunning) {
		t.Fatal("AgentTurn COMPLETED -> RUNNING: want rejected, got allowed")
	}
	// A Turn has no waiting state; waiting belongs to the Action.
	if AgentTurnStatus("WAITING_CALLBACK").IsValid() {
		t.Fatal("AgentTurnStatus WAITING_CALLBACK: want invalid, got valid")
	}
}

func TestAgentActionStatus_Transition_WaitingResolvesWithoutRerunningModel(t *testing.T) {
	if !AgentActionReady.CanTransitionTo(AgentActionRunning) {
		t.Fatal("AgentAction READY -> RUNNING: want allowed, got rejected")
	}
	if !AgentActionRunning.CanTransitionTo(AgentActionWaitingCallback) {
		t.Fatal("AgentAction RUNNING -> WAITING_CALLBACK: want allowed, got rejected")
	}
	if !AgentActionWaitingCallback.CanTransitionTo(AgentActionSucceeded) {
		t.Fatal("AgentAction WAITING_CALLBACK -> SUCCEEDED: want allowed, got rejected")
	}
	// A waiting Action resumes; it never goes back to RUNNING to re-decide.
	if AgentActionWaitingCallback.CanTransitionTo(AgentActionRunning) {
		t.Fatal("AgentAction WAITING_CALLBACK -> RUNNING: want rejected, got allowed")
	}
	if AgentActionSucceeded.CanTransitionTo(AgentActionFailed) {
		t.Fatal("AgentAction SUCCEEDED -> FAILED: want rejected, got allowed")
	}
}

func TestEventType_IsValid_ExcludesPhase2Types(t *testing.T) {
	mvp := []EventType{
		EventRunCreated, EventRunPaused, EventRunResumed, EventRunCompleted, EventRunFailed,
		EventNodeReady, EventNodeStarted, EventNodeRetrying, EventNodeDispatched,
		EventNodeCallbackReceived, EventNodeCompleted, EventNodeFailed,
		EventAgentStarted, EventAgentTurnReady, EventAgentTurnStarted,
		EventAgentDecisionCommitted, EventAgentActionStarted, EventAgentActionWaiting,
		EventAgentActionCompleted, EventAgentActionFailed, EventAgentStateUpdated,
		EventAgentCompleted, EventAgentFailed,
	}
	if len(mvp) != 23 {
		t.Fatalf("MVP event type count: want 23, got %d", len(mvp))
	}
	for _, typ := range mvp {
		if !typ.IsValid() {
			t.Fatalf("EventType %q: want valid, got invalid", typ)
		}
	}
	phase2 := []EventType{
		"RUN_CANCELLED", "NODE_SKIPPED", "NODE_CANCELLED",
		"MODEL_STREAM_STARTED", "MODEL_TOKEN_RECEIVED", "MODEL_STREAM_COMPLETED",
		"RUN_STARTED",
	}
	for _, typ := range phase2 {
		if typ.IsValid() {
			t.Fatalf("EventType %q: want invalid in MVP, got valid", typ)
		}
	}
}

func TestAgentTermination_IsValid_OnlyFinalResponseIsSuccess(t *testing.T) {
	all := []AgentTermination{
		TerminationFinalResponse, TerminationMaxTurns, TerminationTimeout,
		TerminationModelError, TerminationToolError, TerminationInvalidAction,
	}
	for _, term := range all {
		if !term.IsValid() {
			t.Fatalf("AgentTermination %q: want valid, got invalid", term)
		}
	}
	if AgentTermination("CANCELLED").IsValid() {
		t.Fatal("AgentTermination CANCELLED: want invalid, got valid")
	}
}
