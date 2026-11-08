package externaltool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// RunChecksToolName is the stable Name of the Tool that runs the technical checks of the
// media rules on a timeline or on a rendered clip.
const RunChecksToolName = "run_checks"

// The two kinds of subject a check is about. They select the task type and name the
// recorded fact's binding.
const (
	CheckKindTimeline = "TIMELINE"
	CheckKindClip     = "CLIP"
)

// A check result is bounded before it reaches the Tool Attempt, the fact and the Agent's
// context: at most maxFindings findings, each with a code of at most maxFindingCodeBytes
// and a message of at most maxFindingMessageBytes. Findings past the cap are dropped in
// the service's order, so a bounded result keeps the first ones the service reported.
const (
	maxFindings            = 20
	maxFindingCodeBytes    = 64
	maxFindingMessageBytes = 512
)

// maxTimelineSegments bounds how many sentence ranges one timeline check accepts, as the
// schema text spells it.
const maxTimelineSegments = "200"

// Failure codes this Tool reports when the service's SUCCEEDED result cannot establish a
// timeline fact: the canonical timeline is missing, or its digest is not the one the
// result names. Either way no Artifact and no fact are written, and the Attempt fails.
const (
	failureTimelineIncomplete     = "TIMELINE_RESULT_INCOMPLETE"
	failureTimelineDigestMismatch = "TIMELINE_DIGEST_MISMATCH"
	// failureTimelineTooLarge is the code of a timeline the artifact store refuses.
	failureTimelineTooLarge = "TIMELINE_TOO_LARGE"
)

const runChecksInputSchema = `{
  "type": "object",
  "oneOf": [
    {
      "type": "object",
      "properties": {
        "kind": {"const": "TIMELINE"},
        "timeline": {
          "type": "object",
          "properties": {
            "sourceId": {"type": "string", "minLength": 1, "maxLength": 128},
            "transcriptSha256": {"type": "string", "pattern": "` + sha256HexPattern + `"},
            "targetDurationMs": {"type": ["integer", "null"], "minimum": 1},
            "segments": {
              "type": "array",
              "minItems": 1,
              "maxItems": ` + maxTimelineSegments + `,
              "items": {
                "type": "object",
                "properties": {
                  "from": {"type": "string", "minLength": 1, "maxLength": 32},
                  "to": {"type": "string", "minLength": 1, "maxLength": 32}
                },
                "required": ["from", "to"],
                "additionalProperties": false
              }
            }
          },
          "required": ["sourceId", "transcriptSha256", "segments"],
          "additionalProperties": false
        }
      },
      "required": ["kind", "timeline"],
      "additionalProperties": false
    },
    {
      "type": "object",
      "properties": {
        "kind": {"const": "CLIP"},
        "clipSha256": {"type": "string", "pattern": "` + sha256HexPattern + `"}
      },
      "required": ["kind", "clipSha256"],
      "additionalProperties": false
    }
  ]
}`

const runChecksOutputSchema = `{
  "type": "object",
  "properties": {
    "kind": {"type": "string", "enum": ["TIMELINE", "CLIP"]},
    "passed": {"type": "boolean"},
    "ruleVersion": {"type": "string", "minLength": 1, "maxLength": 32},
    "subjectSha256": {"type": "string", "pattern": "` + sha256HexPattern + `"},
    "findings": {
      "type": "array",
      "maxItems": 20,
      "items": {
        "type": "object",
        "properties": {
          "code": {"type": "string", "minLength": 1, "maxLength": 64},
          "level": {"type": "string", "enum": ["BLOCKING", "WARNING"]},
          "message": {"type": "string", "maxLength": 512},
          "segmentIndex": {"type": "integer", "minimum": 0}
        },
        "required": ["code", "level", "message"],
        "additionalProperties": false
      }
    },
    "timelineArtifactId": {"type": "string", "minLength": 1},
    "timelineSha256": {"type": "string", "pattern": "` + sha256HexPattern + `"},
    "durationMs": {"type": "integer", "minimum": 0}
  },
  "required": ["kind", "passed", "ruleVersion", "subjectSha256", "findings"],
  "additionalProperties": false
}`

