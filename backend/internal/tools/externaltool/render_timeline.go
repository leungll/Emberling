package externaltool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// RenderTimelineToolName is the stable Name of the Tool that renders a checked timeline
// into a clip.
const RenderTimelineToolName = "render_timeline"

const renderTimelineInputSchema = `{
  "type": "object",
  "properties": {
    "timelineSha256": {"type": "string", "pattern": "` + sha256HexPattern + `"}
  },
  "required": ["timelineSha256"],
  "additionalProperties": false
}`

const renderTimelineOutputSchema = `{
  "type": "object",
  "properties": {
    "clipSha256": {"type": "string", "pattern": "` + sha256HexPattern + `"},
    "clipUrl": {"type": "string", "minLength": 1, "maxLength": 2048},
    "sizeBytes": {"type": "integer", "minimum": 1},
    "mediaType": {"type": "string", "minLength": 1, "maxLength": 128},
    "durationMs": {"type": "integer", "minimum": 0},
    "timelineSha256": {"type": "string", "pattern": "` + sha256HexPattern + `"}
  },
  "required": ["clipSha256", "clipUrl", "sizeBytes", "mediaType", "durationMs", "timelineSha256"],
  "additionalProperties": false
}`

// RenderTimelineSpec declares `render_timeline`. Its only argument is a timeline digest,
// never timeline content, so the Agent can render nothing but a timeline the service has
// already canonicalised. The Tool requires a media_checked fact about that digest whose
// verdict is true: a timeline that was never checked, or whose newest check did not pass,
// is refused before any Attempt exists and handed back to the Agent as a missing
// verification, with no call to the service. The clip itself stays with the service;
// the result keeps its link, digest, size, media type and duration, and the recorded
// clip_rendered fact is about the clip digest and binds the timeline it came from.
func RenderTimelineSpec() Spec {
	requirePassed := true
	return Spec{
		Metadata: domain.ToolMetadata{
			Name:         RenderTimelineToolName,
			Description:  "Render the clip of a timeline that passed its checks, named by the timeline digest a check returned",
			InputSchema:  json.RawMessage(renderTimelineInputSchema),
			OutputSchema: json.RawMessage(renderTimelineOutputSchema),
			Poll:         &domain.PollPolicy{IntervalMs: mediaPollIntervalMs, MaxPolls: mediaPollMaxPolls},
			Produces: &domain.FactProduction{
				FactType:       FactTypeClipRendered,
				SubjectPointer: domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/clipSha256"},
				BindArguments: map[string]domain.FactPointer{
					"timelineSha256": {Source: domain.FactPointerArguments, Pointer: "/timelineSha256"},
				},
			},
			Requires: []domain.FactRequirement{{
				FactType:        FactTypeMediaChecked,
				SubjectArgument: "/timelineSha256",
				MatchBindings:   []string{},
				RequireVerdict:  &requirePassed,
			}},
			ReturnUnmetRequirement: true,
			Records:                []domain.ToolRecordKind{},
			CountedFailedFacts:     mediaCountedFailedFacts(),
		},
		TaskType:  TaskTypeRender,
		MapInput:  renderTimelineInput,
		MapResult: renderTimelineResult,
	}
}

// renderTimelineInput sends the timeline digest and nothing else.
func renderTimelineInput(action registry.ToolAction) (json.RawMessage, error) {
	var args struct {
		TimelineSha256 string `json:"timelineSha256"`
	}
	if err := json.Unmarshal(action.Arguments, &args); err != nil {
		return nil, errors.New("arguments are not a JSON object")
	}
	if args.TimelineSha256 == "" {
		return nil, errors.New("the call names no timeline digest")
	}
	return json.Marshal(args)
}

// renderResult is the result object of a SUCCEEDED render task. Members a result may lack
// are pointers, so an incomplete result is passed on as-is for the OutputSchema to reject
// with its Trace rather than this Tool inventing a value.
type renderResult struct {
	ClipSha256     string `json:"clipSha256"`
	ClipURL        string `json:"clipUrl"`
	SizeBytes      *int64 `json:"sizeBytes"`
	MediaType      string `json:"mediaType"`
	DurationMs     *int64 `json:"durationMs"`
	TimelineSha256 string `json:"timelineSha256"`
}

// renderTimelineResult keeps exactly the members the Tool result declares.
func renderTimelineResult(_ context.Context, raw json.RawMessage) (registry.ToolResult, error) {
	var result renderResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return registry.ToolResult{}, errors.New("result is not a JSON object")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return registry.ToolResult{}, fmt.Errorf("encode result: %w", err)
	}
	return registry.ToolResult{Output: encoded}, nil
}
