// Package deployrelease implements the `deploy_change` Tool: the asynchronous release of an
// approved change request to the production deployment service's /v2/ routes, whose outcome
// the Runtime reconciles after the fact instead of ever resending it.
//
// The Agent names only the change request it wants released. The target comes from the
// Agent Run's frozen workspace, the base commit and patch digest from the workspace view
// the Runtime resolved and checked against the change request before this Tool is called;
// the model chooses none of them. Every request names the operation key of its Attempt, so
// production can tell a replay from a new release and the Runtime can query, void or
// confirm exactly that operation later.
//
// It performs exactly one request per method and owns no retry, timeout, state transition
// or Event. A refusal is production's definite answer that nothing was deployed; a lost
// answer is reported as a plain error and the Runtime decides what it means. Its errors
// carry only the Tool name, a fixed operation label, an HTTP status and, for a refusal, a
// bounded code - never the base URL, the arguments or a response body.
package deployrelease

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// ToolName is this Tool's stable Registry name, stored in an Agent's Tool allowlist.
const ToolName = "deploy_change"

// ProviderID is the stable identifier of the production service in the Callback Binding
// that routes polls to a release Attempt. It is not a credential.
const ProviderID = domain.ReleaseProviderID

// FactType is the fact a confirmed release records: the patch, by digest, was released
// under an operation key as a version, and the target was healthy or not afterwards.
const FactType = "release_confirmed"

const deploymentsPath = "/v2/deployments"
const targetsPath = "/v2/targets"

// Poll policy: production confirms within seconds, so polls are two seconds apart; the
// bound exists so polling is finite, and reaching it is not a failure.
const (
	pollIntervalMs = 2_000
	pollMaxPolls   = 10_000
)

// defaultTimeout bounds one request when no HTTP client is injected.
const defaultTimeout = 10 * time.Second

// maxResponseBytes bounds every response this Tool reads.
const maxResponseBytes = 64 << 10

// defaultRejectionCode is reported when a refusal carries no usable code of its own.
const defaultRejectionCode = "REJECTED"

var rejectionCodePattern = regexp.MustCompile(`^[A-Z][A-Z_]{0,31}$`)

// Statuses production reports for a release operation.
const (
	statusAccepted  = "ACCEPTED"
	statusDeploying = "DEPLOYING"
	statusDeployed  = "DEPLOYED"
	statusVoided    = "VOIDED"
)

const inputSchema = `{
  "type": "object",
  "properties": {
    "changeRequestId": {"type": "string", "minLength": 1}
  },
  "required": ["changeRequestId"],
  "additionalProperties": false
}`

// OutputSchema is the shape of a confirmed release's result. The Runtime assembles it from
// production's deployment and target answers; the Tool only reports each answer.
const OutputSchema = `{
  "type": "object",
  "properties": {
    "operationKey": {"type": "string", "minLength": 1},
    "version": {"type": "string", "minLength": 1},
    "patchDigest": {"type": "string", "minLength": 1},
    "healthy": {"type": "boolean"},
    "reason": {"type": "string"}
  },
  "required": ["operationKey", "version", "patchDigest", "healthy"],
  "additionalProperties": false
}`

