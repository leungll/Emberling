// Package verifypatch implements the `verify_patch` Tool: the ASYNC dispatch of the Agent
// Run's current workspace patch to the sandbox runner's POST /v1/verifications. The runner
// applies the patch to the frozen base commit of the registered repository, runs the base
// commit's tests, a vulnerability scan and a dependency comparison against a pinned
// snapshot, and reports the outcome later through the shared callback endpoint, which
// resumes the same Tool Attempt. The Tool also answers status polls, so a runner that lost
// the task is detected instead of waited for.
//
// The Agent names only the patch digest it wants verified. The patch text, repository and
// base commit come from the workspace view the Runtime resolved in the claim transaction,
// never from the model, and the Runtime refuses a digest that is not the workspace's
// current patch before this Tool is called. A completed verification records a
// `patch_verified` fact about the digest, bound to the base commit and the result digest,
// whose verdict is the overall pass. The runner's log is kept as a text Execution Artifact
// and the result names it by id; the fact binds the reference, never the text.
//
// It performs exactly one registered operation. It owns no retry, timeout, callback
// routing, state transition or Event. The one-time plaintext callback token travels in the
// request body and nowhere else, this package logs nothing, and its errors carry only the
// stable Provider identifier, a short operation label and an HTTP status code.
package verifypatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// ToolName is this Tool's stable Registry name, stored in an Agent's Tool allowlist.
const ToolName = "verify_patch"

// ProviderID is the stable identifier of the sandbox runner this Tool dispatches to. It is
// persisted in the Callback Binding and shown in Trace; it is not a credential.
const ProviderID = "sandboxrunner"

// FactType is the fact a completed verification records: the patch, identified by its
// digest, was verified against a base commit with the three checks, and passed or not.
const FactType = "patch_verified"

const verificationsPath = "/v1/verifications"

// Poll policy: a verification builds and tests a module, so polls are spaced apart and
// bounded well inside an Agent deadline of some minutes.
const (
	pollIntervalMs = 5_000
	pollMaxPolls   = 120
)

// defaultTimeout bounds one dispatch or status query when no HTTP client is injected.
const defaultTimeout = 30 * time.Second

// maxResponseBytes bounds the response this Tool reads. A status response carries the
// result payload with its log, so it is bounded above the runner's log limit.
const maxResponseBytes = 1 << 20

// Payload statuses the runner reports.
const (
	statusAccepted  = "ACCEPTED"
	statusRunning   = "RUNNING"
	statusSucceeded = "SUCCEEDED"
	statusFailed    = "FAILED"
)

// defaultFailureCode is used when the runner reports FAILED without naming a code.
const defaultFailureCode = "VERIFICATION_FAILED"

// maxFailureMessageBytes bounds the runner-supplied failure message, which reaches the
// Tool Attempt's error and Trace.
const maxFailureMessageBytes = 512

// maxSummaryEntries bounds the advisory and dependency lists copied into the result.
const maxSummaryEntries = 32

// emptyLogText is stored when the runner reported no log at all, so the result always
// names a log artifact and an empty log is distinguishable from a lost one.
const emptyLogText = "(the sandbox runner reported no log)\n"

const inputSchema = `{
  "type": "object",
  "properties": {
    "patchDigest": {"type": "string", "minLength": 1},
    "mock": {"type": "string"}
  },
  "required": ["patchDigest"],
  "additionalProperties": false
}`

