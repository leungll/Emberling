package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/domain"
)

func newTestCompiler() (*Compiler, *fakeCatalog) {
	catalog := newFakeCatalog()
	return NewCompiler(catalog, fixedClock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}), catalog
}

func TestCompiler_DocumentProcessing_ProducesStableOrder(t *testing.T) {
	compiler, _ := newTestCompiler()
	def := loadFixture(t, "document_processing.json")

	got, err := compiler.Compile(context.Background(), def)
	if err != nil {
		t.Fatalf("Compile() unexpected error: %v", err)
	}

	want := []string{"node_input", "node_prompt", "node_summary", "node_translation", "node_output"}
	if diff := cmp.Diff(want, got.Order); diff != "" {
		t.Errorf("Order mismatch (-want +got):\n%s", diff)
	}
	if got.OutputNodeID != "node_output" {
		t.Errorf("OutputNodeID = %q, want node_output", got.OutputNodeID)
	}
	if got.Validation.Status != domain.ValidationValid {
		t.Errorf("Validation.Status = %q, want VALID", got.Validation.Status)
	}
	if got.Validation.ValidatorVersion != ValidatorVersion {
		t.Errorf("Validation.ValidatorVersion = %q, want %q", got.Validation.ValidatorVersion, ValidatorVersion)
	}
}

func TestCompiler_UnknownNodeType_Fails(t *testing.T) {
	compiler, _ := newTestCompiler()
	def := loadFixture(t, "document_processing.json")
	def = cloneDefinition(t, def)
	for i, n := range def.Nodes {
		if n.ID == "node_prompt" {
			def.Nodes[i].Type = "does_not_exist"
		}
	}

	_, err := compiler.Compile(context.Background(), def)
	if err == nil {
		t.Fatal("Compile() expected error, got nil")
	}
	ce, ok := AsCompileError(err)
	if !ok {
		t.Fatalf("expected *CompileError, got %T: %v", err, err)
	}
	if ce.Stage != StageStructure {
		t.Errorf("Stage = %q, want %q", ce.Stage, StageStructure)
	}
	if !containsCode(ce.Errors, CodeUnknownNodeType) {
		t.Errorf("Errors = %+v, want to contain %s", ce.Errors, CodeUnknownNodeType)
	}
}

