// Package deploy implements the `deploy` Tool: one synchronous deployment request to the
// production deployment service's POST /v1/deployments.
//
// It performs exactly one registered operation. It owns no retry, timeout, state
// transition or Event: a refused or failed deployment is reported upward and the Agent
// Runtime decides what happens next.
//
// Every request carries an operationId derived from the Agent Action and its Attempt, so
// each Attempt in the ledger matches at most one deployment in production's own records.
// Production may deduplicate by it, but the Runtime does not rely on that.
//
// Its errors carry only the Tool name, a fixed operation label, an HTTP status code and,
// for a refusal, a bounded error code - never the base URL, the arguments, the parameters
// or a response body.
package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// ToolName is this Tool's stable Registry name, stored in an Agent's Tool allowlist.
const ToolName = "deploy"

const deploymentsPath = "/v1/deployments"

// defaultTimeout bounds one request when no HTTP client is injected. The Agent deadline
// still arrives through ctx and wins whenever it is shorter.
const defaultTimeout = 30 * time.Second

// maxResponseBytes bounds the deployment response this Tool reads.
const maxResponseBytes = 64 << 10

// defaultRejectionCode is reported when a refusal carries no usable code of its own.
const defaultRejectionCode = "REJECTED"

// rejectionCodePattern bounds the code a refusal may carry into the Action's failure.
var rejectionCodePattern = regexp.MustCompile(`^[A-Z][A-Z_]{0,31}$`)

const inputSchema = `{
  "type": "object",
  "properties": {
    "target": {
      "type": "object",
      "properties": {
        "service": {"type": "string", "minLength": 1},
        "environment": {"type": "string", "minLength": 1}
      },
      "required": ["service", "environment"],
      "additionalProperties": false
    },
    "baseCommit": {"type": "string", "minLength": 1},
    "patchDigest": {"type": "string", "minLength": 1},
    "parameters": {"type": "object", "additionalProperties": true}
  },
  "required": ["target", "baseCommit", "patchDigest"],
  "additionalProperties": false
}`

const outputSchema = `{
  "type": "object",
  "properties": {
    "operationId": {"type": "string", "minLength": 1},
    "version": {"type": "string", "minLength": 1}
  },
  "required": ["operationId", "version"],
  "additionalProperties": false
}`

// Registration returns the `deploy` ToolRegistration calling the production deployment
// service at baseURL. A nil client means a default client bounded by defaultTimeout.
//
// A deployment changes production, so the call has an EXTERNAL side effect. Its
// idempotency is UNKNOWN: whether production deduplicates by operationId is not something
// the Runtime may depend on, so an uncertain call is never sent again. A deployment is not
// a generation, so it does not count toward the Agent's generation limit.
func Registration(baseURL string, client *http.Client) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:                        ToolName,
			Description:                 "Deploy a reviewed patch on a base commit to one service environment",
			InputSchema:                 json.RawMessage(inputSchema),
			OutputSchema:                json.RawMessage(outputSchema),
			SideEffect:                  domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
			ExecutionKind:               domain.ToolExecutionSync,
			Requires:                    []domain.FactRequirement{},
			CountsTowardGenerationLimit: false,
		},
		Executor: New(baseURL, client),
	}
}

// Executor implements registry.ToolExecutor for `deploy`.
type Executor struct {
	baseURL    string
	httpClient *http.Client
}

// New returns an Executor calling the production deployment service at baseURL.
func New(baseURL string, client *http.Client) *Executor {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	return &Executor{baseURL: strings.TrimRight(baseURL, "/"), httpClient: client}
}

// OperationID returns the operationId one Attempt of an Agent Action deploys under.
func OperationID(actionID string, attemptNo int) string {
	return "op_" + actionID + "_" + strconv.Itoa(attemptNo)
}

type target struct {
	Service     string `json:"service"`
	Environment string `json:"environment"`
}

