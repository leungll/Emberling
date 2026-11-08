// Package reviewpatch implements the `review_patch` Tool: an independent review of the Agent
// Run's current workspace patch by the review model the Agent node selected, which must be
// a model other than the Agent's own decision model.
//
// The Agent names only the patch digest. The patch text, the repository and the base
// commit come from the workspace view the Runtime resolved in the claim transaction, and the
// repository's rules file and advisories are read from a throwaway rebuild of that
// workspace; nothing the model says can change the patch. The Runtime refuses a digest that
// is not the workspace's current patch before this Tool is called. The review model answers
// with a structured opinion: whether it found a blocking problem, its findings and a
// summary. The whole opinion is kept as a text Execution Artifact; the result names it by id
// and gives the Agent the summary and the bounded list of blocking findings. A completed
// review records a `patch_reviewed` fact about the digest, bound to the review model and
// the opinion Artifact, whose verdict is true when the review found no blocking problem.
//
// The review is a second opinion for the Agent and the approver, never a gate: the Runtime
// does not require it to pass before a change request or a release.
//
// It performs exactly one registered operation: one model call and one Artifact write. It
// owns no retry, timeout, state transition or Event, never selects a replacement model, and
// never falls back to a mock policy when the frozen review model cannot serve. Its errors
// carry the Tool name, the review Model ID and fixed labels only, never the patch, the
// repository text or the model's answer.
package reviewpatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// ToolName is this Tool's stable Registry name, stored in an Agent's Tool allowlist.
const ToolName = "review_patch"

// FactType is the fact a completed review records: the patch, identified by its digest,
// was reviewed by the bound model, with or without a blocking problem.
const FactType = "patch_reviewed"

// ContextNames are the repository entries read beside the patch, in this order: the
// project rules file at the repository root and every file of the advisories directory.
// Entries the repository does not have are left out of the review.
var ContextNames = []string{"RULES.md", "advisories"}

// Bounds. The patch bound is the workspace's own patch limit; the opinion bound keeps the
// Artifact and the result small whatever the model writes.
const (
	maxPatchBytes   = 256 << 10
	maxOpinionBytes = 64 << 10
	// maxBlockingFindings bounds the blocking findings the result repeats for the Agent;
	// the Artifact holds them all.
	maxBlockingFindings    = 8
	maxFindingPathRunes    = 256
	maxFindingMessageRunes = 300
	maxSummaryRunes        = 1000
)

const inputSchema = `{
  "type": "object",
  "properties": {
    "patchDigest": {"type": "string", "minLength": 64, "maxLength": 64}
  },
  "required": ["patchDigest"],
  "additionalProperties": false
}`

const outputSchema = `{
  "type": "object",
  "properties": {
    "patchDigest": {"type": "string", "minLength": 64, "maxLength": 64},
    "reviewModelId": {"type": "string", "minLength": 1},
    "passed": {"type": "boolean"},
    "blocking": {"type": "boolean"},
    "findingCount": {"type": "integer", "minimum": 0},
    "blockingFindings": {
      "type": "array",
      "maxItems": 8,
      "items": {
        "type": "object",
        "properties": {
          "severity": {"type": "string", "minLength": 1},
          "path": {"type": "string", "maxLength": 256},
          "message": {"type": "string", "maxLength": 300}
        },
        "required": ["severity", "path", "message"],
        "additionalProperties": false
      }
    },
    "summary": {"type": "string", "maxLength": 1000},
    "opinionArtifactId": {"type": "string", "minLength": 1}
  },
  "required": ["patchDigest", "reviewModelId", "passed", "blocking", "findingCount", "blockingFindings", "summary", "opinionArtifactId"],
  "additionalProperties": false
}`

// Ports are what a review reads and writes through. Every port is required: a process that
// cannot serve one of them fails the call before any model is asked.
type Ports struct {
	Models    ModelResolver
	Artifacts ArtifactTextStore
	Workspace WorkspaceReader
}

// ModelResolver is the port through which the Tool finds the review model of a call's
// frozen review Model ID. *registry.ModelRegistry satisfies it.
type ModelResolver interface {
	Get(modelID string) (registry.ModelRegistration, registry.ModelProvider, bool)
}

// ArtifactTextStore is the port this Tool writes the opinion through. The store returns the
// content-addressed reference the result declares; the Runtime commits the reference's
// metadata with the result.
type ArtifactTextStore interface {
	WriteText(ctx context.Context, r io.Reader) (domain.ArtifactRef, error)
}

// WorkspaceReader is the port through which the Tool reads the repository's own text, at
// the workspace's current patch, for the entries ContextNames lists. It reads only.
type WorkspaceReader interface {
	ReadFiles(ctx context.Context, view *registry.WorkspaceView, names []string) ([]ContextFile, error)
}

// ContextFile is one repository file the review reads beside the patch.
type ContextFile struct {
	Path      string
	Content   string
	Truncated bool
}

