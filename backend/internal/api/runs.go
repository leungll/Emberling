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

// createRunResponseDTO is the minimal Run identity the interface contract defines
// for a successful creation, deliberately narrower than domain.Run (no input/output/error/
// timestamps): matches Studio's CreateRunResponse field-for-field.
type createRunResponseDTO struct {
	ID                string           `json:"id"`
	WorkflowID        string           `json:"workflowId"`
	DefinitionVersion int              `json:"definitionVersion"`
	Status            domain.RunStatus `json:"status"`
	LastSeq           int64            `json:"lastSeq"`
}

// Idempotency-Key request header and the response header that marks a replay.
const (
	headerIdempotencyKey     = "Idempotency-Key"
	headerIdempotentReplayed = "Idempotent-Replayed"
	maxIdempotencyKeyLength  = 128
)

// idempotencyKey reads the optional Idempotency-Key header. Absent means none. Present,
// it must be one value of 1 to 128 printable ASCII characters (0x20 to 0x7E); anything
// else is invalid request content and ok is false.
func idempotencyKey(r *http.Request) (key string, ok bool) {
	values := r.Header.Values(headerIdempotencyKey)
	if len(values) == 0 {
		return "", true
	}
	if len(values) != 1 {
		return "", false
	}
	key = values[0]
	if len(key) == 0 || len(key) > maxIdempotencyKeyLength {
		return "", false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x20 || key[i] > 0x7e {
			return "", false
		}
	}
	return key, true
}

// createRun serves POST /runs. It only starts the Run: NodeRun advancement happens after
// this transaction commits, driven by the work queue/Reconciler this package never calls
// directly (CLAUDE.md package boundaries). With an Idempotency-Key, a submission the key
// already answered is replayed as 200 with the stored Run's current state and
// Idempotent-Replayed: true.
func (d Deps) createRun(w http.ResponseWriter, r *http.Request) {
	key, ok := idempotencyKey(r)
	if !ok {
		writeError(w, d.Logger, &badRequestError{
			message: "Idempotency-Key must be a single value of 1 to 128 printable ASCII characters",
		}, "")
		return
	}
	var req createRunRequest
	if !decodeJSON(w, d.Logger, r, &req) {
		return
	}
	result, err := d.Execution.SubmitRun(r.Context(), service.CreateRun{
		WorkflowID:        req.WorkflowID,
		DefinitionVersion: req.DefinitionVersion,
		Input:             req.Input,
		IdempotencyKey:    key,
	})
	if err != nil {
		writeError(w, d.Logger, err, codeDefinitionNotFound)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		w.Header().Set(headerIdempotentReplayed, "true")
		status = http.StatusOK
	}
	run := result.Run
	writeJSON(w, status, createRunResponseDTO{
		ID:                run.ID,
		WorkflowID:        run.WorkflowID,
		DefinitionVersion: run.DefinitionVersion,
		Status:            run.Status,
		LastSeq:           run.LastSeq,
	})
}

// runSnapshotResponse mirrors Studio's RunSnapshot. domain.Run and domain.NodeRun already
// carry wire-matching JSON tags, so only the envelope and the Run's read-time fields are
// defined here.
type runSnapshotResponse struct {
	Run      runSnapshotRunDTO `json:"run"`
	NodeRuns []domain.NodeRun  `json:"nodeRuns"`
	LastSeq  int64             `json:"lastSeq"`
}

// runSnapshotRunDTO is the persisted Run plus tokenUsage, the Token total the query
// computed from the NodeRuns of the same read. The key is always present and holds null
// when no NodeRun carries usage.
type runSnapshotRunDTO struct {
	domain.Run
	TokenUsage *domain.TokenUsage `json:"tokenUsage"`
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
	writeJSON(w, http.StatusOK, runSnapshotResponse{
		Run:      runSnapshotRunDTO{Run: snap.Run, TokenUsage: snap.TokenUsage},
		NodeRuns: nodeRuns,
		LastSeq:  snap.LastSeq,
	})
}