// RunChecksSpec declares `run_checks`. One call checks either a timeline, given as sentence
// ranges over a transcript, or a rendered clip, given by its digest; the kind selects the
// task type. A check that does not pass is a successful call whose result says so.
//
// A timeline check establishes the identity every later step uses: the service computes
// the canonical timeline and its digest, and this Tool keeps that canonical text verbatim
// as a text Execution Artifact, so the Artifact's content digest is the timeline digest
// and the Agent can read the timeline back. The recorded media_checked fact is about the
// checked digest, binds the kind and the rule version, and carries the verdict; it is what
// lets a render be refused until the timeline it names has passed.
func RunChecksSpec(artifacts ArtifactTextStore) Spec {
	return Spec{
		Metadata: domain.ToolMetadata{
			Name:  RunChecksToolName,
			Title: "Run checks",
			Description: "Run the technical checks on a timeline (sentence ranges over the transcript) or on a rendered clip; " +
				"a timeline check returns the timeline digest that a render must name",
			InputSchema:  json.RawMessage(runChecksInputSchema),
			OutputSchema: json.RawMessage(runChecksOutputSchema),
			Poll:         &domain.PollPolicy{IntervalMs: mediaPollIntervalMs, MaxPolls: mediaPollMaxPolls},
			Produces: &domain.FactProduction{
				FactType:       FactTypeMediaChecked,
				SubjectPointer: domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/subjectSha256"},
				BindArguments: map[string]domain.FactPointer{
					"kind":        {Source: domain.FactPointerResult, Pointer: "/kind"},
					"ruleVersion": {Source: domain.FactPointerResult, Pointer: "/ruleVersion"},
				},
				VerdictPointer: &domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/passed"},
			},
			Requires:           []domain.FactRequirement{},
			Records:            []domain.ToolRecordKind{},
			CountedFailedFacts: mediaCountedFailedFacts(),
			Display: []domain.ToolDisplayField{
				{Source: domain.FactPointerArguments, Pointer: "/kind", Title: "Kind"},
				{Source: domain.FactPointerResult, Pointer: "/passed", Title: "Passed"},
				{Source: domain.FactPointerResult, Pointer: "/ruleVersion", Title: "Rule version"},
				{Source: domain.FactPointerResult, Pointer: "/durationMs", Title: "Duration (ms)"},
			},
		},
		TaskTypeFor: runChecksTaskType,
		MapInput:    runChecksInput,
		MapResult: func(ctx context.Context, result json.RawMessage) (registry.ToolResult, error) {
			return runChecksResult(ctx, artifacts, result)
		},
	}
}

// runChecksArguments is the Tool's view of its arguments after they passed the schema.
type runChecksArguments struct {
	Kind       string            `json:"kind"`
	Timeline   *timelineArgument `json:"timeline"`
	ClipSha256 string            `json:"clipSha256"`
}

// timelineArgument is the timeline as the Agent submits it: sentence ranges, not times.
// TargetDurationMs is sent as null when the Agent names none, so the service applies its
// default range.
type timelineArgument struct {
	SourceID         string            `json:"sourceId"`
	TranscriptSha256 string            `json:"transcriptSha256"`
	TargetDurationMs *int64            `json:"targetDurationMs"`
	Segments         []timelineSegment `json:"segments"`
}

type timelineSegment struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func decodeRunChecksArguments(action registry.ToolAction) (runChecksArguments, error) {
	var args runChecksArguments
	if err := json.Unmarshal(action.Arguments, &args); err != nil {
		return runChecksArguments{}, errors.New("arguments are not a JSON object")
	}
	switch args.Kind {
	case CheckKindTimeline:
		if args.Timeline == nil {
			return runChecksArguments{}, errors.New("a TIMELINE check names no timeline")
		}
	case CheckKindClip:
		if args.ClipSha256 == "" {
			return runChecksArguments{}, errors.New("a CLIP check names no clip digest")
		}
	default:
		return runChecksArguments{}, fmt.Errorf("kind %q is not %s or %s", args.Kind, CheckKindTimeline, CheckKindClip)
	}
	return args, nil
}

// runChecksTaskType selects the task type from the kind of subject being checked.
func runChecksTaskType(action registry.ToolAction) (string, error) {
	args, err := decodeRunChecksArguments(action)
	if err != nil {
		return "", err
	}
	if args.Kind == CheckKindTimeline {
		return TaskTypeCheckTimeline, nil
	}
	return TaskTypeCheckClip, nil
}

// runChecksInput builds the task input: the timeline as submitted for a timeline check,
// the clip digest for a clip check. The kind itself is the task type and is not repeated.
func runChecksInput(action registry.ToolAction) (json.RawMessage, error) {
	args, err := decodeRunChecksArguments(action)
	if err != nil {
		return nil, err
	}
	if args.Kind == CheckKindTimeline {
		return json.Marshal(args.Timeline)
	}
	return json.Marshal(struct {
		ClipSha256 string `json:"clipSha256"`
	}{ClipSha256: args.ClipSha256})
}

// mediaCheckResult is the result object of a SUCCEEDED check task. Members a result may
// lack are pointers, so an incomplete result is passed on as-is and the OutputSchema
// rejects it with its Trace rather than this Tool inventing a value.
type mediaCheckResult struct {
	Kind           string         `json:"kind"`
	Passed         *bool          `json:"passed"`
	RuleVersion    string         `json:"ruleVersion"`
	SubjectSha256  string         `json:"subjectSha256"`
	TimelineSha256 string         `json:"timelineSha256"`
	DurationMs     *int64         `json:"durationMs"`
	Timeline       *string        `json:"timeline"`
	Findings       []checkFinding `json:"findings"`
}

type checkFinding struct {
	Code         string `json:"code"`
	Level        string `json:"level"`
	Message      string `json:"message"`
	SegmentIndex *int   `json:"segmentIndex,omitempty"`
}