// Registration returns the `review_patch` ToolRegistration reading and writing through
// ports.
//
// A review only reads the workspace and asks a model, so it has no side effect and is safe
// to repeat; the opinion Artifact it writes is referenced only by a committed result, so a
// call interrupted before its result commits leaves unreferenced content and nothing else.
// The Tool uses the workspace and names the patch it reviews at /patchDigest, which the
// Runtime compares with the current patch before the call. It requires an independent
// review model declaring structured decision output: the node must select one, different
// from its decision model, and the Tool never reviews with a mock policy.
func Registration(ports Ports) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          ToolName,
			Title:         "Review patch",
			Description:   "Review the workspace's current patch with the node's independent review model against the repository's rules and advisories; the opinion is kept as a text artifact",
			InputSchema:   json.RawMessage(inputSchema),
			OutputSchema:  json.RawMessage(outputSchema),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			ExecutionKind: domain.ToolExecutionSync,
			Produces: &domain.FactProduction{
				FactType:       FactType,
				SubjectPointer: domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/patchDigest"},
				BindArguments: map[string]domain.FactPointer{
					"reviewModelId":     {Source: domain.FactPointerResult, Pointer: "/reviewModelId"},
					"opinionArtifactId": {Source: domain.FactPointerResult, Pointer: "/opinionArtifactId"},
				},
				VerdictPointer: &domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/passed"},
			},
			Requires:                    []domain.FactRequirement{},
			Records:                     []domain.ToolRecordKind{},
			CountsTowardGenerationLimit: false,
			UsesReviewModel:             true,
			ReviewModelCapability:       domain.ModelCapabilityStructuredDecision,
			RequiresReviewModel:         true,
			UsesWorkspace:               true,
			WorkspacePatchDigestPath:    "/patchDigest",
		},
		Executor: New(ports),
	}
}

// Executor implements registry.ToolExecutor for `review_patch`.
type Executor struct {
	ports Ports
}

// New returns an Executor reading and writing through ports.
func New(ports Ports) *Executor {
	return &Executor{ports: ports}
}

type arguments struct {
	PatchDigest string `json:"patchDigest"`
}

// result is the Tool's result; field order is the result's key order.
type result struct {
	PatchDigest       string    `json:"patchDigest"`
	ReviewModelID     string    `json:"reviewModelId"`
	Passed            bool      `json:"passed"`
	Blocking          bool      `json:"blocking"`
	FindingCount      int       `json:"findingCount"`
	BlockingFindings  []finding `json:"blockingFindings"`
	Summary           string    `json:"summary"`
	OpinionArtifactID string    `json:"opinionArtifactId"`
}

// Execute reviews the workspace's current patch with the call's review model.
func (e *Executor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	if e.ports.Models == nil || e.ports.Artifacts == nil || e.ports.Workspace == nil {
		return registry.ToolExecutionResult{}, toolError("the review is not configured in this process")
	}
	if action.Workspace == nil {
		return registry.ToolExecutionResult{}, toolError("the call carries no workspace view")
	}
	var args arguments
	if err := json.Unmarshal(action.Arguments, &args); err != nil || args.PatchDigest == "" {
		return registry.ToolExecutionResult{}, toolError("arguments carry no patchDigest")
	}
	// The Runtime refused a stale digest before this call; the check is repeated so this
	// Tool can never review a patch other than the workspace's current one.
	if args.PatchDigest != action.Workspace.PatchDigest {
		return registry.ToolExecutionResult{}, toolError("patchDigest is not the workspace's current patch")
	}
	if len(action.Workspace.Patch) > maxPatchBytes {
		return registry.ToolExecutionResult{}, toolError("the current patch exceeds the review bound")
	}
	modelID := action.ReviewModelID
	if modelID == "" {
		return registry.ToolExecutionResult{}, toolError("the call carries no review model; this review never uses a mock policy")
	}
	reg, provider, ok := e.ports.Models.Get(modelID)
	if !ok || !reg.HasCapability(domain.ModelCapabilityStructuredDecision) {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: review model %q is not registered with %s", ToolName, modelID, domain.ModelCapabilityStructuredDecision)
	}

	files, err := e.ports.Workspace.ReadFiles(ctx, action.Workspace, ContextNames)
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: read the repository context: %w", ToolName, err)
	}

	response, err := provider.Generate(ctx, registry.ModelRequest{
		ModelID:      modelID,
		Instructions: reviewInstructions,
		Messages: []registry.ModelMessage{{
			Role:  "user",
			Parts: []registry.ContentPart{{Type: registry.ContentPartText, Text: userMessage(*action.Workspace, files)}},
		}},
		FinalOutputSchema: json.RawMessage(opinionSchema),
		DecisionSchema:    registry.DecisionSchema(),
	})
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: review model %q: %w", ToolName, modelID, err)
	}
	opinion, raw, err := decodeOpinion(response.Decision)
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: review model %q: %w", ToolName, modelID, err)
	}

	ref, err := e.ports.Artifacts.WriteText(ctx, strings.NewReader(string(raw)))
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: store the opinion: %w", ToolName, err)
	}

	output, err := json.Marshal(result{
		PatchDigest:       args.PatchDigest,
		ReviewModelID:     modelID,
		Passed:            !opinion.Blocking,
		Blocking:          opinion.Blocking,
		FindingCount:      len(opinion.Findings),
		BlockingFindings:  blockingFindings(opinion.Findings),
		Summary:           truncateRunes(strings.TrimSpace(strings.ToValidUTF8(opinion.Summary, "")), maxSummaryRunes),
		OpinionArtifactID: ref.ArtifactID,
	})
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: encode result: %w", ToolName, err)
	}
	return registry.ToolExecutionResult{
		Kind: registry.ToolResultCompleted,
		Result: &registry.ToolResult{
			Output:    output,
			Artifacts: []domain.ArtifactRef{ref},
			Cost:      reviewCost(response.Cost, modelID),
		},
	}, nil
}

// reviewCost is the Cost the Runtime records for the review: the amount and currency the
// Adapter normalised, attributed to the frozen review Model ID the call was made with. No
// reported charge is no Cost, never a zero.
func reviewCost(reported *domain.Cost, reviewModelID string) *domain.Cost {
	if reported == nil {
		return nil
	}
	return &domain.Cost{Amount: reported.Amount, Currency: reported.Currency, Source: reviewModelID}
}

// toolError builds a failure from fixed labels only, so no argument, repository text or
// model answer can reach Trace or a log through it.
func toolError(reason string) error {
	return fmt.Errorf("%s: %w", ToolName, errors.New(reason))
}