// callbackBindingDTO mirrors Studio's CallbackBindingSummary (studio/src/api/types.ts):
// exactly the four fields the interface contract allows projecting
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
	ID           string                   `json:"id"`
	NodeRunID    string                   `json:"nodeRunId,omitempty"`
	AttemptNo    int                      `json:"attemptNo"`
	Status       domain.NodeAttemptStatus `json:"status"`
	Input        json.RawMessage          `json:"input"`
	Result       json.RawMessage          `json:"result"`
	StartedAt    time.Time                `json:"startedAt"`
	DeadlineAt   *time.Time               `json:"deadlineAt"`
	DispatchedAt *time.Time               `json:"dispatchedAt"`
	CompletedAt  *time.Time               `json:"completedAt"`
	Error        *domain.ExecutionError   `json:"error"`
	// Uncertain is the failure's recorded determination: true or false on a FAILED
	// Attempt, null on any other Attempt and on one that failed before it was recorded.
	Uncertain       *bool `json:"uncertain"`
	CallbackBinding any   `json:"callbackBinding"`
	// Cost is the charge the Provider reported for this Attempt, recorded with its
	// result; null when none is known.
	Cost *costDTO `json:"cost"`
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
		Uncertain:    a.Uncertain,
		Cost:         toCostDTO(a.Cost),
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

// nodeRunDetailResponse mirrors Studio's NodeRunDetail. capacityWait is always present
// and null unless the NodeRun is READY and queued for a Provider slot.
type nodeRunDetailResponse struct {
	NodeRun      domain.NodeRun   `json:"nodeRun"`
	Attempts     []nodeAttemptDTO `json:"attempts"`
	CapacityWait *capacityWaitDTO `json:"capacityWait"`
}

// capacityWaitDTO is the live queue position of a NodeRun waiting for a Provider slot.
type capacityWaitDTO struct {
	ProviderID string `json:"providerId"`
	Limit      int    `json:"limit"`
	Held       int    `json:"held"`
	Position   int    `json:"position"`
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
	var wait *capacityWaitDTO
	if detail.CapacityWait != nil {
		wait = &capacityWaitDTO{
			ProviderID: detail.CapacityWait.ProviderID,
			Limit:      detail.CapacityWait.Limit,
			Held:       detail.CapacityWait.Held,
			Position:   detail.CapacityWait.Position,
		}
	}
	writeJSON(w, http.StatusOK, nodeRunDetailResponse{NodeRun: detail.NodeRun, Attempts: attempts, CapacityWait: wait})
}

// ---- Agent Trace ----
//
// The DTOs below are deliberately narrower than the domain entities they project. An
// Agent Run's frozen Instructions, Model config and Schemas, a Turn's model request and
// response, a Decision's arguments, state patch, output and response summary, a Tool
// Attempt's input and result, and the Context version chain are all execution payloads,
// and the Tool Attempt's callback token hash is a Secret: none of them is a field here, so
// no future change to a domain struct can leak one through this response. The only Tool
// input and result members shown are the named, type-checked ones
// agent_trace_tool_view.go selects, and the only State content shown is the Agent Run's
// latest State value, bounded by the Trace field bound and withheld above it.

// agentTraceResponse is the whole projection: the Agent Run, its Turns in persisted
// order, the execution facts it recorded, and its generation budget.
type agentTraceResponse struct {
	AgentRun         agentRunDTO         `json:"agentRun"`
	Turns            []agentTraceTurnDTO `json:"turns"`
	Facts            agentTraceFactsDTO  `json:"facts"`
	GenerationBudget generationBudgetDTO `json:"generationBudget"`
	// Workspace is the frozen workspace and its recorded patches, null for an Agent Run
	// that froze none.
	Workspace *agentWorkspaceDTO `json:"workspace"`
	// ChangeRequests are the change requests the Agent Run recorded, oldest first, named
	// by their identifiers, digests and content Artifact; the rendered content is read
	// through the Artifact content route, never carried here.
	ChangeRequests agentTraceChangeRequestsDTO `json:"changeRequests"`
}