// runChecksOutput is the Tool result. The timeline members are present only for a
// timeline check.
type runChecksOutput struct {
	Kind               string         `json:"kind"`
	Passed             *bool          `json:"passed"`
	RuleVersion        string         `json:"ruleVersion"`
	SubjectSha256      string         `json:"subjectSha256"`
	Findings           []checkFinding `json:"findings"`
	TimelineArtifactID string         `json:"timelineArtifactId,omitempty"`
	TimelineSha256     string         `json:"timelineSha256,omitempty"`
	DurationMs         *int64         `json:"durationMs,omitempty"`
}

// runChecksResult maps the service's result to the Tool result. For a timeline check it
// first keeps the canonical timeline as a text Artifact whose content digest must be the
// digest the result names; a result whose timeline is missing or whose digest does not
// match fails the Tool call, since it could establish no identity a render may trust.
func runChecksResult(ctx context.Context, artifacts ArtifactTextStore, raw json.RawMessage) (registry.ToolResult, error) {
	var result mediaCheckResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return registry.ToolResult{}, errors.New("result is not a JSON object")
	}
	output := runChecksOutput{
		Kind:          result.Kind,
		Passed:        result.Passed,
		RuleVersion:   result.RuleVersion,
		SubjectSha256: result.SubjectSha256,
		Findings:      boundedFindings(result.Findings),
	}
	var refs []domain.ArtifactRef
	if result.Kind == CheckKindTimeline {
		ref, err := keepTimeline(ctx, artifacts, result)
		if err != nil {
			return registry.ToolResult{}, err
		}
		refs = []domain.ArtifactRef{ref}
		output.TimelineArtifactID = ref.ArtifactID
		output.TimelineSha256 = result.TimelineSha256
		output.DurationMs = result.DurationMs
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return registry.ToolResult{}, fmt.Errorf("encode result: %w", err)
	}
	return registry.ToolResult{Output: encoded, Artifacts: refs}, nil
}

// keepTimeline writes the canonical timeline text verbatim as an Artifact and proves the
// stored content's digest is the one the result names. The digest is compared before the
// write, so a mismatching timeline never reaches storage, and again on the store's answer,
// so the declared Artifact is the one the result names. The result must also be about that
// digest: the recorded fact's subject is what a render names, so a result whose subject is
// another value would guard nothing.
func keepTimeline(ctx context.Context, artifacts ArtifactTextStore, result mediaCheckResult) (domain.ArtifactRef, error) {
	if result.Timeline == nil || *result.Timeline == "" || result.TimelineSha256 == "" {
		return domain.ArtifactRef{}, &registry.ProviderFailure{Err: domain.ExecutionError{
			Code:    failureTimelineIncomplete,
			Message: "the timeline check result carries no canonical timeline or no timeline digest",
		}}
	}
	sum := sha256.Sum256([]byte(*result.Timeline))
	if digest := hex.EncodeToString(sum[:]); digest != result.TimelineSha256 || result.SubjectSha256 != result.TimelineSha256 {
		return domain.ArtifactRef{}, &registry.ProviderFailure{Err: domain.ExecutionError{
			Code:    failureTimelineDigestMismatch,
			Message: "the canonical timeline's content digest is not the timeline digest the result names",
		}}
	}
	if artifacts == nil {
		return domain.ArtifactRef{}, errors.New("artifact content storage is not configured")
	}
	ref, err := artifacts.WriteText(ctx, strings.NewReader(*result.Timeline))
	if errors.Is(err, domain.ErrArtifactTooLarge) || errors.Is(err, domain.ErrUnsupportedAssetMediaType) {
		// A timeline the store refuses by its content or type is refused the same way on
		// every later delivery and poll, so waiting for another delivery would only run
		// the poll budget down without a Trace; the Attempt fails with the reason instead.
		return domain.ArtifactRef{}, &registry.ProviderFailure{Err: domain.ExecutionError{
			Code:    failureTimelineTooLarge,
			Message: "the canonical timeline cannot be kept as an artifact: it exceeds the size limit or has an unsupported type",
		}}
	}
	if err != nil {
		// The store's error names no path; the timeline is not quoted either.
		return domain.ArtifactRef{}, fmt.Errorf("store timeline artifact: %w", err)
	}
	if ref.SHA256 != result.TimelineSha256 {
		return domain.ArtifactRef{}, &registry.ProviderFailure{Err: domain.ExecutionError{
			Code:    failureTimelineDigestMismatch,
			Message: "the stored timeline artifact's digest is not the timeline digest the result names",
		}}
	}
	return ref, nil
}

// boundedFindings keeps at most maxFindings findings with bounded code and message text,
// and always returns a list, so the result carries [] rather than null.
func boundedFindings(findings []checkFinding) []checkFinding {
	if len(findings) > maxFindings {
		findings = findings[:maxFindings]
	}
	bounded := make([]checkFinding, 0, len(findings))
	for _, finding := range findings {
		finding.Code = truncateUTF8(finding.Code, maxFindingCodeBytes)
		finding.Message = truncateUTF8(finding.Message, maxFindingMessageBytes)
		bounded = append(bounded, finding)
	}
	return bounded
}
