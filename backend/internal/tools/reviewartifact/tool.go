// Package reviewartifact implements the `review_artifact` Tool: a semantic review of a
// timeline by the review model the Agent node selected, which must be a model other than
// the Agent's own decision model.
//
// The Agent names the timeline and the transcript it was cut from, both text Execution
// Artifacts of its own Run, and copies the user's requirement. The Tool reads both, checks
// that the timeline really was computed over that transcript by its digest, and sends the
// selected sentences with two sentences of context on each side to the review model under
// a versioned policy. The model answers two questions: does the selection satisfy the
// requirement, and does any selected sentence standing alone distort the original
// meaning. A review that does not pass is a successful call whose result says so with the
// issues, so the Agent can revise; a completed review records a `content_reviewed` fact
// about the timeline digest, bound to the policy version, whose verdict is the answer.
//
// The first version reviews timelines only. The Tool performs exactly one registered
// operation, one model call, and has no other side effect. It owns no retry, timeout,
// state transition or Event, never selects a replacement model, and never falls back to a
// mock policy when the frozen review model cannot serve. Its errors carry the Tool name,
// the review Model ID, artifact ids and fixed labels only: never the transcript, the
// timeline or the model's answer.
package reviewartifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// ToolName is this Tool's stable Registry name, stored in an Agent's Tool allowlist.
const ToolName = "review_artifact"

// FactType is the fact a completed review records: the timeline, identified by its
// digest, was reviewed under the bound policy version, with the verdict it got.
const FactType = "content_reviewed"

// Failure codes. A synchronous Tool fails the Attempt with its error, so each code is a
// fixed label in the error text that Trace keeps and a test can match; none of them
// quotes material.
const (
	// FailureArtifactNotFound: an artifact does not exist, is not linked to this Run or
	// is not text. Both ids come from committed results the Agent already holds, and a
	// successful result must carry the timeline digest the fact is about, so there is no
	// result to return without the artifacts.
	FailureArtifactNotFound = "ARTIFACT_NOT_FOUND"
	// FailureInputTooLarge: the timeline, the transcript or the excerpt exceeds its bound.
	FailureInputTooLarge = "REVIEW_INPUT_TOO_LARGE"
	// FailureTimelineInvalid: the timeline is not a canonical timeline, or names
	// sentences the transcript does not have in that order.
	FailureTimelineInvalid = "TIMELINE_INVALID"
	// FailureTranscriptInvalid: the transcript is not sentence-per-line material.
	FailureTranscriptInvalid = "TRANSCRIPT_INVALID"
	// FailureTranscriptMismatch: the timeline names a transcript digest that is not the
	// transcript artifact's content digest, so the two do not belong together.
	FailureTranscriptMismatch = "TRANSCRIPT_DIGEST_MISMATCH"
	// FailureOpinionInvalid: the review model's answer is not a usable opinion.
	FailureOpinionInvalid = "REVIEW_OPINION_INVALID"
)

const inputSchema = `{
  "type": "object",
  "properties": {
    "timelineArtifactId": {"type": "string", "minLength": 1, "maxLength": 128, "description": "The timeline artifact a passed timeline check returned."},
    "transcriptArtifactId": {"type": "string", "minLength": 1, "maxLength": 128, "description": "The transcript artifact the timeline was cut from."},
    "requirement": {"type": "string", "minLength": 1, "maxLength": 2000, "description": "The user's requirement for the clip, copied verbatim."}
  },
  "required": ["timelineArtifactId", "transcriptArtifactId", "requirement"],
  "additionalProperties": false
}`

const outputSchema = `{
  "type": "object",
  "properties": {
    "passed": {"type": "boolean"},
    "policyVersion": {"type": "string", "minLength": 1},
    "timelineSha256": {"type": "string", "pattern": "^[0-9a-f]{64}$"},
    "issues": {
      "type": "array",
      "maxItems": 3,
      "items": {
        "type": "object",
        "properties": {
          "type": {"type": "string", "enum": ["REQUIREMENT_NOT_MET", "MEANING_DISTORTED"]},
          "sentences": {"type": "array", "maxItems": 20, "items": {"type": "string", "maxLength": 32}},
          "reason": {"type": "string", "maxLength": 300}
        },
        "required": ["type", "sentences", "reason"],
        "additionalProperties": false
      }
    }
  },
  "required": ["passed", "policyVersion", "timelineSha256", "issues"],
  "additionalProperties": false
}`