// Registration returns the `deploy_change` ToolRegistration releasing through the
// production service at baseURL. A nil client means a default client bounded by
// defaultTimeout.
//
// A release changes production, so the call is an EXTERNAL side effect KEYED by its
// operation key: production deduplicates by it, which is why the Runtime may query and void
// an operation it is unsure about but never resends it. The Tool is reconcilable and
// occupies the frozen deploy target from approval until the release settles.
func Registration(baseURL string, client *http.Client) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          ToolName,
			Title:         "Deploy change",
			Description:   "Release an approved change request to the workspace's deploy target and confirm the deployed version",
			InputSchema:   json.RawMessage(inputSchema),
			OutputSchema:  json.RawMessage(OutputSchema),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed},
			ExecutionKind: domain.ToolExecutionAsync,
			Poll:          &domain.PollPolicy{IntervalMs: pollIntervalMs, MaxPolls: pollMaxPolls},
			Produces: &domain.FactProduction{
				FactType:       FactType,
				SubjectPointer: domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/patchDigest"},
				BindArguments: map[string]domain.FactPointer{
					"operationKey": {Source: domain.FactPointerResult, Pointer: "/operationKey"},
					"version":      {Source: domain.FactPointerResult, Pointer: "/version"},
				},
				VerdictPointer: &domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/healthy"},
			},
			Requires:                    []domain.FactRequirement{},
			Records:                     []domain.ToolRecordKind{},
			CountsTowardGenerationLimit: false,
			Authorization:               &domain.ToolAuthorization{Risk: domain.ToolRiskProduction, Reversible: false},
			UsesWorkspace:               true,
			Reconcilable:                true,
			OccupiesReleaseTarget:       true,
			Display: []domain.ToolDisplayField{
				{Source: domain.FactPointerArguments, Pointer: "/changeRequestId", Title: "Change request"},
				{Source: domain.FactPointerResult, Pointer: "/version", Title: "Version"},
				{Source: domain.FactPointerResult, Pointer: "/healthy", Title: "Healthy"},
				{Source: domain.FactPointerResult, Pointer: "/reason", Title: "Reason"},
			},
		},
		Executor: New(baseURL, client),
	}
}

// Executor implements registry.ReconcilableToolExecutor for `deploy_change`.
type Executor struct {
	baseURL    string
	httpClient *http.Client
}

// New returns an Executor releasing through the production service at baseURL.
func New(baseURL string, client *http.Client) *Executor {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	return &Executor{baseURL: strings.TrimRight(baseURL, "/"), httpClient: client}
}

type target struct {
	Service     string `json:"service"`
	Environment string `json:"environment"`
}

// releaseRequest is the body sent to production; field order is the body's key order.
type releaseRequest struct {
	OperationID string `json:"operationId"`
	Target      target `json:"target"`
	BaseCommit  string `json:"baseCommit"`
	PatchDigest string `json:"patchDigest"`
}

type releaseResponse struct {
	OperationID string `json:"operationId"`
	Status      string `json:"status"`
	Version     string `json:"version"`
}

type targetResponse struct {
	Version         string `json:"version"`
	PatchDigest     string `json:"patchDigest"`
	Healthy         bool   `json:"healthy"`
	LastOperationID string `json:"lastOperationId"`
}

type refusalResponse struct {
	Code string `json:"code"`
}

// DeployedResult is what a DEPLOYED answer carries into the Runtime: the operation and the
// version production assigned. The Runtime completes the result after reading the target.
type DeployedResult struct {
	OperationKey string `json:"operationKey"`
	Version      string `json:"version"`
}

// Execute sends the release request for this Attempt's operation key. A 2xx answer means
// production holds the operation and the Attempt is DISPATCHED under that key. A 409 is
// production's definite refusal, reported as a registry.ProviderFailure with its code. Any
// other status or a transport failure leaves the outcome unknown and is a plain error.
func (e *Executor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	if action.Workspace == nil || action.Workspace.DeployTarget == nil {
		return registry.ToolExecutionResult{}, releaseError("build request", 0, "the call carries no workspace deploy target")
	}
	if action.Workspace.BaseCommit == "" || action.Workspace.PatchDigest == "" {
		return registry.ToolExecutionResult{}, releaseError("build request", 0, "the workspace view carries no base commit or patch digest")
	}
	if action.ActionID == "" || action.AttemptNo < 1 {
		return registry.ToolExecutionResult{}, releaseError("build request", 0, "action carries no Attempt identity")
	}
	operationKey := domain.ReleaseOperationKey(action.ActionID, action.AttemptNo)
	body, err := json.Marshal(releaseRequest{
		OperationID: operationKey,
		Target:      target{Service: action.Workspace.DeployTarget.Service, Environment: action.Workspace.DeployTarget.Environment},
		BaseCommit:  action.Workspace.BaseCommit,
		PatchDigest: action.Workspace.PatchDigest,
	})
	if err != nil {
		return registry.ToolExecutionResult{}, releaseError("build request", 0, "request body could not be encoded")
	}
	response, err := e.send(ctx, http.MethodPost, deploymentsPath, body)
	if err != nil {
		return registry.ToolExecutionResult{}, err
	}
	defer drain(response)

	switch {
	case response.StatusCode == http.StatusConflict:
		return registry.ToolExecutionResult{}, &registry.ProviderFailure{Err: domain.ExecutionError{
			Code:    rejectionCode(response.Body),
			Message: ToolName + ": production refused the release",
		}}
	case response.StatusCode < 200 || response.StatusCode > 299:
		return registry.ToolExecutionResult{}, releaseError("release outcome unknown", response.StatusCode, "unexpected status")
	}
	var accepted releaseResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&accepted); err != nil || accepted.OperationID != operationKey {
		return registry.ToolExecutionResult{}, releaseError("release outcome unknown", response.StatusCode, "response does not confirm this operation")
	}
	return registry.ToolExecutionResult{
		Kind:         registry.ToolResultDispatched,
		ExternalTask: &registry.ExternalTask{ProviderID: ProviderID, ExternalTaskID: operationKey},
	}, nil
}