const outputSchema = `{
  "type": "object",
  "properties": {
    "passed": {"type": "boolean"},
    "testsPassed": {"type": "boolean"},
    "scanClean": {"type": "boolean"},
    "dependenciesClean": {"type": "boolean"},
    "baseCommit": {"type": "string", "minLength": 1},
    "patchDigest": {"type": "string", "minLength": 1},
    "resultDigest": {"type": "string", "minLength": 1},
    "summary": {
      "type": "object",
      "properties": {
        "tests": {
          "type": "object",
          "properties": {
            "total": {"type": "integer", "minimum": 0},
            "failed": {"type": "integer", "minimum": 0}
          },
          "required": ["total", "failed"],
          "additionalProperties": false
        },
        "scanner": {"type": "string"},
        "scanFindings": {"type": "array", "items": {"type": "string"}},
        "vulnerableDependencies": {"type": "array", "items": {"type": "string"}},
        "snapshotDigest": {"type": "string"}
      },
      "required": ["tests", "scanner", "scanFindings", "vulnerableDependencies", "snapshotDigest"],
      "additionalProperties": false
    },
    "logArtifactId": {"type": "string", "minLength": 1},
    "logTruncated": {"type": "boolean"}
  },
  "required": ["passed", "testsPassed", "scanClean", "dependenciesClean", "baseCommit", "patchDigest", "resultDigest", "summary", "logArtifactId", "logTruncated"],
  "additionalProperties": false
}`

// ArtifactTextStore is the port this Tool writes the verification log through. The store
// returns the content-addressed reference the result declares; the Runtime commits the
// reference's metadata with the result.
type ArtifactTextStore interface {
	WriteText(ctx context.Context, r io.Reader) (domain.ArtifactRef, error)
}

// Registration returns the `verify_patch` ToolRegistration dispatching to the sandbox
// runner at baseURL and keeping logs in artifacts. A nil client means a default client
// bounded by defaultTimeout.
//
// Verifying is an EXTERNAL call that is SAFE to repeat: a verification changes nothing
// outside the runner's throwaway checkout, so a lost task may be handed back to the Agent
// as interrupted work. The Tool uses the workspace and names the patch digest it acts on at
// /patchDigest, which the Runtime compares with the current patch before dispatch.
func Registration(baseURL string, client *http.Client, artifacts ArtifactTextStore) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          ToolName,
			Description:   "Verify the workspace's current patch in an isolated sandbox: run the base commit's tests, scan for known vulnerabilities and check every dependency version",
			InputSchema:   json.RawMessage(inputSchema),
			OutputSchema:  json.RawMessage(outputSchema),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencySafe},
			ExecutionKind: domain.ToolExecutionAsync,
			Poll:          &domain.PollPolicy{IntervalMs: pollIntervalMs, MaxPolls: pollMaxPolls},
			Produces: &domain.FactProduction{
				FactType:       FactType,
				SubjectPointer: domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/patchDigest"},
				BindArguments: map[string]domain.FactPointer{
					"baseCommit":   {Source: domain.FactPointerResult, Pointer: "/baseCommit"},
					"resultDigest": {Source: domain.FactPointerResult, Pointer: "/resultDigest"},
				},
				VerdictPointer: &domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/passed"},
			},
			Requires:                    []domain.FactRequirement{},
			Records:                     []domain.ToolRecordKind{},
			CountsTowardGenerationLimit: false,
			UsesWorkspace:               true,
			WorkspacePatchDigestPath:    "/patchDigest",
		},
		Executor: New(baseURL, client, artifacts),
	}
}

// Executor implements registry.PollableAsyncToolExecutor for `verify_patch`.
type Executor struct {
	baseURL    string
	httpClient *http.Client
	artifacts  ArtifactTextStore
}

// New returns an Executor dispatching to the sandbox runner at baseURL.
func New(baseURL string, client *http.Client, artifacts ArtifactTextStore) *Executor {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	return &Executor{baseURL: strings.TrimRight(baseURL, "/"), httpClient: client, artifacts: artifacts}
}

type arguments struct {
	PatchDigest string `json:"patchDigest"`
	Mock        string `json:"mock"`
}

// verificationRequest is this Tool's view of the runner's POST /v1/verifications body. It
// carries no image, mount or command: the runner decides how a verification runs.
type verificationRequest struct {
	CallbackURL   string `json:"callbackUrl"`
	CallbackToken string `json:"callbackToken"`
	RepositoryID  string `json:"repositoryId"`
	BaseCommit    string `json:"baseCommit"`
	Patch         string `json:"patch"`
	Mock          string `json:"mock,omitempty"`
}

type acceptedResponse struct {
	VerificationID string `json:"verificationId"`
}

