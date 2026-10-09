package registry

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

const (
	factTestImageGenerated = "image_generated"
	factTestAssetReviewed  = "asset_reviewed"
)

// factProducerRegistration is a Tool producing image_generated about the asset in its
// result, bound to the photo and settings digest of the call.
func factProducerRegistration() ToolRegistration {
	reg := validSyncToolRegistration()
	reg.Metadata.Name = "generate_image"
	reg.Metadata.InputSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"photoAssetId": {"type": "string"},
			"settings": {"type": "object"}
		},
		"additionalProperties": false
	}`)
	reg.Metadata.OutputSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"asset": {"type": "object", "properties": {"assetId": {"type": "string"}}, "additionalProperties": false},
			"variants": {"type": "array", "items": {"type": "object", "properties": {"digest": {"type": "string"}}}}
		},
		"additionalProperties": false
	}`)
	reg.Metadata.Produces = &domain.FactProduction{
		FactType:       factTestImageGenerated,
		SubjectPointer: domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/asset/assetId"},
		BindArguments: map[string]domain.FactPointer{
			"photoAssetId":   {Source: domain.FactPointerArguments, Pointer: "/photoAssetId"},
			"settingsDigest": {Source: domain.FactPointerResult, Pointer: "/variants/0/digest"},
		},
	}
	reg.Metadata.CountsTowardGenerationLimit = true
	return reg
}

// factConsumerRegistration is a Tool requiring image_generated about its assetRef argument
// and producing asset_reviewed based on it.
func factConsumerRegistration() ToolRegistration {
	reg := validSyncToolRegistration()
	reg.Metadata.Name = "review_asset"
	reg.Metadata.InputSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"assetRef": {"type": "string"},
			"photoAssetId": {"type": "string"},
			"extra": {"type": "object", "additionalProperties": true}
		},
		"additionalProperties": false
	}`)
	reg.Metadata.OutputSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"assetId": {"type": "string"},
			"approved": {"type": "boolean"},
			"notes": {}
		},
		"additionalProperties": false
	}`)
	reg.Metadata.Produces = &domain.FactProduction{
		FactType:       factTestAssetReviewed,
		SubjectPointer: domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/assetId"},
		BindArguments: map[string]domain.FactPointer{
			"photoAssetId": {Source: domain.FactPointerArguments, Pointer: "/photoAssetId"},
		},
		VerdictPointer: &domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/approved"},
		BasisFactType:  factTestImageGenerated,
	}
	reg.Metadata.Requires = []domain.FactRequirement{{
		FactType:        factTestImageGenerated,
		SubjectArgument: "/assetRef",
		MatchBindings:   []string{"photoAssetId"},
	}}
	return reg
}

func registerAll(t *testing.T, regs ...ToolRegistration) *ToolRegistry {
	t.Helper()
	r := NewToolRegistry()
	for _, reg := range regs {
		if err := r.Register(reg); err != nil {
			t.Fatalf("Register(%s) error = %v, want nil", reg.Metadata.Name, err)
		}
	}
	return r
}

func requireErrorMentions(t *testing.T, err error, wants ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want one mentioning %q", wants)
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to mention %q", err, want)
		}
	}
}

func TestFactDeclarations_ConsumerRegisteredBeforeProducer_Accepted(t *testing.T) {
	tools := registerAll(t, factConsumerRegistration(), factProducerRegistration(), validSyncToolRegistration())

	nodes := NewNodeRegistry()
	node := validSyncNodeRegistration()
	node.Metadata.FactInputs = []string{factTestImageGenerated, factTestAssetReviewed}
	if err := nodes.Register(node); err != nil {
		t.Fatalf("NodeRegistry.Register() error = %v, want nil", err)
	}

	if err := ValidateFactDeclarations(tools.ListMetadata(), nodes.ListMetadata()); err != nil {
		t.Fatalf("ValidateFactDeclarations() = %v, want nil", err)
	}
}