// agentTraceChangeRequestsDTO is the bounded change request list. Truncated reports that
// the Agent Run recorded more requests than Items shows; the stored rows are never cut.
type agentTraceChangeRequestsDTO struct {
	Items     []changeRequestDTO `json:"items"`
	Truncated bool               `json:"truncated"`
}

// changeRequestDTO projects one recorded change request: which patch it describes, which
// verification fact it rests on and which text Artifact holds its content, with the digest
// of that content. The decision basis and the Markdown belong to the approval view.
type changeRequestDTO struct {
	ID                 string    `json:"id"`
	PatchDigest        string    `json:"patchDigest"`
	VerificationFactID string    `json:"verificationFactId"`
	ContentArtifactID  string    `json:"contentArtifactId"`
	ContentDigest      string    `json:"contentDigest"`
	ToolAttemptID      string    `json:"toolAttemptId"`
	CreatedAt          time.Time `json:"createdAt"`
}

// agentWorkspaceDTO projects the frozen workspace binding and the summaries of its
// recorded patches, oldest first. The patch text is never part of the response: a patch is
// named by its sequence number, digest and touched paths. PatchesTruncated reports that the
// Agent Run holds more patches than Patches shows; the stored rows are never cut.
type agentWorkspaceDTO struct {
	RepositoryID     string              `json:"repositoryId"`
	BaseCommit       string              `json:"baseCommit"`
	AllowedPaths     []string            `json:"allowedPaths"`
	DeployTarget     *deployTargetDTO    `json:"deployTarget"`
	Patches          []workspacePatchDTO `json:"patches"`
	PatchesTruncated bool                `json:"patchesTruncated"`
}

type deployTargetDTO struct {
	Service     string `json:"service"`
	Environment string `json:"environment"`
}

type workspacePatchDTO struct {
	Seq           int       `json:"seq"`
	PatchDigest   string    `json:"patchDigest"`
	TouchedPaths  []string  `json:"touchedPaths"`
	ToolAttemptID string    `json:"toolAttemptId"`
	CreatedAt     time.Time `json:"createdAt"`
}

// agentTraceFactsDTO is the bounded fact ledger, oldest first. Truncated reports that the
// Agent Run recorded more facts than Items shows; the stored facts are never cut.
type agentTraceFactsDTO struct {
	Items     []executionFactDTO `json:"items"`
	Truncated bool               `json:"truncated"`
}

// executionFactDTO projects one recorded fact. Bindings hold only the references and
// digests the Tool's fact declaration binds, never a credential or storage key.
type executionFactDTO struct {
	ID            string          `json:"id"`
	FactType      string          `json:"factType"`
	Subject       string          `json:"subject"`
	Bindings      json.RawMessage `json:"bindings"`
	Verdict       *bool           `json:"verdict"`
	BasisFactID   *string         `json:"basisFactId"`
	ToolAttemptID string          `json:"toolAttemptId"`
	CreatedAt     time.Time       `json:"createdAt"`
}

// generationBudgetDTO is the frozen generation limit (null when the Agent Run has none)
// and the generation calls already made, counted whatever their outcome.
type generationBudgetDTO struct {
	MaxGenerationCalls  *int `json:"maxGenerationCalls"`
	GenerationCallsUsed int  `json:"generationCallsUsed"`
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
	// PausedAt and PauseReason are set while the Agent's clock is stopped; the deadline is
	// then not running and moves later on resume. Goal is the frozen task goal, nil when
	// the node configures none.
	PausedAt    *time.Time               `json:"pausedAt"`
	PauseReason *domain.AgentPauseReason `json:"pauseReason"`
	Goal        *domain.AgentGoal        `json:"goal"`
	// MaxFailedFacts is the frozen revision limit per fact type, null when the node bounds
	// none. The failures already collected are not projected; the fact ledger shows them.
	MaxFailedFacts map[string]int         `json:"maxFailedFacts"`
	TerminatedAt   *time.Time             `json:"terminatedAt"`
	Error          *domain.ExecutionError `json:"error"`
	// Completion is the goal-based completion verdict: null while the Agent Run runs and
	// for an Agent Run without a goal, which is never evaluated.
	Completion *agentCompletionDTO `json:"completion"`
	// LatestState is the State value the Agent Run's current State pointer names.
	LatestState *latestStateDTO `json:"latestState"`
}