// TestCompiler_UnknownPort_ReturnsUnknownPort asserts the Structure stage's port
// resolution: an edge whose sourceHandle names no output port on its source node, and
// another edge whose targetHandle names no input port on its target node, both produce
// UNKNOWN_PORT. All three node types here are registered (so no UNKNOWN_NODE_TYPE is
// reported alongside), isolating this fixture to exactly the two port errors.
func TestCompiler_UnknownPort_ReturnsUnknownPort(t *testing.T) {
	compiler, _ := newTestCompiler()
	def := domain.Definition{
		WorkflowID: "wf_unknown_port",
		Nodes: []domain.Node{
			{ID: "a", Type: NodeTypeTextInput, Config: json.RawMessage(`{"inputKey":"k","required":true}`)},
			{ID: "b", Type: NodeTypePromptTemplate, Config: json.RawMessage(`{}`)},
			{ID: "c", Type: NodeTypeTextOutput, Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			// a has no "bogus_out" output port -> UNKNOWN_PORT on the source side.
			{ID: "e1", Source: "a", SourceHandle: "bogus_out", Target: "b", TargetHandle: "text"},
			// c has no "bogus_in" input port -> UNKNOWN_PORT on the target side.
			{ID: "e2", Source: "b", SourceHandle: "text", Target: "c", TargetHandle: "bogus_in"},
		},
	}

	_, err := compiler.Compile(context.Background(), def)
	if err == nil {
		t.Fatal("Compile() expected error, got nil")
	}
	ce, ok := AsCompileError(err)
	if !ok {
		t.Fatalf("expected *CompileError, got %T: %v", err, err)
	}
	if ce.Stage != StageStructure {
		t.Errorf("Stage = %q, want %q", ce.Stage, StageStructure)
	}
	if len(ce.Errors) != 2 {
		t.Fatalf("Errors = %+v, want exactly 2 (one per bad handle)", ce.Errors)
	}
	for _, e := range ce.Errors {
		if e.Code != CodeUnknownPort {
			t.Errorf("Code = %q, want %s: %+v", e.Code, CodeUnknownPort, e)
		}
	}
}

func TestCompiler_ConfigSchemaViolation_StopsBeforeSemantics(t *testing.T) {
	compiler, catalog := newTestCompiler()
	def := loadFixture(t, "document_processing.json")
	def = cloneDefinition(t, def)
	for i, n := range def.Nodes {
		if n.ID == "node_summary" {
			// text_generation's ConfigSchema requires "modelId"; drop it.
			def.Nodes[i].Config = json.RawMessage(`{}`)
		}
	}

	_, err := compiler.Compile(context.Background(), def)
	if err == nil {
		t.Fatal("Compile() expected error, got nil")
	}
	ce, ok := AsCompileError(err)
	if !ok {
		t.Fatalf("expected *CompileError, got %T: %v", err, err)
	}
	if ce.Stage != StageConfigSchema {
		t.Errorf("Stage = %q, want %q", ce.Stage, StageConfigSchema)
	}
	if !containsCode(ce.Errors, CodeValidationFailed) {
		t.Errorf("Errors = %+v, want to contain %s", ce.Errors, CodeValidationFailed)
	}
	if catalog.semanticsCalls != 0 {
		t.Errorf("semanticsCalls = %d, want 0 (Semantics stage must not run)", catalog.semanticsCalls)
	}
}

func TestCompiler_SemanticFailure_StopsBeforeGraph(t *testing.T) {
	compiler, catalog := newTestCompiler()
	def := loadFixture(t, "document_processing.json")
	def = cloneDefinition(t, def)
	for i, n := range def.Nodes {
		if n.ID == "node_summary" {
			def.Nodes[i].Config = json.RawMessage(`{"modelId":"text-model-v1","__forceSemanticError":true}`)
		}
	}
	// Also introduce a cycle, so that if Compile incorrectly proceeded past Semantics,
	// it would report a Graph-stage error instead of the expected Semantics one.
	def.Edges = append(def.Edges, domain.Edge{
		ID: "edge_back", Source: "node_translation", SourceHandle: "text",
		Target: "node_summary", TargetHandle: "prompt",
	})

	_, err := compiler.Compile(context.Background(), def)
	if err == nil {
		t.Fatal("Compile() expected error, got nil")
	}
	ce, ok := AsCompileError(err)
	if !ok {
		t.Fatalf("expected *CompileError, got %T: %v", err, err)
	}
	if ce.Stage != StageSemantics {
		t.Errorf("Stage = %q, want %q", ce.Stage, StageSemantics)
	}
	if !containsCode(ce.Errors, CodeSemanticValidationFailed) {
		t.Errorf("Errors = %+v, want to contain %s", ce.Errors, CodeSemanticValidationFailed)
	}
	if catalog.semanticsCalls == 0 {
		t.Errorf("semanticsCalls = 0, want > 0 (Semantics stage must have run)")
	}
}

// TestCompiler_AgentNode_InvalidConfig_ReportsNodeAndField asserts the Semantics stage
// hook for `agent` nodes: an invalid AgentNodeConfig (maxTurns < 1) is reported as
// SEMANTIC_VALIDATION_FAILED against the node's id, with Path naming the offending field,
// rather than passing silently through the ManagedAgentBinding's own no-op
// ValidateSemantics.
func TestCompiler_AgentNode_InvalidConfig_ReportsNodeAndField(t *testing.T) {
	compiler, _ := newTestCompiler()
	def := domain.Definition{
		WorkflowID: "wf_agent_invalid_config",
		Nodes: []domain.Node{
			{ID: "node_in", Type: NodeTypeTextInput, Config: json.RawMessage(`{"inputKey":"k","required":true}`)},
			{ID: "node_agent", Type: NodeTypeAgent, Config: json.RawMessage(`{"modelId":"text-model-v1","maxTurns":0,"timeoutMs":120000}`)},
			{ID: "node_out", Type: NodeTypeTextOutput, Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "node_in", SourceHandle: "text", Target: "node_agent", TargetHandle: "input"},
			{ID: "e2", Source: "node_agent", SourceHandle: "text", Target: "node_out", TargetHandle: "text"},
		},
	}

	_, err := compiler.Compile(context.Background(), def)
	if err == nil {
		t.Fatal("Compile() expected error, got nil")
	}
	ce, ok := AsCompileError(err)
	if !ok {
		t.Fatalf("expected *CompileError, got %T: %v", err, err)
	}
	if ce.Stage != StageSemantics {
		t.Errorf("Stage = %q, want %q", ce.Stage, StageSemantics)
	}
	if len(ce.Errors) != 1 {
		t.Fatalf("Errors = %+v, want exactly 1", ce.Errors)
	}
	got := ce.Errors[0]
	if got.Code != CodeSemanticValidationFailed {
		t.Errorf("Code = %q, want %q", got.Code, CodeSemanticValidationFailed)
	}
	if got.NodeID != "node_agent" {
		t.Errorf("NodeID = %q, want node_agent", got.NodeID)
	}
	if got.Path != "nodes[node_agent].config.maxTurns" {
		t.Errorf("Path = %q, want nodes[node_agent].config.maxTurns", got.Path)
	}
}

func TestCompiler_Cycle_ReturnsDagHasCycle(t *testing.T) {
	compiler, _ := newTestCompiler()
	// Two prompt_template nodes feeding each other's "text" port. prompt_template's
	// ConfigSchema is fully permissive and it has no semantic checks, so this fixture
	// reaches the Graph stage cleanly and hasCycle's early-return means no other Graph
	// code (e.g. output-node-count) gets reported alongside DAG_HAS_CYCLE.
	def := domain.Definition{
		WorkflowID: "wf_cycle",
		Nodes: []domain.Node{
			{ID: "a", Type: NodeTypePromptTemplate, Config: json.RawMessage(`{}`)},
			{ID: "b", Type: NodeTypePromptTemplate, Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "a", SourceHandle: "text", Target: "b", TargetHandle: "text"},
			{ID: "e2", Source: "b", SourceHandle: "text", Target: "a", TargetHandle: "text"},
		},
	}

	_, err := compiler.Compile(context.Background(), def)
	if err == nil {
		t.Fatal("Compile() expected error, got nil")
	}
	ce, ok := AsCompileError(err)
	if !ok {
		t.Fatalf("expected *CompileError, got %T: %v", err, err)
	}
	if ce.Stage != StageGraph {
		t.Errorf("Stage = %q, want %q", ce.Stage, StageGraph)
	}
	if len(ce.Errors) != 1 || ce.Errors[0].Code != CodeDAGHasCycle {
		t.Errorf("Errors = %+v, want exactly one %s", ce.Errors, CodeDAGHasCycle)
	}
}

func TestCompiler_IncompatibleEdge_ReturnsIncompatibleEdge(t *testing.T) {
	compiler, _ := newTestCompiler()
	// node_in(text_input) -> node_img(image_generation).prompt [valid text->text],
	// node_img.image -> node_gen(text_generation).prompt [INVALID image->text],
	// node_gen.text -> node_out(text_output).text [valid].
	def := domain.Definition{
		WorkflowID: "wf_incompatible",
		Nodes: []domain.Node{
			{ID: "node_in", Type: NodeTypeTextInput, Config: json.RawMessage(`{"inputKey":"k","required":true}`)},
			{ID: "node_img", Type: NodeTypeImageGeneration, Config: json.RawMessage(`{"modelId":"m"}`)},
			{ID: "node_gen", Type: NodeTypeTextGeneration, Config: json.RawMessage(`{"modelId":"m"}`)},
			{ID: "node_out", Type: NodeTypeTextOutput, Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "node_in", SourceHandle: "text", Target: "node_img", TargetHandle: "prompt"},
			{ID: "e2", Source: "node_img", SourceHandle: "image", Target: "node_gen", TargetHandle: "prompt"},
			{ID: "e3", Source: "node_gen", SourceHandle: "text", Target: "node_out", TargetHandle: "text"},
		},
	}

	_, err := compiler.Compile(context.Background(), def)
	if err == nil {
		t.Fatal("Compile() expected error, got nil")
	}
	ce, ok := AsCompileError(err)
	if !ok {
		t.Fatalf("expected *CompileError, got %T: %v", err, err)
	}
	if ce.Stage != StageGraph {
		t.Errorf("Stage = %q, want %q", ce.Stage, StageGraph)
	}
	if len(ce.Errors) != 1 || ce.Errors[0].Code != CodeIncompatibleEdge {
		t.Errorf("Errors = %+v, want exactly one %s", ce.Errors, CodeIncompatibleEdge)
	}
}

func TestCompiler_TwoOutputNodes_Fails(t *testing.T) {
	compiler, _ := newTestCompiler()
	def := loadFixture(t, "document_processing.json")
	def = cloneDefinition(t, def)
	// Add a second, independent Output Node fed directly from the existing translation
	// node, so both Output Nodes are reachable and neither has an outgoing edge -- this
	// isolates OUTPUT_NODE_COUNT from OUTPUT_NODE_NOT_UNIQUE_SINK / reachability noise.
	def.Nodes = append(def.Nodes, domain.Node{ID: "node_output2", Type: NodeTypeTextOutput, Config: json.RawMessage(`{}`)})
	def.Edges = append(def.Edges, domain.Edge{
		ID: "edge_translation_output2", Source: "node_translation", SourceHandle: "text",
		Target: "node_output2", TargetHandle: "text",
	})

	_, err := compiler.Compile(context.Background(), def)
	if err == nil {
		t.Fatal("Compile() expected error, got nil")
	}
	ce, ok := AsCompileError(err)
	if !ok {
		t.Fatalf("expected *CompileError, got %T: %v", err, err)
	}
	if ce.Stage != StageGraph {
		t.Errorf("Stage = %q, want %q", ce.Stage, StageGraph)
	}
	if !containsCode(ce.Errors, CodeOutputNodeCount) {
		t.Errorf("Errors = %+v, want to contain %s", ce.Errors, CodeOutputNodeCount)
	}
}

func TestCompiler_NoOutputNode_Fails(t *testing.T) {
	compiler, _ := newTestCompiler()
	def := loadFixture(t, "document_processing.json")
	def = cloneDefinition(t, def)
	def = removeNode(def, "node_output")
	def = removeEdge(def, "edge_translation_output")

	_, err := compiler.Compile(context.Background(), def)
	if err == nil {
		t.Fatal("Compile() expected error, got nil")
	}
	ce, ok := AsCompileError(err)
	if !ok {
		t.Fatalf("expected *CompileError, got %T: %v", err, err)
	}
	if ce.Stage != StageGraph {
		t.Errorf("Stage = %q, want %q", ce.Stage, StageGraph)
	}
	if !containsCode(ce.Errors, CodeOutputNodeCount) {
		t.Errorf("Errors = %+v, want to contain %s", ce.Errors, CodeOutputNodeCount)
	}
}

func TestCompiler_OutputNodeWithOutgoingEdge_Fails(t *testing.T) {
	compiler, _ := newTestCompiler()
	def := loadFixture(t, "document_processing.json")
	def = cloneDefinition(t, def)
	// text_output has a test-only "text" output port (fakeCatalog), letting an Output
	// Node validly carry an outgoing edge for this test.
	def.Nodes = append(def.Nodes, domain.Node{ID: "node_extra", Type: NodeTypePromptTemplate, Config: json.RawMessage(`{}`)})
	def.Edges = append(def.Edges, domain.Edge{
		ID: "edge_output_extra", Source: "node_output", SourceHandle: "text",
		Target: "node_extra", TargetHandle: "text",
	})

	_, err := compiler.Compile(context.Background(), def)
	if err == nil {
		t.Fatal("Compile() expected error, got nil")
	}
	ce, ok := AsCompileError(err)
	if !ok {
		t.Fatalf("expected *CompileError, got %T: %v", err, err)
	}
	if ce.Stage != StageGraph {
		t.Errorf("Stage = %q, want %q", ce.Stage, StageGraph)
	}
	if !containsCode(ce.Errors, CodeOutputNodeNotUniqueSink) {
		t.Errorf("Errors = %+v, want to contain %s", ce.Errors, CodeOutputNodeNotUniqueSink)
	}
}

func TestCompiler_UnreachableNode_Fails(t *testing.T) {
	compiler, _ := newTestCompiler()
	def := loadFixture(t, "document_processing.json")
	def = cloneDefinition(t, def)
	// An isolated node with no edges at all: unreachable to the Output Node, but without
	// a second, unrelated error. That needs a node type with no required inputs, which
	// rules out prompt_template ("text" is required) and agent ("input" is required by
	// the MVP port contract); image_input declares no input ports at all.
	def.Nodes = append(def.Nodes, domain.Node{ID: "node_isolated", Type: NodeTypeImageInput, Config: json.RawMessage(`{}`)})

	_, err := compiler.Compile(context.Background(), def)
	if err == nil {
		t.Fatal("Compile() expected error, got nil")
	}
	ce, ok := AsCompileError(err)
	if !ok {
		t.Fatalf("expected *CompileError, got %T: %v", err, err)
	}
	if ce.Stage != StageGraph {
		t.Errorf("Stage = %q, want %q", ce.Stage, StageGraph)
	}
	if !containsCode(ce.Errors, CodeNodeUnreachableToOutput) {
		t.Errorf("Errors = %+v, want to contain %s", ce.Errors, CodeNodeUnreachableToOutput)
	}
}

func TestCompiler_MissingRequiredInput_Fails(t *testing.T) {
	compiler, _ := newTestCompiler()
	def := loadFixture(t, "document_processing.json")
	def = cloneDefinition(t, def)
	// Removing edge_prompt_summary leaves node_summary's required "prompt" port
	// unconnected. node_prompt (upstream of the removed edge) still reaches node_output
	// via no other path, so it also becomes unreachable -- an unavoidable side effect of
	// this minimal fixture, hence the "contains" (not exact-set) assertion.
	def = removeEdge(def, "edge_prompt_summary")

	_, err := compiler.Compile(context.Background(), def)
	if err == nil {
		t.Fatal("Compile() expected error, got nil")
	}
	ce, ok := AsCompileError(err)
	if !ok {
		t.Fatalf("expected *CompileError, got %T: %v", err, err)
	}
	if ce.Stage != StageGraph {
		t.Errorf("Stage = %q, want %q", ce.Stage, StageGraph)
	}
	if !containsCode(ce.Errors, CodeMissingRequiredInput) {
		t.Errorf("Errors = %+v, want to contain %s", ce.Errors, CodeMissingRequiredInput)
	}
}

// TestCompiler_AmbiguousInput_ReturnsAmbiguousInput asserts a required input port fed by
// two edges reports AMBIGUOUS_INPUT. Two distinct text_input nodes both feed
// node_gen.prompt (text_generation's required input); node_gen then feeds a single
// Output Node, so every node stays reachable and no other Graph-stage code (cycle,
// incompatible edge, output count/reachability) is triggered alongside it.
func TestCompiler_AmbiguousInput_ReturnsAmbiguousInput(t *testing.T) {
	compiler, _ := newTestCompiler()
	def := domain.Definition{
		WorkflowID: "wf_ambiguous_input",
		Nodes: []domain.Node{
			{ID: "node_in1", Type: NodeTypeTextInput, Config: json.RawMessage(`{"inputKey":"a","required":true}`)},
			{ID: "node_in2", Type: NodeTypeTextInput, Config: json.RawMessage(`{"inputKey":"b","required":true}`)},
			{ID: "node_gen", Type: NodeTypeTextGeneration, Config: json.RawMessage(`{"modelId":"m"}`)},
			{ID: "node_out", Type: NodeTypeTextOutput, Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "node_in1", SourceHandle: "text", Target: "node_gen", TargetHandle: "prompt"},
			{ID: "e2", Source: "node_in2", SourceHandle: "text", Target: "node_gen", TargetHandle: "prompt"},
			{ID: "e3", Source: "node_gen", SourceHandle: "text", Target: "node_out", TargetHandle: "text"},
		},
	}

	_, err := compiler.Compile(context.Background(), def)
	if err == nil {
		t.Fatal("Compile() expected error, got nil")
	}
	ce, ok := AsCompileError(err)
	if !ok {
		t.Fatalf("expected *CompileError, got %T: %v", err, err)
	}
	if ce.Stage != StageGraph {
		t.Errorf("Stage = %q, want %q", ce.Stage, StageGraph)
	}
	if len(ce.Errors) != 1 || ce.Errors[0].Code != CodeAmbiguousInput {
		t.Errorf("Errors = %+v, want exactly one %s", ce.Errors, CodeAmbiguousInput)
	}
}

func TestCompiler_AIGCMedia_CompilesCleanly(t *testing.T) {
	compiler, _ := newTestCompiler()
	def := loadFixture(t, "aigc_media.json")

	got, err := compiler.Compile(context.Background(), def)
	if err != nil {
		t.Fatalf("Compile() unexpected error: %v", err)
	}
	if got.OutputNodeID != "node_output" {
		t.Errorf("OutputNodeID = %q, want node_output", got.OutputNodeID)
	}
	if len(got.Order) != len(def.Nodes) {
		t.Errorf("Order has %d entries, want %d", len(got.Order), len(def.Nodes))
	}
}
