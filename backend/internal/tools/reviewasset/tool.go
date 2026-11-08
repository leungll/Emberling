// Package reviewasset implements the `review_asset` Tool: a side-effect free review of a
// generated image asset against its original photo and a style requirement.
//
// When the Agent Run froze a review model, the verdict and its reasons come from one call
// to that vision model, which sees the original photo, the candidate image and the style
// requirement. When it froze none, the deterministic mock policy decides: the verdict is a
// pure function of the asset reference (about two of three references pass), unless the
// call carries the optional mock control that forces a verdict for a test or a demo.
//
// It performs exactly one registered operation. It owns no retry, timeout, state
// transition or Event, and it never substitutes one policy or model for another.
package reviewasset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// ToolName is this Tool's stable Registry name, stored in an Agent's Tool allowlist.
const ToolName = "review_asset"

// FactType is the execution fact a review establishes about the reviewed asset.
const FactType = "asset_reviewed"

// generatedFactType is the fact a reviewed asset must already carry: only an asset this
// Run generated, from the same photo, can be reviewed.
const generatedFactType = "image_generated"

// PolicyVersion identifies the deterministic mock review rules. It is recorded with the
// fact so a later change of rules can be told apart from an earlier verdict.
const PolicyVersion = "mock-review-policy-v1"

// MockReviewModelID is the reserved reviewModelId a result reports when the mock policy,
// not a model, decided it.
const MockReviewModelID = "mock"

// Mock control values for the optional `mock` argument.
const (
	mockPass = "pass"
	mockFail = "fail"
)

// rejectionReason is the one fixed reason a failed mock review reports.
const rejectionReason = "asset does not meet the mock review policy"

const inputSchema = `{
  "type": "object",
  "properties": {
    "assetRef": {"type": "string", "minLength": 1},
    "photoAssetId": {"type": "string", "minLength": 1},
    "styleRequirement": {"type": "string", "minLength": 1, "maxLength": 500},
    "mock": {"type": "string", "enum": ["pass", "fail"]}
  },
  "required": ["assetRef", "photoAssetId", "styleRequirement"],
  "additionalProperties": false
}`

// Result bounds. A model's reasons beyond them are dropped or truncated, never rejected.
const (
	maxReasons     = 3
	maxReasonRunes = 200
)

const outputSchema = `{
  "type": "object",
  "properties": {
    "passed": {"type": "boolean"},
    "policyVersion": {"type": "string", "minLength": 1},
    "reasons": {"type": "array", "maxItems": 3, "items": {"type": "string", "maxLength": 200}},
    "reviewModelId": {"type": "string", "minLength": 1},
    "styleDigest": {"type": "string", "minLength": 1}
  },
  "required": ["passed", "policyVersion", "reasons", "reviewModelId", "styleDigest"],
  "additionalProperties": false
}`

// Registration returns the `review_asset` ToolRegistration. ports serve a review with a
// model; the mock policy needs none of them.
//
// A review only reads, so it has no side effect and is safe to repeat. Its result
// establishes an `asset_reviewed` fact about the reviewed assetRef, bound to the source
// photo, the policy version, the review model and the style requirement's digest, whose
// verdict is the `passed` member and whose basis is the asset's `image_generated` fact.
// The call itself requires that `image_generated` fact for the same assetRef and photo, so
// an asset from another photo, or one this Run never generated, cannot be reviewed into a
// passing verdict.
//
// The Tool declares UsesReviewModel with the vision input capability, so each call carries
// the Agent Run's frozen review Model ID, which declares vision input, or none when the
// Agent node selected no review model.
func Registration(ports Ports) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          ToolName,
			Title:         "Review image",
			Description:   "Review a generated image asset against its original photo and a style requirement, with the Agent's review model or the mock review policy",
			InputSchema:   json.RawMessage(inputSchema),
			OutputSchema:  json.RawMessage(outputSchema),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			ExecutionKind: domain.ToolExecutionSync,
			Produces: &domain.FactProduction{
				FactType:       FactType,
				SubjectPointer: domain.FactPointer{Source: domain.FactPointerArguments, Pointer: "/assetRef"},
				BindArguments: map[string]domain.FactPointer{
					"photoAssetId":  {Source: domain.FactPointerArguments, Pointer: "/photoAssetId"},
					"policyVersion": {Source: domain.FactPointerResult, Pointer: "/policyVersion"},
					"reviewModelId": {Source: domain.FactPointerResult, Pointer: "/reviewModelId"},
					"styleDigest":   {Source: domain.FactPointerResult, Pointer: "/styleDigest"},
				},
				VerdictPointer: &domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/passed"},
				BasisFactType:  generatedFactType,
			},
			Requires: []domain.FactRequirement{{
				FactType:        generatedFactType,
				SubjectArgument: "/assetRef",
				MatchBindings:   []string{"photoAssetId"},
			}},
			UsesReviewModel:       true,
			ReviewModelCapability: domain.ModelCapabilityVisionInput,
			Display: []domain.ToolDisplayField{
				{Source: domain.FactPointerArguments, Pointer: "/styleRequirement", Title: "Style requirement"},
				{Source: domain.FactPointerResult, Pointer: "/passed", Title: "Passed"},
				{Source: domain.FactPointerResult, Pointer: "/reasons", Title: "Reasons"},
			},
		},
		Executor: Executor{ports: ports},
	}
}