// latestStateDTO projects the Agent Run's latest State Version. Value is the saved JSON,
// compacted, when its canonical encoding fits the Trace field bound; otherwise it is null
// and Withheld says why. The value is never truncated: a cut JSON document would read as
// a different State.
type latestStateDTO struct {
	Version  int             `json:"version"`
	Value    json.RawMessage `json:"value"`
	Withheld string          `json:"withheld,omitempty"`
}

// toLatestStateDTO decides what of the latest State Version the Trace shows. A State
// Version with no saved value is the empty object the State chain starts from.
func toLatestStateDTO(version domain.AgentStateVersion, maxFieldBytes int) *latestStateDTO {
	dto := &latestStateDTO{Version: version.Version, Value: json.RawMessage(`{}`)}
	if len(version.Value) == 0 {
		return dto
	}
	var decoded any
	if err := json.Unmarshal(version.Value, &decoded); err != nil {
		return dto
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return dto
	}
	if len(canonical) > maxFieldBytes {
		dto.Value, dto.Withheld = json.RawMessage(`null`), withheldTooLarge
		return dto
	}
	dto.Value = canonical
	return dto
}

// agentCompletionDTO projects the completion verdict: the task result, every check with
// the records it rested on, and what an INCOMPLETE task still lacks.
type agentCompletionDTO struct {
	Outcome     domain.CompletionOutcome   `json:"outcome"`
	Checks      []domain.CompletionCheck   `json:"checks"`
	Missing     []domain.CompletionMissing `json:"missing"`
	EvaluatedAt time.Time                  `json:"evaluatedAt"`
}

func toAgentCompletionDTO(completion *domain.AgentCompletion) *agentCompletionDTO {
	if completion == nil {
		return nil
	}
	dto := &agentCompletionDTO{
		Outcome:     completion.Outcome,
		Checks:      completion.Checks,
		Missing:     completion.Missing,
		EvaluatedAt: utcTime(completion.EvaluatedAt),
	}
	if dto.Checks == nil {
		dto.Checks = []domain.CompletionCheck{}
	}
	if dto.Missing == nil {
		dto.Missing = []domain.CompletionMissing{}
	}
	for i := range dto.Checks {
		if dto.Checks[i].Refs == nil {
			dto.Checks[i].Refs = []string{}
		}
	}
	return dto
}

// agentTraceTurnDTO is one round. Decision, Action and ToolAttempts are typed so that a
// Turn which committed none of them serialises the JSON literal null (and an empty array
// for toolAttempts), because every key is required by the response shape.
type agentTraceTurnDTO struct {
	ID          string                 `json:"id"`
	TurnNo      int                    `json:"turnNo"`
	Status      domain.AgentTurnStatus `json:"status"`
	StartedAt   *time.Time             `json:"startedAt"`
	CompletedAt *time.Time             `json:"completedAt"`
	Error       *domain.ExecutionError `json:"error"`
	// Usage is the Token usage this Turn's model call reported, null when the Provider
	// reported none or the Turn has no committed model result.
	Usage *domain.TokenUsage `json:"usage"`
	// Cost is the charge the Provider reported for this Turn's model call, null when it
	// reported none or the Turn has no committed model result.
	Cost *costDTO `json:"cost"`
	// ClaimSource is who claimed the Turn, read from its committed Event; null before the
	// Turn was claimed.
	ClaimSource  *domain.ClaimSource `json:"claimSource"`
	Decision     *agentDecisionDTO   `json:"decision"`
	Action       *agentActionDTO     `json:"action"`
	ToolAttempts []toolAttemptDTO    `json:"toolAttempts"`
	// Manifest is the Turn's Context Manifest: the materials its model request carried
	// and left out, as identifiers, versions, digests and lengths. It serialises null
	// until the Turn has been claimed, and never carries the material itself.
	Manifest []manifestItemDTO `json:"manifest"`
}