// errTooLarge reports material past a read bound. It never carries the bytes.
var errTooLarge = errors.New("material exceeds the review bound")

// Registration returns the `review_artifact` ToolRegistration reading through ports.
//
// A review only reads committed artifacts and asks a model, so it has no side effect and
// is safe to repeat. It requires an independent review model declaring structured
// decision output: the node must select one, different from its decision model, and the
// Tool never reviews with a mock policy. The fact it produces is about the timeline
// digest at /timelineSha256, bound to the policy version, with the verdict at /passed;
// its failed verdicts are counted against the Agent Run's revision bound for this fact
// type, so the Runtime refuses further reviews once the bound is passed.
func Registration(ports Ports) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:  ToolName,
			Title: "Review artifact",
			Description: "Review a timeline's selected sentences, with their context in the transcript, against the user's requirement " +
				"with the node's independent review model; a review that does not pass returns its issues",
			InputSchema:   json.RawMessage(inputSchema),
			OutputSchema:  json.RawMessage(outputSchema),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			ExecutionKind: domain.ToolExecutionSync,
			Produces: &domain.FactProduction{
				FactType:       FactType,
				SubjectPointer: domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/timelineSha256"},
				BindArguments: map[string]domain.FactPointer{
					"policyVersion": {Source: domain.FactPointerResult, Pointer: "/policyVersion"},
				},
				VerdictPointer: &domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/passed"},
			},
			Requires:                    []domain.FactRequirement{},
			Records:                     []domain.ToolRecordKind{},
			CountsTowardGenerationLimit: false,
			UsesReviewModel:             true,
			ReviewModelCapability:       domain.ModelCapabilityStructuredDecision,
			RequiresReviewModel:         true,
			CountedFailedFacts:          []string{FactType},
		},
		Executor: New(ports),
	}
}

// Executor implements registry.ToolExecutor for `review_artifact`.
type Executor struct {
	ports Ports
}

// New returns an Executor reading through ports.
func New(ports Ports) *Executor {
	return &Executor{ports: ports}
}

type arguments struct {
	TimelineArtifactID   string `json:"timelineArtifactId"`
	TranscriptArtifactID string `json:"transcriptArtifactId"`
	Requirement          string `json:"requirement"`
}

// result is the Tool's result; field order is the result's key order.
type result struct {
	Passed         bool    `json:"passed"`
	PolicyVersion  string  `json:"policyVersion"`
	TimelineSha256 string  `json:"timelineSha256"`
	Issues         []issue `json:"issues"`
}

