package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

// createRunRequest is the wire shape of POST /runs, matching Studio's CreateRunRequest.
type createRunRequest struct {
	WorkflowID        string          `json:"workflowId"`
	DefinitionVersion int             `json:"definitionVersion"`
	Input             json.RawMessage `json:"input"`
}

// createRunResponseDTO is the minimal Run identity docs/08-interface-spec.md §3.3 defines
// for a successful creation, deliberately narrower than domain.Run (no input/output/error/
// timestamps): matches Studio's CreateRunResponse field-for-field.
type createRunResponseDTO struct {
	ID                string           `json:"id"`
	WorkflowID        string           `json:"workflowId"`
	DefinitionVersion int              `json:"definitionVersion"`
	Status            domain.RunStatus `json:"status"`
	LastSeq           int64            `json:"lastSeq"`
}

// createRun serves POST /runs. It only starts the Run: NodeRun advancement happens after
// this transaction commits, driven by the work queue/Reconciler this package never calls
// directly (CLAUDE.md package boundaries).
func (d Deps) createRun(w http.ResponseWriter, r *http.Request) {
	var req createRunRequest
	if !decodeJSON(w, d.Logger, r, &req) {
		return
	}
	run, err := d.Execution.CreateRun(r.Context(), service.CreateRun{
		WorkflowID:        req.WorkflowID,
		DefinitionVersion: req.DefinitionVersion,
		Input:             req.Input,
	})
	if err != nil {
		writeError(w, d.Logger, err, codeDefinitionNotFound)
		return
	}
	writeJSON(w, http.StatusCreated, createRunResponseDTO{
		ID:                run.ID,
		WorkflowID:        run.WorkflowID,
		DefinitionVersion: run.DefinitionVersion,
		Status:            run.Status,
		LastSeq:           run.LastSeq,
	})
}

// runSnapshotResponse mirrors Studio's RunSnapshot. domain.Run and domain.NodeRun already
// carry wire-matching JSON tags, so only the envelope is defined here.
type runSnapshotResponse struct {
	Run      domain.Run       `json:"run"`
	NodeRuns []domain.NodeRun `json:"nodeRuns"`
	LastSeq  int64            `json:"lastSeq"`
}

// getRunSnapshot serves GET /runs/{runId}.
func (d Deps) getRunSnapshot(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")
	snap, err := d.Query.Snapshot(r.Context(), runID)
	if err != nil {
		writeError(w, d.Logger, err, codeRunNotFound)
		return
	}
	nodeRuns := snap.NodeRuns
	if nodeRuns == nil {
		nodeRuns = []domain.NodeRun{}
	}
	writeJSON(w, http.StatusOK, runSnapshotResponse{Run: snap.Run, NodeRuns: nodeRuns, LastSeq: snap.LastSeq})
}

// callbackBindingDTO mirrors Studio's CallbackBindingSummary (studio/src/api/types.ts):
// exactly the four fields docs/08-interface-spec.md lines 403-408/414 allow projecting
// from a Callback Binding. providerId and externalTaskId are the Provider's own stable
// identifiers, never a credential; the token hash never appears here.
type callbackBindingDTO struct {
	ID             string    `json:"id"`
	ProviderID     string    `json:"providerId"`
	ExternalTaskID string    `json:"externalTaskId"`
	CreatedAt      time.Time `json:"createdAt"`
}

// nodeAttemptDTO mirrors Studio's NodeAttempt. CallbackBinding is typed as `any` so a
// sync Attempt (or an async Attempt with no Binding yet) serialises the JSON literal
// null; Studio's NodeAttempt.callbackBinding is a required, non-optional key
// (studio/src/api/types.ts), so the key must still be present on every Attempt.
type nodeAttemptDTO struct {
	ID              string                   `json:"id"`
	NodeRunID       string                   `json:"nodeRunId,omitempty"`
	AttemptNo       int                      `json:"attemptNo"`
	Status          domain.NodeAttemptStatus `json:"status"`
	Input           json.RawMessage          `json:"input"`
	Result          json.RawMessage          `json:"result"`
	StartedAt       time.Time                `json:"startedAt"`
	DeadlineAt      *time.Time               `json:"deadlineAt"`
	DispatchedAt    *time.Time               `json:"dispatchedAt"`
	CompletedAt     *time.Time               `json:"completedAt"`
	Error           *domain.ExecutionError   `json:"error"`
	CallbackBinding any                      `json:"callbackBinding"`
}

// toNodeAttemptDTO projects a, together with its Callback Binding when one exists.
// binding is nil for a sync Attempt or an async Attempt not yet dispatched.
func toNodeAttemptDTO(a domain.NodeAttempt, binding *domain.CallbackBinding) nodeAttemptDTO {
	dto := nodeAttemptDTO{
		ID:           a.ID,
		NodeRunID:    a.NodeRunID,
		AttemptNo:    a.AttemptNo,
		Status:       a.Status,
		Input:        a.Input,
		Result:       a.Result,
		StartedAt:    a.StartedAt,
		DeadlineAt:   a.DeadlineAt,
		DispatchedAt: a.DispatchedAt,
		CompletedAt:  a.CompletedAt,
		Error:        a.Error,
	}
	if binding != nil {
		dto.CallbackBinding = callbackBindingDTO{
			ID:             binding.ID,
			ProviderID:     binding.ProviderID,
			ExternalTaskID: binding.ExternalTaskID,
			CreatedAt:      binding.CreatedAt,
		}
	}
	return dto
}