// manifestItemDTO projects one Context Manifest item. For an included item the digest and
// length are those of the request bytes; for an omitted ARTIFACT they describe the
// referenced content, and `ref` is the Artifact ID the artifact content route takes.
type manifestItemDTO struct {
	Kind     domain.ManifestItemKind `json:"kind"`
	Label    string                  `json:"label"`
	Ref      string                  `json:"ref"`
	Version  string                  `json:"version"`
	SHA256   string                  `json:"sha256"`
	Length   int                     `json:"length"`
	Included bool                    `json:"included"`
	// OmittedReason is null for an included item.
	OmittedReason *domain.ManifestOmission `json:"omittedReason"`
	// Range is the line range a MATERIAL_RANGE item carried; null for every other kind.
	Range *manifestRangeDTO `json:"range"`
}

// manifestRangeDTO projects the lines a material range item carried, numbered from one,
// with the line count of the whole artifact.
type manifestRangeDTO struct {
	StartLine  int `json:"startLine"`
	LineCount  int `json:"lineCount"`
	TotalLines int `json:"totalLines"`
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

// agentActionDTO projects the durable work item created with the Decision. The three
// sources are read from the Action's committed Events, each null until that Event exists.
type agentActionDTO struct {
	ID               string                   `json:"id"`
	Type             domain.AgentActionType   `json:"type"`
	Status           domain.AgentActionStatus `json:"status"`
	StartedAt        *time.Time               `json:"startedAt"`
	CompletedAt      *time.Time               `json:"completedAt"`
	Error            *domain.ExecutionError   `json:"error"`
	ClaimSource      *domain.ClaimSource      `json:"claimSource"`
	CompletionSource *domain.CompletionSource `json:"completionSource"`
	FailureSource    *domain.FailureSource    `json:"failureSource"`
	// Authorization is the claim's decision for this Action, nil until it decided;
	// AuthorizationReason explains a DENY. Approval is the human decision requested for an
	// ASK, nil otherwise.
	Authorization       *domain.AuthorizationDecision `json:"authorization"`
	AuthorizationReason *string                       `json:"authorizationReason"`
	Approval            *approvalDTO                  `json:"approval"`
	// Release is the release view of a deploy_change Action: its operations, the target
	// occupation and the human wait. Null for every other Action.
	Release *releaseActionDTO `json:"release"`
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
	// Cost is the charge the Provider reported for this Tool call, null when none is
	// known. It is a tracing figure, not a Tool output member.
	Cost *costDTO `json:"cost"`

	// The keys below are the Tool-call facts of agent_trace_tool_view.go. Each is present
	// only for the Tool it describes and only once the facts it shows are committed;
	// Withheld names a member left out because it exceeded the field bound or could carry
	// a credential.
	Photo        *toolPhotoDTO        `json:"photo,omitempty"`
	Settings     *toolSettingsDTO     `json:"settings,omitempty"`
	Review       *toolReviewDTO       `json:"review,omitempty"`
	Test         *toolTestDTO         `json:"test,omitempty"`
	Verification *toolVerificationDTO `json:"verification,omitempty"`
	Deployment   *toolDeploymentDTO   `json:"deployment,omitempty"`
	Video        *toolVideoDTO        `json:"video,omitempty"`
	Withheld     []withheldFieldDTO   `json:"withheld,omitempty"`
}

// agentCallbackBindingDTO is the Tool Attempt's Binding summary: the same three
// non-credential identifiers the interface contract projects, without the
// createdAt the Node Attempt summary carries.
type agentCallbackBindingDTO struct {
	ID             string `json:"id"`
	ProviderID     string `json:"providerId"`
	ExternalTaskID string `json:"externalTaskId"`
}

// getAgentTrace serves GET /runs/{runId}/nodes/{nodeRunId}/agent. A NodeRun that does not
// exist, belongs to another Run, or has no Agent Run is one 404 NODE_RUN_NOT_FOUND: the
// service returns domain.ErrNotFound for all three, and this route adds no code beyond
// the ones the interface contract defines.
func (d Deps) getAgentTrace(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")
	nodeRunID := chi.URLParam(r, "nodeRunId")
	trace, err := d.Query.AgentTrace(r.Context(), runID, nodeRunID)
	if err != nil {
		writeError(w, d.Logger, err, codeNodeRunNotFound)
		return
	}
	writeJSON(w, http.StatusOK, toAgentTraceResponse(trace, d.TraceMaxFieldBytes))
}

func toAgentTraceResponse(trace service.AgentTrace, maxFieldBytes int) agentTraceResponse {
	projector := newToolViewProjector(trace.RunInput, maxFieldBytes)
	turns := make([]agentTraceTurnDTO, len(trace.Turns))
	for i, round := range trace.Turns {
		turns[i] = toAgentTraceTurnDTO(round)
		for j, attempt := range round.ToolAttempts {
			view := projector.project(attempt)
			projected := &turns[i].ToolAttempts[j]
			projected.Photo, projected.Settings, projected.Review = view.Photo, view.Settings, view.Review
			projected.Test, projected.Verification, projected.Deployment, projected.Video, projected.Withheld =
				view.Test, view.Verification, view.Deployment, view.Video, view.Withheld
		}
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
			PausedAt:              utcTimePtr(trace.AgentRun.PausedAt),
			PauseReason:           trace.AgentRun.PauseReason,
			Goal:                  trace.AgentRun.Goal,
			MaxFailedFacts:        trace.AgentRun.MaxFailedFacts,
			TerminatedAt:          utcTimePtr(trace.AgentRun.TerminatedAt),
			Error:                 trace.AgentRun.Error,
			Completion:            toAgentCompletionDTO(trace.Completion),
			LatestState:           toLatestStateDTO(trace.LatestState, maxFieldBytes),
		},
		Turns: turns,
		Facts: toAgentTraceFactsDTO(trace.Facts),
		GenerationBudget: generationBudgetDTO{
			MaxGenerationCalls:  trace.Budget.MaxGenerationCalls,
			GenerationCallsUsed: trace.Budget.Used,
		},
		Workspace:      toAgentWorkspaceDTO(trace.Workspace),
		ChangeRequests: toAgentTraceChangeRequestsDTO(trace.ChangeRequests),
	}
}

