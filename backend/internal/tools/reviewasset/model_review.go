package reviewasset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// VisionPolicyVersion identifies the review instructions and verdict form a review model
// is asked with. A change to either is a new version, so a fact shows which rules a
// verdict answered.
const VisionPolicyVersion = "vision-review-policy-v2"

// reviewInstructions are the versioned instructions of VisionPolicyVersion.
const reviewInstructions = `You review one candidate image that an image model produced from an original photo.

You receive the original photo, the candidate image and a style requirement. Decide whether the candidate passes. It passes only when all three hold:
1. The candidate keeps the original photo's subject recognisable: the same people, animals or objects, with their identity, pose and overall composition preserved.
2. The candidate clearly meets the style requirement.
3. The candidate is of deliverable quality: no deformed anatomy or faces, no duplicated or missing limbs or features, and no visible generation artifacts, text garbage or broken geometry.

Answer FINAL with "passed" and "reasons". Give at most three short reasons, each under 200 characters. When the candidate fails, each reason names one concrete problem and what would fix it. When it passes, one reason may say what meets the requirement. Judge only what you see; do not call tools.`

// verdictSchema is the review model's FINAL output Schema. It stays inside the strict
// structured output subset, so the bounds on reasons are applied after the answer.
const verdictSchema = `{
  "type": "object",
  "properties": {
    "passed": {"type": "boolean"},
    "reasons": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["passed", "reasons"],
  "additionalProperties": false
}`

// verdict is the review model's decoded answer.
type verdict struct {
	Passed  bool     `json:"passed"`
	Reasons []string `json:"reasons"`
}

// modelReview asks the call's review model for a verdict and returns it with the charge
// the model's Adapter reported, nil when it reported none. It reads both images in full
// before the model call and fails without calling the model when either is unreadable or
// over the inline bound. Errors carry the Tool name, the review Model ID, the Asset or
// artifact id and a fixed label only, never image bytes, the style requirement or the
// model's text.
func (e Executor) modelReview(ctx context.Context, action registry.ToolAction, args arguments) (result, *domain.Cost, error) {
	modelID := action.ReviewModelID
	if e.ports.Models == nil || e.ports.Photos == nil || e.ports.Artifacts == nil || e.ports.MaxImageBytes <= 0 {
		return result{}, nil, fmt.Errorf("%s: review model %q: review with a model is not configured in this process", ToolName, modelID)
	}
	reg, provider, ok := e.ports.Models.Get(modelID)
	if !ok || !reg.HasCapability(domain.ModelCapabilityVisionInput) {
		return result{}, nil, fmt.Errorf("%s: review model %q is not registered with %s", ToolName, modelID, domain.ModelCapabilityVisionInput)
	}

	photo, err := e.ports.Photos.OpenPhoto(ctx, args.PhotoAssetID)
	if err != nil {
		return result{}, nil, fmt.Errorf("%s: photo %s could not be opened: %w", ToolName, args.PhotoAssetID, err)
	}
	photoImage, err := readImage(photo, e.ports.MaxImageBytes)
	if err != nil {
		return result{}, nil, fmt.Errorf("%s: photo %s: %w", ToolName, args.PhotoAssetID, err)
	}
	candidate, err := e.ports.Artifacts.OpenArtifact(ctx, action.RunID, args.AssetRef)
	if err != nil {
		return result{}, nil, fmt.Errorf("%s: candidate %s could not be opened: %w", ToolName, args.AssetRef, err)
	}
	candidateImage, err := readImage(candidate, e.ports.MaxImageBytes)
	if err != nil {
		return result{}, nil, fmt.Errorf("%s: candidate %s: %w", ToolName, args.AssetRef, err)
	}

	response, err := provider.Generate(ctx, registry.ModelRequest{
		ModelID:      modelID,
		Instructions: reviewInstructions,
		Messages: []registry.ModelMessage{{
			Role: "user",
			Parts: []registry.ContentPart{
				{Type: registry.ContentPartText, Text: "Original photo:"},
				{Type: registry.ContentPartImage, Image: photoImage},
				{Type: registry.ContentPartText, Text: "Candidate image:"},
				{Type: registry.ContentPartImage, Image: candidateImage},
				{Type: registry.ContentPartText, Text: "Style requirement:\n" + args.StyleRequirement},
			},
		}},
		FinalOutputSchema: json.RawMessage(verdictSchema),
		DecisionSchema:    registry.DecisionSchema(),
	})
	if err != nil {
		return result{}, nil, fmt.Errorf("%s: review model %q: %w", ToolName, modelID, err)
	}
	answer, err := decodeVerdict(response.Decision)
	if err != nil {
		return result{}, nil, fmt.Errorf("%s: review model %q: %w", ToolName, modelID, err)
	}
	return result{
		Passed:        answer.Passed,
		PolicyVersion: VisionPolicyVersion,
		Reasons:       boundReasons(answer.Reasons),
		ReviewModelID: modelID,
	}, reviewCost(response.Cost, modelID), nil
}