// OnCallback is never used: production does not call back, the Runtime polls.
func (e *Executor) OnCallback(context.Context, registry.ToolAsyncState, []byte) (registry.ToolResult, error) {
	return registry.ToolResult{}, releaseError("callback", 0, "the release tool receives no callbacks; its outcome is polled")
}

// Poll queries the operation once. ACCEPTED and DEPLOYING are RUNNING; DEPLOYED is
// SUCCEEDED with the version; VOIDED is FAILED with the VOIDED code; a 404 is UNKNOWN_TASK;
// a 409 with production's code is FAILED with that code; anything else is UNKNOWN. A
// transport failure is an error: production gave no answer.
func (e *Executor) Poll(ctx context.Context, state registry.ToolAsyncState) (registry.PollResult, error) {
	response, err := e.send(ctx, http.MethodGet, deploymentsPath+"/"+state.ExternalTask.ExternalTaskID, nil)
	if err != nil {
		return registry.PollResult{}, err
	}
	defer drain(response)
	return interpret(response, state.ExternalTask.ExternalTaskID)
}

// Void asks production to void the operation once and reports the status production
// answers with, in Poll's vocabulary. A refusal here is of the void, not of the operation,
// which may be in flight, so only a confirmed void is a definite failure; any other refusal
// is UNKNOWN and leaves the next query to find out.
func (e *Executor) Void(ctx context.Context, state registry.ToolAsyncState) (registry.PollResult, error) {
	response, err := e.send(ctx, http.MethodPost, deploymentsPath+"/"+state.ExternalTask.ExternalTaskID+"/void", nil)
	if err != nil {
		return registry.PollResult{}, err
	}
	defer drain(response)
	result, err := interpret(response, state.ExternalTask.ExternalTaskID)
	if err != nil {
		return registry.PollResult{}, err
	}
	if result.Status == registry.PollFailed && (result.Error == nil || result.Error.Code != domain.ReleaseCodeVoided) {
		return registry.PollResult{Status: registry.PollStatusUnknown}, nil
	}
	return result, nil
}

// CheckTarget reads what the deploy target runs now. A 404 is a target with no deployment;
// any other non-200 status or a transport failure is an error: production gave no answer.
func (e *Executor) CheckTarget(ctx context.Context, state registry.ToolAsyncState) (registry.TargetState, error) {
	if state.ReleaseTarget == nil || state.ReleaseTarget.Service == "" || state.ReleaseTarget.Environment == "" {
		return registry.TargetState{}, releaseError("build target request", 0, "the attempt carries no deploy target")
	}
	response, err := e.send(ctx, http.MethodGet, targetsPath+"/"+state.ReleaseTarget.Service+"/"+state.ReleaseTarget.Environment, nil)
	if err != nil {
		return registry.TargetState{}, err
	}
	defer drain(response)
	switch {
	case response.StatusCode == http.StatusNotFound:
		return registry.TargetState{}, nil
	case response.StatusCode != http.StatusOK:
		return registry.TargetState{}, releaseError("read target", response.StatusCode, "unexpected status")
	}
	var current targetResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&current); err != nil || current.Version == "" {
		return registry.TargetState{}, releaseError("read target", response.StatusCode, "response body is not a target state")
	}
	return registry.TargetState{
		Known: true, Version: current.Version, PatchDigest: current.PatchDigest,
		Healthy: current.Healthy, LastOperationID: current.LastOperationID,
	}, nil
}