func toAgentTraceChangeRequestsDTO(requests service.AgentTraceChangeRequests) agentTraceChangeRequestsDTO {
	items := make([]changeRequestDTO, len(requests.Requests))
	for i, request := range requests.Requests {
		items[i] = toChangeRequestDTO(request)
	}
	return agentTraceChangeRequestsDTO{Items: items, Truncated: requests.Truncated}
}

func toChangeRequestDTO(request domain.ChangeRequest) changeRequestDTO {
	return changeRequestDTO{
		ID:                 request.ID,
		PatchDigest:        request.PatchDigest,
		VerificationFactID: request.VerificationFactID,
		ContentArtifactID:  request.ContentArtifactID,
		ContentDigest:      request.ContentDigest,
		ToolAttemptID:      request.ToolAttemptID,
		CreatedAt:          utcTime(request.CreatedAt),
	}
}

func toWorkspacePatchDTO(patch domain.WorkspacePatch) workspacePatchDTO {
	touched := patch.TouchedPaths
	if touched == nil {
		touched = []string{}
	}
	return workspacePatchDTO{
		Seq:           patch.Seq,
		PatchDigest:   patch.PatchDigest,
		TouchedPaths:  touched,
		ToolAttemptID: patch.ToolAttemptID,
		CreatedAt:     utcTime(patch.CreatedAt),
	}
}

