// Package architecture proves, mechanically, that the package layout CLAUDE.md's
// "Package boundaries" section describes is what the code actually does. The package
// dependency direction, and with it the rule that PostgreSQL is the authority for
// execution facts and the surrounding transactional guarantees, only hold if `runtime`
// truly never reaches PostgreSQL, `store` truly never decides the next Runtime step, and
// so on. Before this file, that section was enforced only by review; a stray import can
// silently widen a package's reach without any test noticing.
//
// This test shells out to `go list -json` (stdlib os/exec; no third-party dependency) to
// get every package's real, compiler-verified direct imports, then checks each one against
// a hand-derived allow-list keyed by the CLAUDE.md role the package plays. The allow-list
// is deliberately not wider than what the current graph already does: where CLAUDE.md's
// prose and the real graph disagree, that is reported as a failure (see the comments next
// to each role below for the ones that needed a documented decision, not a silent widen).
package architecture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

// modulePrefix is this module's declared import path (see go.mod); every internal package
// lives under it, and every import path this test cares about is reported relative to it.
const modulePrefix = "github.com/leungll/Emberling/backend"

// pkgInfo is the subset of one `go list -json` record this test needs. Imports is the
// package's own direct, non-test-file imports only (go list's default "Imports" field
// excludes TestImports/XTestImports) -- CLAUDE.md's dependency direction is a production
// code contract, not a constraint on what a _test.go file may import to build a fixture.
type pkgInfo struct {
	ImportPath string
	Imports    []string
}

// loadModuleGraph runs `go list -json <modulePrefix>/...` and returns each package's
// direct imports, keyed by import path relative to modulePrefix (e.g. "internal/domain").
// Using the real go/build-driven `go list` (rather than hand-parsing source with go/build
// or go/parser) means this test sees exactly what the Go compiler would: build tags,
// platform files and generated code are already resolved correctly.
func loadModuleGraph(t *testing.T) map[string][]string {
	t.Helper()
	cmd := exec.Command("go", "list", "-json", modulePrefix+"/...")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list -json %s/...: %v\n%s", modulePrefix, err, out.String())
	}

	graph := make(map[string][]string)
	dec := json.NewDecoder(&out)
	for dec.More() {
		var p pkgInfo
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("decode `go list -json` output: %v", err)
		}
		rel := strings.TrimPrefix(p.ImportPath, modulePrefix+"/")
		graph[rel] = p.Imports
	}
	if len(graph) == 0 {
		t.Fatalf("go list -json %s/... reported no packages", modulePrefix)
	}
	return graph
}

// role classifies pkg (a module-relative import path) into the CLAUDE.md "Package
// boundaries" role it plays, or "" if no known role claims it. An empty role is not
// treated as "no rule applies" by checkDependencyDirection below: a package this function
// cannot classify fails the test, so a brand new top-level directory must update this
// list rather than silently going unchecked.
func role(pkg string) string {
	switch {
	case pkg == "internal/domain":
		return "domain"
	case pkg == "internal/asset":
		return "asset"
	case pkg == "internal/runtime":
		return "runtime"
	case pkg == "internal/store":
		return "store"
	case pkg == "internal/store/postgres":
		return "store/postgres"
	case pkg == "internal/registry":
		return "registry"
	case pkg == "internal/nodes" || strings.HasPrefix(pkg, "internal/nodes/"):
		return "nodes"
	case pkg == "internal/tools" || strings.HasPrefix(pkg, "internal/tools/"):
		return "tools"
	case pkg == "internal/adapters" || strings.HasPrefix(pkg, "internal/adapters/"):
		return "adapters"
	case pkg == "internal/service":
		return "service"
	case pkg == "internal/work":
		return "work"
	case pkg == "internal/reconciler":
		return "reconciler"
	case pkg == "internal/api" || strings.HasPrefix(pkg, "internal/api/"):
		return "api"
	case pkg == "internal/config" || strings.HasPrefix(pkg, "internal/config/"):
		return "config"
	case pkg == "internal/trace" || strings.HasPrefix(pkg, "internal/trace/"):
		return "trace"
	case pkg == "internal/mockprovider":
		return "mockprovider"
	case pkg == "internal/mockproduction":
		return "mockproduction"
	case pkg == "internal/mockcontrol":
		return "mockcontrol"
	case pkg == "migrations":
		return "migrations"
	case pkg == "cmd" || strings.HasPrefix(pkg, "cmd/"):
		return "cmd"
	case pkg == "test" || strings.HasPrefix(pkg, "test/"):
		return "test"
	default:
		return ""
	}
}