// Execute reviews the named timeline with the call's review model.
func (e *Executor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	if e.ports.Models == nil || e.ports.Materials == nil {
		return registry.ToolExecutionResult{}, toolError("the review is not configured in this process")
	}
	var args arguments
	if err := json.Unmarshal(action.Arguments, &args); err != nil ||
		args.TimelineArtifactID == "" || args.TranscriptArtifactID == "" || args.Requirement == "" {
		return registry.ToolExecutionResult{}, toolError("arguments carry no timelineArtifactId, transcriptArtifactId and requirement")
	}
	if utf8.RuneCountInString(args.Requirement) > maxRequirementRunes {
		return registry.ToolExecutionResult{}, toolError("the requirement exceeds the review bound")
	}
	modelID := action.ReviewModelID
	if modelID == "" {
		return registry.ToolExecutionResult{}, toolError("the call carries no review model; this review never uses a mock policy")
	}
	reg, provider, ok := e.ports.Models.Get(modelID)
	if !ok || !reg.HasCapability(domain.ModelCapabilityStructuredDecision) {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: review model %q is not registered with %s", ToolName, modelID, domain.ModelCapabilityStructuredDecision)
	}

	timelineText, timelineSha, err := e.readMaterial(ctx, action.RunID, args.TimelineArtifactID, maxTimelineBytes)
	if err != nil {
		return registry.ToolExecutionResult{}, err
	}
	tl, err := decodeTimeline(timelineText)
	if err != nil {
		return registry.ToolExecutionResult{}, failure(FailureTimelineInvalid, args.TimelineArtifactID, err)
	}
	transcriptText, transcriptSha, err := e.readMaterial(ctx, action.RunID, args.TranscriptArtifactID, maxTranscriptBytes)
	if err != nil {
		return registry.ToolExecutionResult{}, err
	}
	// The timeline check computed the timeline over one transcript and wrote that
	// transcript's digest into it; a review over any other transcript would judge
	// sentences the clip does not contain.
	if tl.TranscriptSha256 != transcriptSha {
		return registry.ToolExecutionResult{}, failure(FailureTranscriptMismatch, args.TimelineArtifactID,
			errors.New("the timeline's transcript digest is not the transcript artifact's content digest"))
	}
	sentences, err := decodeTranscript(transcriptText)
	if errors.Is(err, errTooLarge) {
		return registry.ToolExecutionResult{}, failure(FailureInputTooLarge, args.TranscriptArtifactID, err)
	}
	if err != nil {
		return registry.ToolExecutionResult{}, failure(FailureTranscriptInvalid, args.TranscriptArtifactID, err)
	}
	lines, err := excerpt(sentences, tl.Segments)
	if err != nil {
		return registry.ToolExecutionResult{}, failure(FailureTimelineInvalid, args.TimelineArtifactID, err)
	}
	message := userMessage(args.Requirement, tl.Segments, lines)
	if len(message) > maxExcerptBytes {
		return registry.ToolExecutionResult{}, failure(FailureInputTooLarge, args.TimelineArtifactID, errors.New("the excerpt exceeds the review bound"))
	}

	response, err := provider.Generate(ctx, registry.ModelRequest{
		ModelID:      modelID,
		Instructions: reviewInstructions,
		Messages: []registry.ModelMessage{{
			Role:  "user",
			Parts: []registry.ContentPart{{Type: registry.ContentPartText, Text: message}},
		}},
		FinalOutputSchema: json.RawMessage(opinionSchema),
		DecisionSchema:    registry.DecisionSchema(),
	})
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: review model %q: %w", ToolName, modelID, err)
	}
	answer, err := decodeOpinion(response.Decision)
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: review model %q: %s: %w", ToolName, modelID, FailureOpinionInvalid, err)
	}

	output, err := json.Marshal(result{
		Passed:         answer.Passed,
		PolicyVersion:  PolicyVersion,
		TimelineSha256: timelineSha,
		Issues:         boundedIssues(answer.Issues),
	})
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: encode result: %w", ToolName, err)
	}
	return registry.ToolExecutionResult{
		Kind:   registry.ToolResultCompleted,
		Result: &registry.ToolResult{Output: output, Cost: reviewCost(response.Cost, modelID)},
	}, nil
}

// readMaterial opens one text artifact of the Run and returns its content within limit
// and the content's digest, computed from the bytes read so the result names what was
// actually reviewed. An artifact the Run cannot read as text is ARTIFACT_NOT_FOUND;
// content past the bound is REVIEW_INPUT_TOO_LARGE; any other read failure is the
// Backend's and is passed on without the material. So is content whose digest differs
// from the one committed with the artifact: corrupt storage is never reported as a
// mismatch the Agent could correct.
func (e *Executor) readMaterial(ctx context.Context, runID, artifactID string, limit int) ([]byte, string, error) {
	artifact, content, err := e.ports.Materials.OpenText(ctx, runID, artifactID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, "", failure(FailureArtifactNotFound, artifactID, errors.New("the artifact is not a text artifact of this run"))
	}
	if err != nil {
		return nil, "", fmt.Errorf("%s: artifact %s could not be opened: %w", ToolName, artifactID, err)
	}
	defer func() { _ = content.Close() }()
	if artifact.MediaType != domain.ArtifactMediaTypeTextPlain {
		return nil, "", failure(FailureArtifactNotFound, artifactID, errors.New("the artifact is not a text artifact of this run"))
	}
	data, err := readBounded(content, limit)
	if errors.Is(err, errTooLarge) {
		return nil, "", failure(FailureInputTooLarge, artifactID, err)
	}
	if err != nil {
		return nil, "", fmt.Errorf("%s: artifact %s could not be read: %w", ToolName, artifactID, err)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if digest != artifact.SHA256 {
		return nil, "", fmt.Errorf("%s: artifact %s: stored content does not match its committed digest", ToolName, artifactID)
	}
	return data, digest, nil
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

// failure builds a Tool failure from a stable code, the artifact it concerns and a
// reason made of fixed labels, so no material can reach Trace or a log through it.
func failure(code, artifactID string, reason error) error {
	return fmt.Errorf("%s: %s: artifact %s: %w", ToolName, code, artifactID, reason)
}

// toolError builds a failure from fixed labels only.
func toolError(reason string) error {
	return fmt.Errorf("%s: %w", ToolName, errors.New(reason))
}