// reviewCost is the Cost the Runtime records for a review with a model: the amount and
// currency the Adapter normalised, attributed to the frozen review Model ID the call was
// made with, so the figure traces back to the model the Agent Run selected. No reported
// charge is no Cost, never a zero.
func reviewCost(reported *domain.Cost, reviewModelID string) *domain.Cost {
	if reported == nil {
		return nil
	}
	return &domain.Cost{Amount: reported.Amount, Currency: reported.Currency, Source: reviewModelID}
}

// readImage reads and closes one image, refusing a media type that is not a supported
// image and content over maxBytes rather than sending a truncated image.
func readImage(image Image, maxBytes int64) (*registry.InlineImage, error) {
	defer func() { _ = image.Content.Close() }()
	if !domain.IsSupportedAssetMediaType(image.MediaType) {
		return nil, errors.New("image media type is not supported for review")
	}
	content, err := io.ReadAll(io.LimitReader(image.Content, maxBytes+1))
	if err != nil {
		return nil, errors.New("image could not be read")
	}
	if int64(len(content)) > maxBytes {
		return nil, fmt.Errorf("image exceeds %d bytes and is not sent for review", maxBytes)
	}
	if len(content) == 0 {
		return nil, errors.New("image is empty")
	}
	return &registry.InlineImage{MediaType: image.MediaType, Data: content}, nil
}

// decodeVerdict accepts only a FINAL whose output satisfies the verdict Schema.
func decodeVerdict(decision registry.ModelDecision) (verdict, error) {
	if decision.Kind != registry.DecisionFinal {
		return verdict{}, errors.New("answer is not a FINAL verdict")
	}
	var value any
	if err := json.Unmarshal(decision.Output, &value); err != nil {
		return verdict{}, errors.New("verdict is not JSON")
	}
	schema, err := registry.CompileSchema(json.RawMessage(verdictSchema))
	if err != nil {
		return verdict{}, fmt.Errorf("compile review result schema: %w", err)
	}
	if err := registry.ValidateValue(schema, value); err != nil {
		return verdict{}, errors.New("verdict does not match the review result schema")
	}
	var answer verdict
	if err := json.Unmarshal(decision.Output, &answer); err != nil {
		return verdict{}, errors.New("verdict does not match the review result schema")
	}
	return answer, nil
}

// boundReasons keeps the first maxReasons non-blank reasons, each trimmed and truncated
// to maxReasonRunes runes.
func boundReasons(reasons []string) []string {
	out := make([]string, 0, maxReasons)
	for _, reason := range reasons {
		if len(out) == maxReasons {
			break
		}
		reason = strings.TrimSpace(strings.ToValidUTF8(reason, ""))
		if reason == "" {
			continue
		}
		if utf8.RuneCountInString(reason) > maxReasonRunes {
			reason = string([]rune(reason)[:maxReasonRunes])
		}
		out = append(out, reason)
	}
	return out
}