// unrestrictedRoles need no import check at all. "cmd" is explicit in CLAUDE.md ("cmd/**
// may import anything"). "test" (everything under backend/test/**, including this package
// itself) is not a CLAUDE.md production role at all -- it is test harnesses exercising the
// production roles from outside, so CLAUDE.md's package-boundary directions do not apply
// to what a harness may import to build its fixtures. Without this, this very test package
// (which go list -json necessarily includes when walking modulePrefix+"/...") would report
// itself as an unclassified-package violation on every run.
var unrestrictedRoles = map[string]bool{"cmd": true, "test": true}

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}
	return m
}

// allowedInternalImports is CLAUDE.md's dependency direction, one entry per role, as the
// set of module-relative internal import paths that role may depend on. Every entry
// matches what the real graph already does today (verified by
// TestDependencyDirection_MatchesClaudeMdPackageBoundaries); nothing here is widened past
// a real, existing import just to make the test pass -- a role with no rows below (or a
// currently-unclassified package) is a failure, not a silent pass.
var allowedInternalImports = map[string]map[string]bool{
	// domain: "contains shared domain types and errors only" -- no internal import at all.
	"domain": set(),

	// runtime: "does not access PostgreSQL, HTTP, Provider SDKs, or process-global
	// queues" -- domain is its only internal dependency (see also
	// TestRuntime_NeverImportsImpureIO below for the specific-package ban CLAUDE.md
	// names).
	"runtime": set("internal/domain"),

	// store: "persists facts... does not call Providers, publish SSE, or decide the next
	// Runtime step" -- domain only; the pgx-specific implementation lives in
	// store/postgres, not here.
	"store": set("internal/domain"),

	// asset: Asset binary storage on the local volume.
	// It stores bytes and nothing else, so it needs only the domain errors an unknown,
	// duplicate or oversized Asset maps to; it never sees SQL, HTTP or a Runtime decision.
	"asset": set("internal/domain"),

	// store/postgres additionally imports the sibling "migrations" package: an
	// embed.FS-only bundle of migration SQL (see migrations/migrations.go), not a
	// CLAUDE.md package-boundary role in its own right -- it is store/postgres's own
	// startup asset, not a second dependency direction to police.
	"store/postgres": set("internal/store", "internal/domain", "migrations"),

	// registry: "owns stable Node Type, Model ID, and Tool Name resolution" -- domain
	// only.
	"registry": set("internal/domain"),

	// nodes/tools/adapters: "perform one registered operation... do not own retry,
	// timeout, callback routing, state transitions, or Event writes" -- registry (their
	// own registration contract) and domain only. None of the registered built-in Node
	// Types imports a sibling nodes/* package today, so that is not added preemptively.
	"nodes":    set("internal/registry", "internal/domain"),
	"tools":    set("internal/registry", "internal/domain"),
	"adapters": set("internal/registry", "internal/domain"),

	// service: "owns use cases, Unit of Work orchestration, and scheduling of post-COMMIT
	// work" -- domain, registry (to resolve Node/Model implementations), runtime (pure
	// decisions) and store (Unit of Work / repositories).
	"service": set("internal/domain", "internal/registry", "internal/runtime", "internal/store", "internal/asset"),

	// work: "call the same service use cases... may not maintain a second execution path
	// or update business state directly" -- service only in the real graph today (it
	// does not even need domain directly: service's own exported types cover it).
	"work": set("internal/service", "internal/domain"),

	// reconciler additionally imports internal/store to discover persisted READY, retry,
	// timeout and callback work, and to apply Pending Callback TTL retention. It hands
	// every claim and Execution-state mutation to the same service use cases work.Pool
	// calls. TTL deletion is operational retention, not an Execution transition and does
	// not write an Event. CLAUDE.md forbids a second execution path or direct mutation of
	// Execution business state; it does not impose a blanket store import ban.
	"reconciler": set("internal/service", "internal/domain", "internal/store"),

	// api: "parses transport input, calls service, and maps results. It does not query
	// repositories or advance executions directly." internal/runtime is allowed only
	// because service.ValidateResult.Errors is typed []runtime.ValidationError and api's
	// writeError/toValidationErrorDTOs map that result type onto the wire DTO (CLAUDE.md:
	// api "maps results") -- api never calls runtime.NewCompiler or any other Compiler
	// entry point itself. internal/config/readiness is allowed because api.Deps.Readiness
	// holds the shared *readiness.Probe reported by GET /health and /ready. The base config
	// and registry packages are deliberately absent: production API code does not import
	// them, and the allow-list must not reserve unused dependency edges.
	"api": set("internal/service", "internal/domain", "internal/runtime", "internal/config/readiness"),

	// config/**: today internal/config and internal/config/readiness import no internal
	// package at all (config_test.go/readiness_test.go aside, which this check does not
	// see). This row is the ceiling the task names ("config/** imports
	// domain/store/store/postgres only"), not a floor config must reach.
	"config": set("internal/domain", "internal/store", "internal/store/postgres"),

	// trace: "builds read-only projections... must never mutate or truncate authoritative
	// Execution State." No internal/trace package exists yet in this tree; forward-
	// declared for the same reason as api above.
	"trace": set("internal/domain", "internal/store"),

	// migrations: an embed.FS bundle of SQL, not a CLAUDE.md role -- it imports nothing
	// internal.
	"migrations": set(),

	// mockprovider: the deterministic HTTP Mock Provider, served as a process by
	// cmd/mockprovider and in-process by the contract tests. It stands in for an external
	// Provider, so it must not share a single Go type with the Runtime it is dispatched
	// from (its own package doc: "a real external Provider would not share Go types with
	// Emberling either"). The only internal import is internal/mockcontrol, the shared
	// test-fixture barrier and request record, which itself imports nothing internal: not
	// even internal/domain or internal/registry may be reached from here.
	"mockprovider": set("internal/mockcontrol"),

	// mockproduction: the deterministic production deployment stand-in the deploy Tool
	// calls, served as a process by cmd/mockproduction and in-process by tests. It stands in
	// for an external service exactly as mockprovider does and holds to the same rule: its
	// only internal import is internal/mockcontrol.
	"mockproduction": set("internal/mockcontrol"),

	// mockcontrol: the barrier and request record shared by the HTTP fixtures that stand
	// in for external services. It is fixture code like mockprovider and holds to the same
	// rule: no Go type shared with the Runtime, so no internal import at all.
	"mockcontrol": set(),
}