type arguments struct {
	Target      *target         `json:"target"`
	BaseCommit  string          `json:"baseCommit"`
	PatchDigest string          `json:"patchDigest"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// deploymentRequest is the body sent to production; field order is the body's key order.
type deploymentRequest struct {
	OperationID string          `json:"operationId"`
	Target      target          `json:"target"`
	BaseCommit  string          `json:"baseCommit"`
	PatchDigest string          `json:"patchDigest"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type deploymentResponse struct {
	OperationID string `json:"operationId"`
	Version     string `json:"version"`
}

type refusalResponse struct {
	Code string `json:"code"`
}

// result is the Tool's result; field order is the result's key order.
type result struct {
	OperationID string `json:"operationId"`
	Version     string `json:"version"`
}

// Execute sends one deployment request. A 409 is production's definite refusal: nothing
// was deployed, and it is reported as a registry.ProviderFailure. Any other non-2xx status
// or a transport failure leaves the outcome unknown and is reported as a plain error.
func (e *Executor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	var args arguments
	if err := json.Unmarshal(action.Arguments, &args); err != nil || args.Target == nil ||
		args.Target.Service == "" || args.Target.Environment == "" || args.BaseCommit == "" || args.PatchDigest == "" {
		return registry.ToolExecutionResult{}, deployError("build request", 0, "arguments are not a deployment request")
	}
	if action.ActionID == "" || action.AttemptNo < 1 {
		return registry.ToolExecutionResult{}, deployError("build request", 0, "action carries no Attempt identity")
	}
	operationID := OperationID(action.ActionID, action.AttemptNo)
	body, err := json.Marshal(deploymentRequest{
		OperationID: operationID,
		Target:      *args.Target,
		BaseCommit:  args.BaseCommit,
		PatchDigest: args.PatchDigest,
		Parameters:  args.Parameters,
	})
	if err != nil {
		return registry.ToolExecutionResult{}, deployError("build request", 0, "request body could not be encoded")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+deploymentsPath, bytes.NewReader(body))
	if err != nil {
		return registry.ToolExecutionResult{}, deployError("build request", 0, "request could not be created")
	}
	httpRequest.Header.Set("Content-Type", "application/json")

	response, err := e.httpClient.Do(httpRequest)
	if err != nil {
		// The transport error quotes the request URL, so only the context's own error is
		// kept: it lets the caller tell a deadline from any other transport failure.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return registry.ToolExecutionResult{}, fmt.Errorf("%s: send request: %w", ToolName, ctxErr)
		}
		return registry.ToolExecutionResult{}, deployError("send request", 0, "transport failure")
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		_ = response.Body.Close()
	}()

	switch {
	case response.StatusCode == http.StatusConflict:
		return registry.ToolExecutionResult{}, &registry.ProviderFailure{Err: domain.ExecutionError{
			Code:    rejectionCode(response.Body),
			Message: ToolName + ": production refused the deployment",
		}}
	case response.StatusCode < 200 || response.StatusCode > 299:
		return registry.ToolExecutionResult{}, deployError("deployment outcome unknown", response.StatusCode, "unexpected status")
	}

	var deployed deploymentResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&deployed); err != nil {
		return registry.ToolExecutionResult{}, deployError("decode response", response.StatusCode, "response body is not a deployment response")
	}
	if deployed.OperationID != operationID || deployed.Version == "" {
		return registry.ToolExecutionResult{}, deployError("decode response", response.StatusCode, "response does not confirm this operation")
	}
	output, err := json.Marshal(result{OperationID: operationID, Version: deployed.Version})
	if err != nil {
		return registry.ToolExecutionResult{}, deployError("encode result", 0, "result could not be encoded")
	}
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{Output: output}}, nil
}

// rejectionCode reads the refusal's code when it is a short upper-case identifier, and
// falls back to defaultRejectionCode otherwise, so no free text from the body travels on.
func rejectionCode(body io.Reader) string {
	var refusal refusalResponse
	if err := json.NewDecoder(io.LimitReader(body, maxResponseBytes)).Decode(&refusal); err != nil ||
		!rejectionCodePattern.MatchString(refusal.Code) {
		return defaultRejectionCode
	}
	return refusal.Code
}

// deployError builds a deployment failure from fixed labels only, so no request or
// response content can reach Trace or a log through it.
func deployError(op string, statusCode int, reason string) error {
	if statusCode != 0 {
		return fmt.Errorf("%s: %s (status %d): %w", ToolName, op, statusCode, errors.New(reason))
	}
	return fmt.Errorf("%s: %s: %w", ToolName, op, errors.New(reason))
}