type statusResponse struct {
	VerificationID string          `json:"verificationId"`
	Status         string          `json:"status"`
	Result         json.RawMessage `json:"result"`
}

type testCounts struct {
	Total  int `json:"total"`
	Failed int `json:"failed"`
}

type summary struct {
	Tests                  testCounts `json:"tests"`
	Scanner                string     `json:"scanner"`
	ScanFindings           []string   `json:"scanFindings"`
	VulnerableDependencies []string   `json:"vulnerableDependencies"`
	SnapshotDigest         string     `json:"snapshotDigest"`
}

// result is the Tool's result, republished member by member from the runner payload with
// the log replaced by the artifact that holds it.
type result struct {
	Passed            bool    `json:"passed"`
	TestsPassed       bool    `json:"testsPassed"`
	ScanClean         bool    `json:"scanClean"`
	DependenciesClean bool    `json:"dependenciesClean"`
	BaseCommit        string  `json:"baseCommit"`
	PatchDigest       string  `json:"patchDigest"`
	ResultDigest      string  `json:"resultDigest"`
	Summary           summary `json:"summary"`
	LogArtifactID     string  `json:"logArtifactId"`
	LogTruncated      bool    `json:"logTruncated"`
}

// Execute dispatches the verification of the workspace's current patch and returns its
// external identity. Being an ASYNC Tool it returns a DISPATCHED result or an explicit
// error, never a completed result.
func (e *Executor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	if action.Callback == nil || strings.TrimSpace(action.Callback.URL) == "" || strings.TrimSpace(action.Callback.Token) == "" {
		return registry.ToolExecutionResult{}, dispatchError("build request", 0, "callback URL and token are both required")
	}
	if action.Workspace == nil {
		return registry.ToolExecutionResult{}, dispatchError("build request", 0, "the call carries no workspace view")
	}
	var args arguments
	if err := json.Unmarshal(action.Arguments, &args); err != nil || args.PatchDigest == "" {
		return registry.ToolExecutionResult{}, dispatchError("build request", 0, "arguments carry no patchDigest")
	}
	// The Runtime refused a stale digest before this call; the check is repeated here so
	// this Tool can never send a patch other than the one the Agent named.
	if args.PatchDigest != action.Workspace.PatchDigest {
		return registry.ToolExecutionResult{}, dispatchError("build request", 0, "patchDigest is not the workspace's current patch")
	}

	body, err := json.Marshal(verificationRequest{
		CallbackURL:   action.Callback.URL,
		CallbackToken: action.Callback.Token,
		RepositoryID:  action.Workspace.RepositoryID,
		BaseCommit:    action.Workspace.BaseCommit,
		Patch:         action.Workspace.Patch,
		Mock:          args.Mock,
	})
	if err != nil {
		return registry.ToolExecutionResult{}, dispatchError("build request", 0, "request body could not be encoded")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+verificationsPath, bytes.NewReader(body))
	if err != nil {
		return registry.ToolExecutionResult{}, dispatchError("build request", 0, "request could not be created")
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := e.httpClient.Do(request)
	if err != nil {
		// A transport error from net/http quotes the URL, so it is not wrapped.
		return registry.ToolExecutionResult{}, dispatchError("send request", 0, "transport failure")
	}
	defer drain(response)
	if response.StatusCode != http.StatusAccepted {
		// The body is not read into the error: a runner may echo the request, including
		// the callback token and the patch, inside its own error body.
		return registry.ToolExecutionResult{}, dispatchError("runner rejected the dispatch", response.StatusCode, "status is not 202 Accepted")
	}
	var accepted acceptedResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&accepted); err != nil || accepted.VerificationID == "" {
		return registry.ToolExecutionResult{}, dispatchError("decode response", response.StatusCode, "accepted response carries no verificationId")
	}
	return registry.ToolExecutionResult{
		Kind:         registry.ToolResultDispatched,
		ExternalTask: &registry.ExternalTask{ProviderID: ProviderID, ExternalTaskID: accepted.VerificationID},
	}, nil
}