func TestFactDeclarations_NoDeclarations_Accepted(t *testing.T) {
	tools := registerAll(t, validSyncToolRegistration(), validAsyncToolRegistration())
	if err := ValidateFactDeclarations(tools.ListMetadata(), nil); err != nil {
		t.Fatalf("ValidateFactDeclarations() = %v, want nil", err)
	}
}

func TestToolRegistry_Register_FactPointerOutsideSchema_Rejected(t *testing.T) {
	cases := []struct {
		name    string
		reg     func() ToolRegistration
		mutate  func(*domain.ToolMetadata)
		wantMsg string
	}{
		{
			name: "result subject not a declared property",
			reg:  factProducerRegistration,
			mutate: func(m *domain.ToolMetadata) {
				m.Produces.SubjectPointer.Pointer = "/asset/missing"
			},
			wantMsg: `produces.subjectPointer: pointer "/asset/missing" is outside the outputSchema`,
		},
		{
			name: "arguments binding resolved against the input schema",
			reg:  factProducerRegistration,
			mutate: func(m *domain.ToolMetadata) {
				// /asset exists only in the Output Schema.
				m.Produces.BindArguments["photoAssetId"] = domain.FactPointer{Source: domain.FactPointerArguments, Pointer: "/asset"}
			},
			wantMsg: `produces.bindArguments["photoAssetId"]: pointer "/asset" is outside the inputSchema`,
		},
		{
			name: "result binding not an array index",
			reg:  factProducerRegistration,
			mutate: func(m *domain.ToolMetadata) {
				m.Produces.BindArguments["settingsDigest"] = domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/variants/first/digest"}
			},
			wantMsg: `pointer "/variants/first/digest" is outside the outputSchema`,
		},
		{
			name: "verdict outside output schema",
			reg:  factConsumerRegistration,
			mutate: func(m *domain.ToolMetadata) {
				m.Produces.VerdictPointer = &domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/verdict"}
			},
			wantMsg: `produces.verdictPointer: pointer "/verdict" is outside the outputSchema`,
		},
		{
			name: "requirement subject outside input schema",
			reg:  factConsumerRegistration,
			mutate: func(m *domain.ToolMetadata) {
				m.Requires[0].SubjectArgument = "/assetId"
			},
			wantMsg: `requires[0].subjectArgument: pointer "/assetId" is outside the inputSchema`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := tc.reg()
			tc.mutate(&reg.Metadata)
			err := NewToolRegistry().Register(reg)
			requireErrorMentions(t, err, `register tool "`+reg.Metadata.Name+`"`, tc.wantMsg)
		})
	}
}

