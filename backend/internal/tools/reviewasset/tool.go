// Package reviewasset implements the `review_asset` Tool: a deterministic, side-effect
// free policy review of a generated image asset. The verdict is a pure function of the
// asset reference (about two of three references pass), unless the call carries the
// optional mock control that forces a verdict for a test or a demo.
//
// It performs exactly one registered operation and calls no Provider. It owns no retry,
// timeout, state transition or Event.
package reviewasset

import (
	"context"
	"crypto/sha256"
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

// PolicyVersion identifies the deterministic review rules. It is recorded with the fact so
// a later change of rules can be told apart from an earlier verdict.
const PolicyVersion = "mock-review-policy-v1"

// Mock control values for the optional `mock` argument.
const (
	mockPass = "pass"
	mockFail = "fail"
)

// rejectionReason is the one fixed reason a failed review reports.
const rejectionReason = "asset does not meet the mock review policy"

const inputSchema = `{
  "type": "object",
  "properties": {
    "assetRef": {"type": "string", "minLength": 1},
    "photoAssetId": {"type": "string", "minLength": 1},
    "mock": {"type": "string", "enum": ["pass", "fail"]}
  },
  "required": ["assetRef", "photoAssetId"],
  "additionalProperties": false
}`

const outputSchema = `{
  "type": "object",
  "properties": {
    "passed": {"type": "boolean"},
    "policyVersion": {"type": "string", "minLength": 1},
    "reasons": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["passed", "policyVersion", "reasons"],
  "additionalProperties": false
}`

// Registration returns the `review_asset` ToolRegistration.
//
// A review only reads its arguments, so it has no side effect and is safe to repeat. Its
// result establishes an `asset_reviewed` fact about the reviewed assetRef, bound to the
// source photo and the policy version, whose verdict is the `passed` member and whose basis
// is the asset's `image_generated` fact. The call itself requires that `image_generated`
// fact for the same assetRef and photo, so an asset from another photo, or one this Run
// never generated, cannot be reviewed into a passing verdict.
func Registration() registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          ToolName,
			Description:   "Review a generated image asset against the mock content policy",
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
				},
				VerdictPointer: &domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/passed"},
				BasisFactType:  generatedFactType,
			},
			Requires: []domain.FactRequirement{{
				FactType:        generatedFactType,
				SubjectArgument: "/assetRef",
				MatchBindings:   []string{"photoAssetId"},
			}},
		},
		Executor: Executor{},
	}
}

// Executor implements registry.ToolExecutor for `review_asset`.
type Executor struct{}

type arguments struct {
	AssetRef     string `json:"assetRef"`
	PhotoAssetID string `json:"photoAssetId"`
	Mock         string `json:"mock"`
}

// result is the Tool's result; field order is the result's key order.
type result struct {
	Passed        bool     `json:"passed"`
	PolicyVersion string   `json:"policyVersion"`
	Reasons       []string `json:"reasons"`
}

// Execute reviews one asset and returns the verdict.
func (Executor) Execute(_ context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	var args arguments
	if err := json.Unmarshal(action.Arguments, &args); err != nil || args.AssetRef == "" || args.PhotoAssetID == "" {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: %w", ToolName, errors.New("arguments carry no assetRef and photoAssetId"))
	}
	passed := Verdict(args.AssetRef)
	switch args.Mock {
	case mockPass:
		passed = true
	case mockFail:
		passed = false
	}
	out := result{Passed: passed, PolicyVersion: PolicyVersion, Reasons: []string{}}
	if !passed {
		out.Reasons = []string{rejectionReason}
	}
	output, err := json.Marshal(out)
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: encode result: %w", ToolName, err)
	}
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{Output: output}}, nil
}

// Verdict is the uncontrolled review decision for an asset reference: it fails when the
// first byte of the reference's SHA-256 is divisible by three, so roughly two of three
// references pass and the same reference always gets the same verdict.
func Verdict(assetRef string) bool {
	sum := sha256.Sum256([]byte(assetRef))
	return sum[0]%3 != 0
}