// payload is the runner's result payload as this Tool reads it, from a callback or a
// status query.
type payload struct {
	Status            string   `json:"status"`
	Passed            *bool    `json:"passed"`
	TestsPassed       *bool    `json:"testsPassed"`
	ScanClean         *bool    `json:"scanClean"`
	DependenciesClean *bool    `json:"dependenciesClean"`
	BaseCommit        string   `json:"baseCommit"`
	PatchDigest       string   `json:"patchDigest"`
	ResultDigest      string   `json:"resultDigest"`
	Summary           *summary `json:"summary"`
	Log               *string  `json:"log"`
	LogTruncated      bool     `json:"logTruncated"`
	Error             *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// OnCallback converts one runner callback payload into the Tool result.
//
// A reported verification failure, such as a module the offline cache lacks, becomes a
// *registry.ProviderFailure, which fails the Tool Attempt with the callback as its failure
// source. A failing check is not such a failure: it is a SUCCEEDED verification whose
// verdict is false. A payload this Tool cannot interpret stays a plain error, since a
// malformed delivery is not evidence that the verification failed. The log is written to
// content storage here, before the result is returned, so the result can name it; the
// Runtime commits the reference with the result.
func (e *Executor) OnCallback(ctx context.Context, state registry.ToolAsyncState, raw []byte) (registry.ToolResult, error) {
	var body payload
	if err := json.Unmarshal(raw, &body); err != nil {
		return registry.ToolResult{}, fmt.Errorf("%s: callback payload for external task %s is not a JSON object", ToolName, state.ExternalTask.ExternalTaskID)
	}
	return e.interpret(ctx, state.ExternalTask.ExternalTaskID, body)
}

// interpret turns a final payload into the Tool result or the Provider-reported failure.
func (e *Executor) interpret(ctx context.Context, externalTaskID string, body payload) (registry.ToolResult, error) {
	switch body.Status {
	case statusSucceeded:
		if body.Passed == nil || body.TestsPassed == nil || body.ScanClean == nil || body.DependenciesClean == nil ||
			body.BaseCommit == "" || body.PatchDigest == "" || body.ResultDigest == "" || body.Summary == nil || body.Log == nil {
			return registry.ToolResult{}, fmt.Errorf("%s: payload for external task %s reports %s without every verdict, binding, summary and log", ToolName, externalTaskID, statusSucceeded)
		}
		if body.Summary.Tests.Total < 0 || body.Summary.Tests.Failed < 0 {
			return registry.ToolResult{}, fmt.Errorf("%s: payload for external task %s reports a negative test count", ToolName, externalTaskID)
		}
		if e.artifacts == nil {
			return registry.ToolResult{}, fmt.Errorf("%s: external task %s: artifact content storage is not configured", ToolName, externalTaskID)
		}
		logText := *body.Log
		if logText == "" {
			logText = emptyLogText
		}
		ref, err := e.artifacts.WriteText(ctx, strings.NewReader(logText))
		if err != nil {
			// The store's error names no path; the log is not quoted either.
			return registry.ToolResult{}, fmt.Errorf("%s: external task %s: store verification log: %w", ToolName, externalTaskID, err)
		}
		output, err := json.Marshal(result{
			Passed:            *body.Passed,
			TestsPassed:       *body.TestsPassed,
			ScanClean:         *body.ScanClean,
			DependenciesClean: *body.DependenciesClean,
			BaseCommit:        body.BaseCommit,
			PatchDigest:       body.PatchDigest,
			ResultDigest:      body.ResultDigest,
			Summary: summary{
				Tests:                  body.Summary.Tests,
				Scanner:                body.Summary.Scanner,
				ScanFindings:           boundedStrings(body.Summary.ScanFindings),
				VulnerableDependencies: boundedStrings(body.Summary.VulnerableDependencies),
				SnapshotDigest:         body.Summary.SnapshotDigest,
			},
			LogArtifactID: ref.ArtifactID,
			LogTruncated:  body.LogTruncated,
		})
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("%s: encode result for external task %s: %w", ToolName, externalTaskID, err)
		}
		return registry.ToolResult{Output: output, Artifacts: []domain.ArtifactRef{ref}}, nil
	case statusFailed:
		failure := domain.ExecutionError{Code: defaultFailureCode, Message: "sandbox runner reported the verification failed"}
		if body.Error != nil {
			if body.Error.Code != "" {
				failure.Code = truncateUTF8(body.Error.Code, maxFailureMessageBytes)
			}
			if body.Error.Message != "" {
				failure.Message = truncateUTF8(body.Error.Message, maxFailureMessageBytes)
			}
		}
		return registry.ToolResult{}, &registry.ProviderFailure{Err: failure}
	default:
		return registry.ToolResult{}, fmt.Errorf("%s: payload for external task %s carries status %q, want %s or %s", ToolName, externalTaskID, body.Status, statusSucceeded, statusFailed)
	}
}