// Executor implements registry.ToolExecutor for `review_asset`.
type Executor struct {
	ports Ports
}

type arguments struct {
	AssetRef         string `json:"assetRef"`
	PhotoAssetID     string `json:"photoAssetId"`
	StyleRequirement string `json:"styleRequirement"`
	Mock             string `json:"mock"`
}

// result is the Tool's result; field order is the result's key order.
type result struct {
	Passed        bool     `json:"passed"`
	PolicyVersion string   `json:"policyVersion"`
	Reasons       []string `json:"reasons"`
	ReviewModelID string   `json:"reviewModelId"`
	StyleDigest   string   `json:"styleDigest"`
}

// Execute reviews one asset and returns the verdict: with the call's review model when it
// carries one, otherwise with the mock policy.
func (e Executor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	var args arguments
	if err := json.Unmarshal(action.Arguments, &args); err != nil || args.AssetRef == "" || args.PhotoAssetID == "" || args.StyleRequirement == "" {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: %w", ToolName, errors.New("arguments carry no assetRef, photoAssetId and styleRequirement"))
	}

	var (
		out  result
		cost *domain.Cost
	)
	if action.ReviewModelID == "" {
		// The mock policy asks no model, so there is no charge to record.
		out = mockReview(args)
	} else {
		// A forced verdict would let the deciding model overrule the review model, so the
		// mock control is refused rather than ignored.
		if args.Mock != "" {
			return registry.ToolExecutionResult{}, fmt.Errorf("%s: the mock control applies only to the mock review policy", ToolName)
		}
		reviewed, reviewedCost, err := e.modelReview(ctx, action, args)
		if err != nil {
			return registry.ToolExecutionResult{}, err
		}
		out, cost = reviewed, reviewedCost
	}
	out.StyleDigest = StyleDigest(args.StyleRequirement)

	output, err := json.Marshal(out)
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: encode result: %w", ToolName, err)
	}
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{Output: output, Cost: cost}}, nil
}

// mockReview applies the deterministic mock policy.
func mockReview(args arguments) result {
	passed := Verdict(args.AssetRef)
	switch args.Mock {
	case mockPass:
		passed = true
	case mockFail:
		passed = false
	}
	out := result{Passed: passed, PolicyVersion: PolicyVersion, Reasons: []string{}, ReviewModelID: MockReviewModelID}
	if !passed {
		out.Reasons = []string{rejectionReason}
	}
	return out
}

// Verdict is the uncontrolled mock review decision for an asset reference: it fails when
// the first byte of the reference's SHA-256 is divisible by three, so roughly two of three
// references pass and the same reference always gets the same verdict.
func Verdict(assetRef string) bool {
	sum := sha256.Sum256([]byte(assetRef))
	return sum[0]%3 != 0
}

// StyleDigest returns "sha256:" followed by the lowercase hex SHA-256 of the style
// requirement's UTF-8 bytes, so a fact can show which requirement a verdict answered
// without repeating it.
func StyleDigest(styleRequirement string) string {
	sum := sha256.Sum256([]byte(styleRequirement))
	return "sha256:" + hex.EncodeToString(sum[:])
}