func toManifestItemDTO(item domain.ManifestItem) manifestItemDTO {
	dto := manifestItemDTO{
		Kind:          item.Kind,
		Label:         item.Label,
		Ref:           item.Ref,
		Version:       item.Version,
		SHA256:        item.SHA256,
		Length:        item.Length,
		Included:      item.Included,
		OmittedReason: item.OmittedReason,
	}
	if item.Range != nil {
		dto.Range = &manifestRangeDTO{StartLine: item.Range.StartLine, LineCount: item.Range.LineCount, TotalLines: item.Range.TotalLines}
	}
	return dto
}

func toAgentWorkspaceDTO(workspace *service.AgentTraceWorkspace) *agentWorkspaceDTO {
	if workspace == nil {
		return nil
	}
	allowed := workspace.Workspace.AllowedPaths
	if allowed == nil {
		allowed = []string{}
	}
	dto := &agentWorkspaceDTO{
		RepositoryID:     workspace.Workspace.RepositoryID,
		BaseCommit:       workspace.Workspace.BaseCommit,
		AllowedPaths:     allowed,
		Patches:          make([]workspacePatchDTO, len(workspace.Patches)),
		PatchesTruncated: workspace.PatchesTruncated,
	}
	if target := workspace.Workspace.DeployTarget; target != nil {
		dto.DeployTarget = &deployTargetDTO{Service: target.Service, Environment: target.Environment}
	}
	for i, patch := range workspace.Patches {
		dto.Patches[i] = toWorkspacePatchDTO(patch)
	}
	return dto
}

func toAgentTraceFactsDTO(ledger service.AgentTraceFacts) agentTraceFactsDTO {
	items := make([]executionFactDTO, len(ledger.Facts))
	for i, fact := range ledger.Facts {
		bindings := fact.Binding
		if !hasJSONValue(bindings) {
			bindings = json.RawMessage(`{}`)
		}
		items[i] = executionFactDTO{
			ID:            fact.ID,
			FactType:      fact.FactType,
			Subject:       fact.SubjectRef,
			Bindings:      bindings,
			Verdict:       fact.Verdict,
			BasisFactID:   fact.BasisFactID,
			ToolAttemptID: fact.ToolAttemptID,
			CreatedAt:     utcTime(fact.CreatedAt),
		}
	}
	return agentTraceFactsDTO{Items: items, Truncated: ledger.Truncated}
}

func toAgentTraceTurnDTO(round service.AgentTraceTurn) agentTraceTurnDTO {
	dto := agentTraceTurnDTO{
		ID:           round.Turn.ID,
		TurnNo:       round.Turn.TurnNo,
		Status:       round.Turn.Status,
		StartedAt:    utcTimePtr(round.Turn.StartedAt),
		CompletedAt:  utcTimePtr(round.Turn.CompletedAt),
		Error:        round.Turn.Error,
		Usage:        round.Turn.TokenUsage,
		Cost:         toCostDTO(round.Turn.Cost),
		ClaimSource:  round.TurnClaimSource,
		ToolAttempts: []toolAttemptDTO{},
	}
	if round.Manifest != nil {
		dto.Manifest = make([]manifestItemDTO, 0, len(round.Manifest.Items))
		for _, item := range round.Manifest.Items {
			dto.Manifest = append(dto.Manifest, toManifestItemDTO(item))
		}
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

			ClaimSource:      round.ActionSources.Claim,
			CompletionSource: round.ActionSources.Completion,
			FailureSource:    round.ActionSources.Failure,

			Authorization:       round.Action.Authorization,
			AuthorizationReason: round.Action.AuthorizationReason,
			Approval:            toApprovalDTOPtr(round.Approval),
			Release:             toReleaseActionDTO(round),
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
			Cost:         toCostDTO(attempt.Cost),
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
