//go:build integration

package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const (
	factProducerTestToolName = "test_fact_generate"
	factConsumerTestToolName = "test_fact_review"
	factReaderTestNodeType   = "test_fact_reader"
)

// factTestToolExecutor lets the catalogue expose fact declarations. It is never called.
type factTestToolExecutor struct{}

func (factTestToolExecutor) Execute(context.Context, registry.ToolAction) (registry.ToolExecutionResult, error) {
	return registry.ToolExecutionResult{}, nil
}

// factDeclaringTestRegistrations returns a producer Tool, a consumer Tool that requires and
// bases its own fact on the producer's, and a Node Type that reads both fact types.
func factDeclaringTestRegistrations() (registry.NodeRegistration, []registry.ToolRegistration) {
	requireApproved := true
	producer := registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          factProducerTestToolName,
			Description:   "Fact producer used only by catalogue contract tests",
			InputSchema:   json.RawMessage(`{"type":"object","properties":{"photoAssetId":{"type":"string"}}}`),
			OutputSchema:  json.RawMessage(`{"type":"object","properties":{"assetId":{"type":"string"}}}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
			ExecutionKind: domain.ToolExecutionSync,
			Produces: &domain.FactProduction{
				FactType:       "test_generated",
				SubjectPointer: domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/assetId"},
				BindArguments: map[string]domain.FactPointer{
					"photoAssetId": {Source: domain.FactPointerArguments, Pointer: "/photoAssetId"},
				},
			},
			CountsTowardGenerationLimit: true,
		},
		Executor: factTestToolExecutor{},
	}
	consumer := registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          factConsumerTestToolName,
			Description:   "Fact consumer used only by catalogue contract tests",
			InputSchema:   json.RawMessage(`{"type":"object","properties":{"assetRef":{"type":"string"},"photoAssetId":{"type":"string"}}}`),
			OutputSchema:  json.RawMessage(`{"type":"object","properties":{"assetId":{"type":"string"},"approved":{"type":"boolean"}}}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			ExecutionKind: domain.ToolExecutionSync,
			Produces: &domain.FactProduction{
				FactType:       "test_reviewed",
				SubjectPointer: domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/assetId"},
				VerdictPointer: &domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/approved"},
				BasisFactType:  "test_generated",
			},
			Requires: []domain.FactRequirement{{
				FactType:        "test_generated",
				SubjectArgument: "/assetRef",
				MatchBindings:   []string{"photoAssetId"},
				RequireVerdict:  &requireApproved,
			}},
		},
		Executor: factTestToolExecutor{},
	}

	node := testAsyncNodeRegistration(nil)
	node.Metadata.Type = factReaderTestNodeType
	node.Metadata.DisplayName = "Test Fact Reader"
	node.Metadata.FactInputs = []string{"test_generated", "test_reviewed"}
	return node, []registry.ToolRegistration{consumer, producer}
}

func compactJSON(t *testing.T, raw string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(raw)); err != nil {
		t.Fatalf("compact %s: %v", raw, err)
	}
	return buf.String()
}

func TestAPI_ToolsCatalog_FactDeclarations_SerialisedInDTOShape(t *testing.T) {
	node, tools := factDeclaringTestRegistrations()
	env := newTestEnvWithOptions(t, testEnvOptions{
		ExtraNodeRegistrations: []registry.NodeRegistration{node},
		ExtraToolRegistrations: tools,
	})

	resp, body := env.doJSON(t, http.MethodGet, "/api/tools", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/tools status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	items := catalogItemsByKey(t, body, "name")

	cases := []struct {
		name         string
		produces     string // empty means the key must be absent
		requires     string
		countsToward string
	}{
		{
			name:         factProducerTestToolName,
			produces:     `{"factType":"test_generated","subjectPointer":{"source":"RESULT","pointer":"/assetId"},"bindArguments":{"photoAssetId":{"source":"ARGUMENTS","pointer":"/photoAssetId"}}}`,
			requires:     `[]`,
			countsToward: `true`,
		},
		{
			name:         factConsumerTestToolName,
			produces:     `{"factType":"test_reviewed","subjectPointer":{"source":"RESULT","pointer":"/assetId"},"bindArguments":{},"verdictPointer":{"source":"RESULT","pointer":"/approved"},"basisFactType":"test_generated"}`,
			requires:     `[{"factType":"test_generated","subjectArgument":"/assetRef","matchBindings":["photoAssetId"],"requireVerdict":true}]`,
			countsToward: `false`,
		},
		{
			// A built-in Tool declaring no facts.
			name:         "lookup",
			requires:     `[]`,
			countsToward: `false`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item, ok := items[tc.name]
			if !ok {
				t.Fatalf("GET /api/tools does not list %s: %s", tc.name, body)
			}
			raw, present := item["produces"]
			switch {
			case tc.produces == "" && present:
				t.Errorf("%s.produces = %s, want the key absent for an undeclared production", tc.name, raw)
			case tc.produces != "" && compactJSON(t, string(raw)) != tc.produces:
				t.Errorf("%s.produces = %s, want %s", tc.name, raw, tc.produces)
			}
			if got := compactJSON(t, string(item["requires"])); got != tc.requires {
				t.Errorf("%s.requires = %s, want %s", tc.name, got, tc.requires)
			}
			if got := string(item["countsTowardGenerationLimit"]); got != tc.countsToward {
				t.Errorf("%s.countsTowardGenerationLimit = %s, want %s", tc.name, got, tc.countsToward)
			}
		})
	}
}

func TestAPI_NodeTypesCatalog_FactInputs_AlwaysAnArray(t *testing.T) {
	node, tools := factDeclaringTestRegistrations()
	env := newTestEnvWithOptions(t, testEnvOptions{
		ExtraNodeRegistrations: []registry.NodeRegistration{node},
		ExtraToolRegistrations: tools,
	})

	resp, body := env.doJSON(t, http.MethodGet, "/api/node-types", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/node-types status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	items := catalogItemsByKey(t, body, "type")
	for typ, want := range map[string]string{
		factReaderTestNodeType: `["test_generated","test_reviewed"]`,
		"text_input":           `[]`,
	} {
		item, ok := items[typ]
		if !ok {
			t.Fatalf("GET /api/node-types does not list %s: %s", typ, body)
		}
		if got := string(item["factInputs"]); got != want {
			t.Errorf("GET /api/node-types %s.factInputs = %s, want %s", typ, got, want)
		}
	}
}