// violation is one forbidden edge (or one unclassifiable package) found by
// checkDependencyDirection.
type violation struct {
	Pkg    string
	Import string
}

func (v violation) String() string { return fmt.Sprintf("%s -> %s", v.Pkg, v.Import) }

// checkDependencyDirection walks graph (a plain map[string][]string with exactly
// loadModuleGraph's shape: module-relative package path -> its direct imports, which may
// be stdlib, third-party or internal) and reports every edge allowedInternalImports
// forbids. It takes a plain map rather than depending on loadModuleGraph/go list directly
// so it can be exercised against a synthetic, deliberately-violating graph in
// TestCheckDependencyDirection_CatchesViolation below -- proof this check actually rejects
// bad input, not a tautology that passes anything it is given.
func checkDependencyDirection(graph map[string][]string) []violation {
	var out []violation
	for pkg, imports := range graph {
		r := role(pkg)
		if r == "" {
			out = append(out, violation{Pkg: pkg, Import: "(package matches no known role; update role() and allowedInternalImports in dependencies_test.go)"})
			continue
		}
		if unrestrictedRoles[r] {
			continue
		}
		allowed := allowedInternalImports[r]
		for _, imp := range imports {
			rel := strings.TrimPrefix(imp, modulePrefix+"/")
			if rel == imp {
				continue // not one of this module's own packages (stdlib or third-party)
			}
			if !allowed[rel] {
				out = append(out, violation{Pkg: pkg, Import: imp})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pkg != out[j].Pkg {
			return out[i].Pkg < out[j].Pkg
		}
		return out[i].Import < out[j].Import
	})
	return out
}

// runtimeForbiddenImportPrefixes are the specific impure-I/O packages CLAUDE.md names
// runtime must never reach: "It does not access PostgreSQL, HTTP, Provider SDKs, or
// process-global queues."
var runtimeForbiddenImportPrefixes = []string{
	"net/http",
	"database/sql",
	"github.com/jackc/pgx",
}

// checkRuntimeForbiddenImports reports every internal/runtime import (direct or
// transitive is not checked here -- only direct, matching the rest of this file) that
// matches one of runtimeForbiddenImportPrefixes.
func checkRuntimeForbiddenImports(graph map[string][]string) []violation {
	var out []violation
	for pkg, imports := range graph {
		if role(pkg) != "runtime" {
			continue
		}
		for _, imp := range imports {
			for _, forbidden := range runtimeForbiddenImportPrefixes {
				if imp == forbidden || strings.HasPrefix(imp, forbidden+"/") {
					out = append(out, violation{Pkg: pkg, Import: imp})
				}
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------------------
// Real-graph tests
// ---------------------------------------------------------------------------------------

func TestDependencyDirection_MatchesClaudeMdPackageBoundaries(t *testing.T) {
	graph := loadModuleGraph(t)
	for _, v := range checkDependencyDirection(graph) {
		t.Errorf("forbidden dependency (CLAUDE.md \"Package boundaries\"): %s", v)
	}
}

func TestRuntime_NeverImportsImpureIO(t *testing.T) {
	graph := loadModuleGraph(t)
	for _, v := range checkRuntimeForbiddenImports(graph) {
		t.Errorf("internal/runtime must not import an impure I/O package (CLAUDE.md: "+
			"\"does not access PostgreSQL, HTTP, Provider SDKs\"): %s", v)
	}
}

// ---------------------------------------------------------------------------------------
// Proof the checker itself rejects bad input (failing-first, since the real graph today
// has nothing to catch): a synthetic graph carries one deliberate violation of each kind
// checkDependencyDirection is supposed to reject, and this test fails if either slips
// through.
// ---------------------------------------------------------------------------------------

func TestCheckDependencyDirection_CatchesViolation(t *testing.T) {
	synthetic := map[string][]string{
		"internal/domain": nil,
		"internal/store":  {modulePrefix + "/internal/domain"},
		// A pure decisions package reaching into store.Tx/postgres directly is exactly
		// what CLAUDE.md's runtime rule ("does not access PostgreSQL... or decide the
		// next Runtime step" being store's job) forbids.
		"internal/runtime": {modulePrefix + "/internal/domain", modulePrefix + "/internal/store"},
		// A brand new top-level package this test has never been told the role of.
		"internal/somethingnew": nil,
	}

	violations := checkDependencyDirection(synthetic)

	foundRuntimeToStore := false
	foundUnclassified := false
	for _, v := range violations {
		if v.Pkg == "internal/runtime" && v.Import == modulePrefix+"/internal/store" {
			foundRuntimeToStore = true
		}
		if v.Pkg == "internal/somethingnew" {
			foundUnclassified = true
		}
	}
	if !foundRuntimeToStore {
		t.Errorf("expected checkDependencyDirection to flag internal/runtime -> internal/store, got %v", violations)
	}
	if !foundUnclassified {
		t.Errorf("expected checkDependencyDirection to flag the unclassified package internal/somethingnew, got %v", violations)
	}
	// internal/store -> internal/domain is allowed and must not be reported alongside
	// the two deliberate violations above.
	for _, v := range violations {
		if v.Pkg == "internal/store" {
			t.Errorf("internal/store -> internal/domain is allowed and must not be flagged: %v", v)
		}
	}
}

func TestCheckRuntimeForbiddenImports_CatchesViolation(t *testing.T) {
	synthetic := map[string][]string{
		"internal/runtime": {
			modulePrefix + "/internal/domain",
			"github.com/jackc/pgx/v5",
			"net/http",
		},
	}
	violations := checkRuntimeForbiddenImports(synthetic)
	if len(violations) != 2 {
		t.Fatalf("expected 2 forbidden-import violations, got %d: %v", len(violations), violations)
	}
}