// Poll asks the runner for the verification's status. A 404 is the runner's definite
// answer that it does not know the task, reported as UNKNOWN_TASK so the Runtime can hand
// the lost call back to the Agent; any other non-2xx answer or unreadable body is UNKNOWN
// and changes nothing. A finished status carries the same payload the callback would.
func (e *Executor) Poll(ctx context.Context, state registry.ToolAsyncState) (registry.PollResult, error) {
	externalTaskID := state.ExternalTask.ExternalTaskID
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, e.baseURL+verificationsPath+"/"+externalTaskID, nil)
	if err != nil {
		return registry.PollResult{}, dispatchError("build status request", 0, "request could not be created")
	}
	response, err := e.httpClient.Do(request)
	if err != nil {
		return registry.PollResult{Status: registry.PollStatusUnknown}, nil
	}
	defer drain(response)
	switch {
	case response.StatusCode == http.StatusNotFound:
		return registry.PollResult{Status: registry.PollUnknownTask}, nil
	case response.StatusCode != http.StatusOK:
		return registry.PollResult{Status: registry.PollStatusUnknown}, nil
	}
	var status statusResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&status); err != nil {
		return registry.PollResult{Status: registry.PollStatusUnknown}, nil
	}
	switch status.Status {
	case statusAccepted, statusRunning:
		return registry.PollResult{Status: registry.PollRunning}, nil
	case statusSucceeded, statusFailed:
		var body payload
		if err := json.Unmarshal(status.Result, &body); err != nil || body.Status != status.Status {
			return registry.PollResult{Status: registry.PollStatusUnknown}, nil
		}
		toolResult, err := e.interpret(ctx, externalTaskID, body)
		var providerFailure *registry.ProviderFailure
		switch {
		case errors.As(err, &providerFailure):
			failure := providerFailure.Err
			return registry.PollResult{Status: registry.PollFailed, Error: &failure}, nil
		case err != nil:
			return registry.PollResult{}, err
		}
		return registry.PollResult{Status: registry.PollSucceeded, ToolResult: &toolResult}, nil
	default:
		return registry.PollResult{Status: registry.PollStatusUnknown}, nil
	}
}

func drain(response *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	_ = response.Body.Close()
}

// boundedStrings copies at most maxSummaryEntries entries, each cut to the failure message
// bound, and never returns nil so the result always encodes an array.
func boundedStrings(items []string) []string {
	out := make([]string, 0, min(len(items), maxSummaryEntries))
	for _, item := range items {
		if len(out) == maxSummaryEntries {
			break
		}
		out = append(out, truncateUTF8(item, maxFailureMessageBytes))
	}
	return out
}

// truncateUTF8 cuts s to at most limit bytes without splitting a multi-byte rune.
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// dispatchError builds a failure from fixed labels only, so no request or response content
// can reach Trace or a log through it.
func dispatchError(op string, statusCode int, reason string) error {
	if statusCode != 0 {
		return fmt.Errorf("%s: dispatch to provider %q: %s (status %d): %w", ToolName, ProviderID, op, statusCode, errors.New(reason))
	}
	return fmt.Errorf("%s: dispatch to provider %q: %s: %w", ToolName, ProviderID, op, errors.New(reason))
}