// nodeRunDetailResponse mirrors Studio's NodeRunDetail.
type nodeRunDetailResponse struct {
	NodeRun  domain.NodeRun   `json:"nodeRun"`
	Attempts []nodeAttemptDTO `json:"attempts"`
}

// getNodeRunDetail serves GET /runs/{runId}/nodes/{nodeRunId}.
func (d Deps) getNodeRunDetail(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")
	nodeRunID := chi.URLParam(r, "nodeRunId")
	detail, err := d.Query.NodeRunDetail(r.Context(), runID, nodeRunID)
	if err != nil {
		writeError(w, d.Logger, err, codeNodeRunNotFound)
		return
	}
	attempts := make([]nodeAttemptDTO, len(detail.Attempts))
	for i, a := range detail.Attempts {
		var binding *domain.CallbackBinding
		if b, ok := detail.CallbackBindings[a.ID]; ok {
			binding = &b
		}
		attempts[i] = toNodeAttemptDTO(a, binding)
	}
	writeJSON(w, http.StatusOK, nodeRunDetailResponse{NodeRun: detail.NodeRun, Attempts: attempts})
}

// ---- Agent Trace (docs/08-interface-spec.md §3.4) ----
//
// The DTOs below are deliberately narrower than the domain entities they project. An
// Agent Run's frozen Instructions, Model config and Schemas, a Turn's model request and
// response, a Decision's arguments, output and response summary, and a Tool Attempt's
// input and result are all execution payloads, and the Tool Attempt's callback token hash
// is a Secret (10-ops §4): none of them is a field here, so no future change to a domain
// struct can leak one through this response.

// agentTraceResponse is the whole projection: the Agent Run and its Turns in persisted
// order.
type agentTraceResponse struct {
	AgentRun agentRunDTO         `json:"agentRun"`
	Turns    []agentTraceTurnDTO `json:"turns"`
}

// agentRunDTO projects the Agent Run's identity, recovery pointers, deadline and outcome.
type agentRunDTO struct {
	ID                    string                   `json:"id"`
	NodeRunID             string                   `json:"nodeRunId"`
	Termination           *domain.AgentTermination `json:"termination"`
	CurrentTurnNo         int                      `json:"currentTurnNo"`
	CurrentContextVersion int                      `json:"currentContextVersion"`
	CurrentStateVersion   int                      `json:"currentStateVersion"`
	Deadline              time.Time                `json:"deadline"`
	TerminatedAt          *time.Time               `json:"terminatedAt"`
	Error                 *domain.ExecutionError   `json:"error"`
}

// agentTraceTurnDTO is one round. Decision, Action and ToolAttempts are typed so that a
// Turn which committed none of them serialises the JSON literal null (and an empty array
// for toolAttempts), because every key is required by the response shape.
type agentTraceTurnDTO struct {
	ID           string                 `json:"id"`
	TurnNo       int                    `json:"turnNo"`
	Status       domain.AgentTurnStatus `json:"status"`
	StartedAt    *time.Time             `json:"startedAt"`
	CompletedAt  *time.Time             `json:"completedAt"`
	Error        *domain.ExecutionError `json:"error"`
	Decision     *agentDecisionDTO      `json:"decision"`
	Action       *agentActionDTO        `json:"action"`
	ToolAttempts []toolAttemptDTO       `json:"toolAttempts"`
}

// agentDecisionDTO says what the model committed to, without saying what it said: the
// Decision kind, the Tool it named (null for a FINAL), and whether it carried a state
// patch at all. The patch content belongs to the State chain, which this projection never
// expands.
type agentDecisionDTO struct {
	Kind          domain.DecisionKind `json:"kind"`
	Tool          *string             `json:"tool"`
	HasStatePatch bool                `json:"hasStatePatch"`
}

// agentActionDTO projects the durable work item created with the Decision.
type agentActionDTO struct {
	ID          string                   `json:"id"`
	Type        domain.AgentActionType   `json:"type"`
	Status      domain.AgentActionStatus `json:"status"`
	StartedAt   *time.Time               `json:"startedAt"`
	CompletedAt *time.Time               `json:"completedAt"`
	Error       *domain.ExecutionError   `json:"error"`
}

// toolAttemptDTO projects one real Tool call. CallbackBinding is typed as `any` for the
// same reason nodeAttemptDTO's is: the key must be present on every Attempt, holding the
// JSON literal null for a synchronous Tool call.
type toolAttemptDTO struct {
	ID              string                   `json:"id"`
	ToolName        string                   `json:"toolName"`
	AttemptNo       int                      `json:"attemptNo"`
	Status          domain.ToolAttemptStatus `json:"status"`
	CallbackBinding any                      `json:"callbackBinding"`
	StartedAt       time.Time                `json:"startedAt"`
	DispatchedAt    *time.Time               `json:"dispatchedAt"`
	CompletedAt     *time.Time               `json:"completedAt"`
	Error           *domain.ExecutionError   `json:"error"`
}