func TestToolRegistry_Register_FactPointerIntoOpenSchema_Accepted(t *testing.T) {
	cases := []struct {
		name    string
		pointer domain.FactPointer
	}{
		{"below additionalProperties true", domain.FactPointer{Source: domain.FactPointerArguments, Pointer: "/extra/any/depth"}},
		{"below a property with no declared structure", domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/notes/anything"}},
		{"escaped token below open object", domain.FactPointer{Source: domain.FactPointerArguments, Pointer: "/extra/a~1b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := factConsumerRegistration()
			reg.Metadata.Produces.BindArguments["extra"] = tc.pointer
			if err := NewToolRegistry().Register(reg); err != nil {
				t.Fatalf("Register() error = %v, want nil", err)
			}
		})
	}

	reg := validSyncToolRegistration()
	reg.Metadata.Produces = &domain.FactProduction{
		FactType:       factTestImageGenerated,
		SubjectPointer: domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/anything"},
	}
	if err := NewToolRegistry().Register(reg); err != nil {
		t.Fatalf("Register() against a schema with no properties error = %v, want nil", err)
	}
}

func TestToolRegistry_Register_FactPointerSyntaxInvalid_Rejected(t *testing.T) {
	reg := factProducerRegistration()
	reg.Metadata.Produces.SubjectPointer.Pointer = "asset/assetId"
	err := NewToolRegistry().Register(reg)
	requireErrorMentions(t, err, `register tool "generate_image"`, `produces.subjectPointer: pointer "asset/assetId" is not a valid JSON Pointer`)
}

func TestToolRegistry_Register_FactSelfReference_Rejected(t *testing.T) {
	requiresOwn := factConsumerRegistration()
	requiresOwn.Metadata.Requires[0].FactType = factTestAssetReviewed
	requireErrorMentions(t, NewToolRegistry().Register(requiresOwn),
		`register tool "review_asset"`, `a Tool must not require the fact type it produces`)

	basedOnOwn := factConsumerRegistration()
	basedOnOwn.Metadata.Produces.BasisFactType = factTestAssetReviewed
	requireErrorMentions(t, NewToolRegistry().Register(basedOnOwn),
		`register tool "review_asset"`, `a Tool must not base its fact on the fact type it produces`)
}

func TestFactDeclarations_RequiredFactTypeWithoutProducer_Rejected(t *testing.T) {
	tools := registerAll(t, factConsumerRegistration())
	err := ValidateFactDeclarations(tools.ListMetadata(), nil)
	requireErrorMentions(t, err,
		`tool "review_asset": requires[0].factType "image_generated" has no registered producer Tool`,
		`tool "review_asset": produces.basisFactType "image_generated" has no registered producer Tool`)
}

func TestFactDeclarations_NodeFactInputWithoutProducer_Rejected(t *testing.T) {
	tools := registerAll(t, factProducerRegistration())
	nodes := NewNodeRegistry()
	node := validSyncNodeRegistration()
	node.Metadata.FactInputs = []string{factTestAssetReviewed}
	if err := nodes.Register(node); err != nil {
		t.Fatalf("NodeRegistry.Register() error = %v, want nil", err)
	}
	err := ValidateFactDeclarations(tools.ListMetadata(), nodes.ListMetadata())
	requireErrorMentions(t, err, `node type "`+node.Metadata.Type+`": factInputs "asset_reviewed" has no registered producer Tool`)
}

func TestFactDeclarations_MatchBindingUnknownToProducers_Rejected(t *testing.T) {
	consumer := factConsumerRegistration()
	consumer.Metadata.Requires[0].MatchBindings = []string{"photoAssetId", "policyVersion"}
	tools := registerAll(t, factProducerRegistration(), consumer)
	err := ValidateFactDeclarations(tools.ListMetadata(), nil)
	requireErrorMentions(t, err, `tool "review_asset": requires[0].matchBindings "policyVersion" is not bound by any producer of fact type "image_generated"`)
	if strings.Contains(err.Error(), `"photoAssetId" is not bound`) {
		t.Fatalf("error = %q, want photoAssetId accepted because the producer binds it", err)
	}
}

func TestSchemaPointerResolves_ShapesOfSchema_MatchContract(t *testing.T) {
	cases := []struct {
		name    string
		schema  string
		pointer string
		ok      bool
	}{
		{"declared property", `{"properties":{"a":{}}}`, "/a", true},
		{"undeclared property", `{"properties":{"a":{}}}`, "/b", false},
		{"undeclared property with additionalProperties false", `{"properties":{"a":{}},"additionalProperties":false}`, "/b", false},
		{"undeclared property with additionalProperties true", `{"properties":{"a":{}},"additionalProperties":true}`, "/b/c", true},
		{"additionalProperties schema is walked", `{"additionalProperties":{"properties":{"x":{}}}}`, "/any/y", false},
		{"array index into items", `{"items":{"properties":{"x":{}}}}`, "/3/x", true},
		{"end-of-array token into items", `{"items":{"properties":{"x":{}}}}`, "/-/x", true},
		{"leading-zero index rejected", `{"items":{}}`, "/01", false},
		{"positional items accepted", `{"items":[{"type":"string"}]}`, "/0", true},
		{"false schema", `{"properties":{"a":false}}`, "/a/b", false},
		{"true schema", `{"properties":{"a":true}}`, "/a/b", true},
		{"empty schema", `{}`, "/a/b", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := schemaPointerResolves(json.RawMessage(tc.schema), tc.pointer)
			if (err == nil) != tc.ok {
				t.Fatalf("schemaPointerResolves(%s, %q) = %v, want ok=%v", tc.schema, tc.pointer, err, tc.ok)
			}
		})
	}
}