// interpret maps one operation status response onto the poll vocabulary.
func interpret(response *http.Response, operationKey string) (registry.PollResult, error) {
	switch {
	case response.StatusCode == http.StatusNotFound:
		return registry.PollResult{Status: registry.PollUnknownTask}, nil
	case response.StatusCode == http.StatusConflict:
		// Production's refusal names its code. A 409 without one, from something in front
		// of production for example, is not a definite answer about the operation.
		code, ok := refusalCode(response.Body)
		if !ok {
			return registry.PollResult{Status: registry.PollStatusUnknown}, nil
		}
		return registry.PollResult{Status: registry.PollFailed, Error: &domain.ExecutionError{
			Code: code, Message: ToolName + ": production refused the release",
		}}, nil
	case response.StatusCode != http.StatusOK:
		return registry.PollResult{Status: registry.PollStatusUnknown}, nil
	}
	var status releaseResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&status); err != nil || status.OperationID != operationKey {
		return registry.PollResult{Status: registry.PollStatusUnknown}, nil
	}
	switch status.Status {
	case statusAccepted, statusDeploying:
		return registry.PollResult{Status: registry.PollRunning}, nil
	case statusDeployed:
		if status.Version == "" {
			return registry.PollResult{Status: registry.PollStatusUnknown}, nil
		}
		output, err := json.Marshal(DeployedResult{OperationKey: operationKey, Version: status.Version})
		if err != nil {
			return registry.PollResult{}, releaseError("encode result", 0, "result could not be encoded")
		}
		return registry.PollResult{Status: registry.PollSucceeded, ToolResult: &registry.ToolResult{Output: output}}, nil
	case statusVoided:
		return registry.PollResult{Status: registry.PollFailed, Error: &domain.ExecutionError{
			Code: domain.ReleaseCodeVoided, Message: ToolName + ": production confirmed the operation was voided; nothing was deployed under it",
		}}, nil
	default:
		return registry.PollResult{Status: registry.PollStatusUnknown}, nil
	}
}

// send issues one request. A transport failure keeps only the context's own error, so a
// deadline stays distinguishable without the request URL travelling on.
func (e *Executor) send(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, e.baseURL+path, reader)
	if err != nil {
		return nil, releaseError("build request", 0, "request could not be created")
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := e.httpClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%s: send request: %w", ToolName, ctxErr)
		}
		return nil, releaseError("send request", 0, "transport failure")
	}
	return response, nil
}

func drain(response *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	_ = response.Body.Close()
}

// rejectionCode reads the refusal's code when it is a short upper-case identifier, and
// falls back to defaultRejectionCode otherwise, so no free text from the body travels on.
func rejectionCode(body io.Reader) string {
	code, ok := refusalCode(body)
	if !ok {
		return defaultRejectionCode
	}
	return code
}

// refusalCode reads the refusal's code and reports whether the body carried a usable one.
func refusalCode(body io.Reader) (string, bool) {
	var refusal refusalResponse
	if err := json.NewDecoder(io.LimitReader(body, maxResponseBytes)).Decode(&refusal); err != nil ||
		!rejectionCodePattern.MatchString(refusal.Code) {
		return "", false
	}
	return refusal.Code, true
}

// releaseError builds a failure from fixed labels only, so no request or response content
// can reach Trace or a log through it.
func releaseError(op string, statusCode int, reason string) error {
	if statusCode != 0 {
		return fmt.Errorf("%s: %s (status %d): %w", ToolName, op, statusCode, errors.New(reason))
	}
	return fmt.Errorf("%s: %s: %w", ToolName, op, errors.New(reason))
}

var _ registry.ReconcilableToolExecutor = (*Executor)(nil)