// agentCallbackBindingDTO is the Tool Attempt's Binding summary: the same three
// non-credential identifiers docs/08-interface-spec.md lines 496-500 project, without the
// createdAt the Node Attempt summary carries.
type agentCallbackBindingDTO struct {
	ID             string `json:"id"`
	ProviderID     string `json:"providerId"`
	ExternalTaskID string `json:"externalTaskId"`
}

// getAgentTrace serves GET /runs/{runId}/nodes/{nodeRunId}/agent. A NodeRun that does not
// exist, belongs to another Run, or has no Agent Run is one 404 NODE_RUN_NOT_FOUND: the
// service returns domain.ErrNotFound for all three, and this route adds no code beyond
// the ones docs/08-interface-spec.md §6 defines.
func (d Deps) getAgentTrace(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")
	nodeRunID := chi.URLParam(r, "nodeRunId")
	trace, err := d.Query.AgentTrace(r.Context(), runID, nodeRunID)
	if err != nil {
		writeError(w, d.Logger, err, codeNodeRunNotFound)
		return
	}
	writeJSON(w, http.StatusOK, toAgentTraceResponse(trace))
}

func toAgentTraceResponse(trace service.AgentTrace) agentTraceResponse {
	turns := make([]agentTraceTurnDTO, len(trace.Turns))
	for i, round := range trace.Turns {
		turns[i] = toAgentTraceTurnDTO(round)
	}
	return agentTraceResponse{
		AgentRun: agentRunDTO{
			ID:                    trace.AgentRun.ID,
			NodeRunID:             trace.AgentRun.NodeRunID,
			Termination:           trace.AgentRun.Termination,
			CurrentTurnNo:         trace.AgentRun.CurrentTurnNo,
			CurrentContextVersion: trace.AgentRun.CurrentContextVersion,
			CurrentStateVersion:   trace.AgentRun.CurrentStateVersion,
			Deadline:              utcTime(trace.AgentRun.Deadline),
			TerminatedAt:          utcTimePtr(trace.AgentRun.TerminatedAt),
			Error:                 trace.AgentRun.Error,
		},
		Turns: turns,
	}
}

func toAgentTraceTurnDTO(round service.AgentTraceTurn) agentTraceTurnDTO {
	dto := agentTraceTurnDTO{
		ID:           round.Turn.ID,
		TurnNo:       round.Turn.TurnNo,
		Status:       round.Turn.Status,
		StartedAt:    utcTimePtr(round.Turn.StartedAt),
		CompletedAt:  utcTimePtr(round.Turn.CompletedAt),
		Error:        round.Turn.Error,
		ToolAttempts: []toolAttemptDTO{},
	}
	if round.Decision != nil {
		dto.Decision = &agentDecisionDTO{
			Kind:          round.Decision.Kind,
			Tool:          round.Decision.ToolName,
			HasStatePatch: hasJSONValue(round.Decision.StatePatch),
		}
	}
	if round.Action != nil {
		dto.Action = &agentActionDTO{
			ID:          round.Action.ID,
			Type:        round.Action.Type,
			Status:      round.Action.Status,
			StartedAt:   utcTimePtr(round.Action.StartedAt),
			CompletedAt: utcTimePtr(round.Action.CompletedAt),
			Error:       round.Action.Error,
		}
	}
	for _, attempt := range round.ToolAttempts {
		projected := toolAttemptDTO{
			ID:           attempt.ID,
			ToolName:     attempt.ToolName,
			AttemptNo:    attempt.AttemptNo,
			Status:       attempt.Status,
			StartedAt:    utcTime(attempt.StartedAt),
			DispatchedAt: utcTimePtr(attempt.DispatchedAt),
			CompletedAt:  utcTimePtr(attempt.CompletedAt),
			Error:        attempt.Error,
		}
		if binding, ok := round.CallbackBindings[attempt.ID]; ok {
			projected.CallbackBinding = agentCallbackBindingDTO{
				ID:             binding.ID,
				ProviderID:     binding.ProviderID,
				ExternalTaskID: binding.ExternalTaskID,
			}
		}
		dto.ToolAttempts = append(dto.ToolAttempts, projected)
	}
	return dto
}

// hasJSONValue reports whether raw holds an actual JSON value rather than nothing or the
// literal null, which is how an absent optional column comes back from the Store.
func hasJSONValue(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// utcTime and utcTimePtr normalise a persisted timestamp to UTC, so every timestamp in the
// Agent Trace projection is rendered as RFC 3339 UTC regardless of the session time zone
// the row was read with.
func utcTime(t time.Time) time.Time { return t.UTC() }

func utcTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	utc := t.UTC()
	return &utc
}
