package runtime

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// ValidatorVersion identifies the whole compilation contract, including the
// runInputSchema generation algorithm (docs/11-decisions.md §5: "Validate 与 Save 使用同一
// 条服务端编译链，成功保存时冻结 runInputSchema 和 validatorVersion"). Changing any stage's
// behaviour or the schema generation algorithm requires a new value.
const ValidatorVersion = "mvp-v1"

// CompiledDefinition is the successful result of Compile: the Definition together with
// the Execution Plan artifacts Run creation and the Scheduler need, plus the frozen
// runInputSchema and Validation stamp a Save would persist
// (docs/06-execution-model.md §1.2; docs/08-interface-spec.md §1.3).
type CompiledDefinition struct {
	Definition domain.Definition

	// Order is a stable topological order of node ids (docs/06-execution-model.md §1.3:
	// "按稳定拓扑顺序"), used both to report a deterministic execution order and as the
	// Scheduler's tie-break.
	Order []string
	// Upstream/Downstream map a node id to the distinct node ids of its direct
	// predecessors/successors (deduped by node pair, not edge count).
	Upstream   map[string][]string
	Downstream map[string][]string

	// OutputNodeID is the Definition's single, validated Output Node.
	OutputNodeID string

	// RunInputSchema is the canonical JSON Schema generated from Input Node config
	// (docs/08-interface-spec.md §1.3). It is frozen at Save time and never regenerated
	// for an existing Run.
	RunInputSchema json.RawMessage

	// Validation is the frozen compile stamp a successful Save would persist alongside
	// the Definition version.
	Validation domain.Validation
}

// Compiler runs the Definition Validate/Save compilation chain
// (docs/06-execution-model.md §1.2, docs/08-interface-spec.md §1.2): Structure ->
// ConfigSchema -> Semantics -> Graph -> Plan -> runInputSchema. Compile stops at the
// first stage producing any ValidationError, so a caller only ever sees one stage's
// worth of errors.
//
// Compiler is safe for concurrent use: its only mutable state is a mutex-guarded cache
// of compiled per-Node-Type ConfigSchemas.
type Compiler struct {
	catalog NodeCatalog
	clock   domain.Clock

	mu          sync.Mutex
	schemaCache map[string]*jsonschema.Schema
}

// NewCompiler builds a Compiler backed by catalog (the Node Type registration seam) and
// clock (used only to stamp Validation.ValidatedAt on a successful compile, so that
// timestamp stays testable rather than calling time.Now() directly).
func NewCompiler(catalog NodeCatalog, clock domain.Clock) *Compiler {
	return &Compiler{
		catalog:     catalog,
		clock:       clock,
		schemaCache: make(map[string]*jsonschema.Schema),
	}
}

// Compile runs the full validation chain against def and, on success, returns the
// Execution Plan and frozen runInputSchema a Save would persist.
func (c *Compiler) Compile(ctx context.Context, def domain.Definition) (*CompiledDefinition, error) {
	if errs := validateStructure(def, c.catalog); len(errs) > 0 {
		return nil, newCompileError(StageStructure, errs, def)
	}
	if errs := c.validateConfigSchema(def); len(errs) > 0 {
		return nil, newCompileError(StageConfigSchema, errs, def)
	}
	if errs := validateSemantics(ctx, def, c.catalog); len(errs) > 0 {
		return nil, newCompileError(StageSemantics, errs, def)
	}
	outputNodeID, errs := validateGraph(def, c.catalog)
	if len(errs) > 0 {
		return nil, newCompileError(StageGraph, errs, def)
	}
	order, upstream, downstream, err := buildPlan(def)
	if err != nil {
		// Graph already proved def acyclic; reaching here means an internal invariant
		// was violated, not a Definition authoring mistake.
		return nil, err
	}
	schema, errs := buildRunInputSchema(def)
	if len(errs) > 0 {
		return nil, newCompileError(StageRunInputSchema, errs, def)
	}

	return &CompiledDefinition{
		Definition:     def,
		Order:          order,
		Upstream:       upstream,
		Downstream:     downstream,
		OutputNodeID:   outputNodeID,
		RunInputSchema: schema,
		Validation: domain.Validation{
			Status:           domain.ValidationValid,
			ValidatorVersion: ValidatorVersion,
			ValidatedAt:      c.clock.Now(),
		},
	}, nil
}

// newCompileError sorts errs deterministically (node index, edge index, code) before
// wrapping them, so repeated compilation of the same invalid Definition reports errors
// in the same order every time.
func newCompileError(stage CompileStage, errs []ValidationError, def domain.Definition) *CompileError {
	sortValidationErrors(errs, def)
	return &CompileError{Stage: stage, Errors: errs}
}

// sortValidationErrors orders errs by (node index in def.Nodes, edge index in def.Edges
// if the error's Path names one, error Code). Errors whose NodeID/Path do not resolve to
// a known node/edge sort after every error that does.
func sortValidationErrors(errs []ValidationError, def domain.Definition) {
	nodeIndex := make(map[string]int, len(def.Nodes))
	for i, n := range def.Nodes {
		nodeIndex[n.ID] = i
	}
	edgeIndex := make(map[string]int, len(def.Edges))
	for i, e := range def.Edges {
		edgeIndex[e.ID] = i
	}

	rank := func(e ValidationError) (int, int) {
		ni := len(def.Nodes)
		if idx, ok := nodeIndex[e.NodeID]; ok {
			ni = idx
		}
		ei := len(def.Edges)
		if id := extractEdgeID(e.Path); id != "" {
			if idx, ok := edgeIndex[id]; ok {
				ei = idx
			}
		}
		return ni, ei
	}

	sort.SliceStable(errs, func(i, j int) bool {
		ni, ei := rank(errs[i])
		nj, ej := rank(errs[j])
		if ni != nj {
			return ni < nj
		}
		if ei != ej {
			return ei < ej
		}
		return errs[i].Code < errs[j].Code
	})
}

// extractEdgeID pulls the edge id out of a Path shaped like "edges[<id>]...", or returns
// "" if the path does not name an edge this way.
func extractEdgeID(path string) string {
	const prefix = "edges["
	i := strings.Index(path, prefix)
	if i == -1 {
		return ""
	}
	rest := path[i+len(prefix):]
	j := strings.Index(rest, "]")
	if j == -1 {
		return ""
	}
	return rest[:j]
}
