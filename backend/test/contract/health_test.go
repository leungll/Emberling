//go:build integration

package contract

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/api"
	"github.com/leungll/Emberling/backend/internal/config/readiness"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// TestAPI_Health_Live_Returns200 covers /health as pure process liveness
// (docs/10-ops.md's liveness/readiness split): it must answer 200 whether or not the
// startup gate has ever run, since a process that has not yet passed Ready is still
// alive.
func TestAPI_Health_Live_Returns200(t *testing.T) {
	probe := readiness.NewProbe()
	router := api.NewRouter(api.Deps{Readiness: probe})
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /health status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

// TestAPI_Health_AfterShutdownBegins_Returns503 covers the one condition that turns
// liveness false: BeginShutdown, independent of Ready (docs/10-ops.md).
func TestAPI_Health_AfterShutdownBegins_Returns503(t *testing.T) {
	probe := readiness.NewProbe()
	probe.BeginShutdown()
	router := api.NewRouter(api.Deps{Readiness: probe})
	server := httptest.NewServer(router)
	defer server.Close()

	resp, _ := doGet(t, server, "/health")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /health after BeginShutdown status = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
}

// TestAPI_Ready_BeforeGateRuns_Returns503DependencyUnavailable covers the readiness
// contract before any startup check has ever executed: this is the state a real process
// is in for the entire duration of readiness.Probe.Run, so a load balancer must not route
// traffic here (docs/10-ops.md §1, step 7 "accept_requests" is the last of seven).
func TestAPI_Ready_BeforeGateRuns_Returns503DependencyUnavailable(t *testing.T) {
	probe := readiness.NewProbe()
	router := api.NewRouter(api.Deps{Readiness: probe})
	server := httptest.NewServer(router)
	defer server.Close()

	resp, body := doGet(t, server, "/ready")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /ready before gate status = %d, want %d, body=%s", resp.StatusCode, http.StatusServiceUnavailable, body)
	}
	if code := errorCode(t, body); code != "DEPENDENCY_UNAVAILABLE" {
		t.Errorf("GET /ready before gate error.code = %q, want %q", code, "DEPENDENCY_UNAVAILABLE")
	}
}

// TestAPI_Ready_DatabaseCheckFails_Returns503 proves the gate itself, not merely "never
// ran", drives /ready: a Probe whose Database step fails must report the same 503 as one
// that never attempted the gate at all, and must never advance to Ready.
func TestAPI_Ready_DatabaseCheckFails_Returns503(t *testing.T) {
	probe := readiness.NewProbe()
	err := probe.Run(context.Background(), readiness.Checks{
		Database: func(context.Context) error { return errUnavailable },
	}, noopObserver{})
	if err == nil {
		t.Fatal("probe.Run with a failing Database check: want error, got nil")
	}

	router := api.NewRouter(api.Deps{Readiness: probe})
	server := httptest.NewServer(router)
	defer server.Close()

	resp, body := doGet(t, server, "/ready")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /ready with failed Database check status = %d, want %d, body=%s", resp.StatusCode, http.StatusServiceUnavailable, body)
	}
}

// TestAPI_Ready_RegistryCheckFails_Returns503 covers docs/09-testing-and-acceptance.md
// §3.9's Registry row: an inconsistent Tool/Node registration (Metadata, Executor or
// ExecutionKind mismatch) fails the registry_registration step, and the Backend must not
// become ready. Steps 1 and 2 pass here on purpose, so the test proves the gate stopped
// at step 3 specifically, not merely that "some" step failed. The "Backend does not
// accept new Runs" half of the row is structural: cmd/emberling returns the StepError
// before api.NewRouter is ever constructed, so a process that fails this step serves no
// /api route at all; the contract-observable fact is /ready answering 503 with
// DEPENDENCY_UNAVAILABLE and the Probe never reaching Ready.
func TestAPI_Ready_RegistryCheckFails_Returns503(t *testing.T) {
	errRegistry := &staticError{"tool registration metadata does not match its executor"}
	probe := readiness.NewProbe()
	err := probe.Run(context.Background(), readiness.Checks{
		Database:      func(context.Context) error { return nil },
		Configuration: func(context.Context) error { return nil },
		Registry:      func(context.Context) error { return errRegistry },
	}, noopObserver{})
	if err == nil {
		t.Fatal("probe.Run with a failing Registry check: want error, got nil")
	}
	var stepErr *readiness.StepError
	if !errors.As(err, &stepErr) {
		t.Fatalf("probe.Run error = %v (%T), want *readiness.StepError", err, err)
	}
	if stepErr.Step != readiness.StepRegistry {
		t.Errorf("failing step = %q, want %q", stepErr.Step, readiness.StepRegistry)
	}
	if !errors.Is(err, errRegistry) {
		t.Errorf("probe.Run error = %v, want it to wrap the Registry check's own error", err)
	}
	if probe.Ready() {
		t.Fatal("probe.Ready() = true after the Registry step failed, want false")
	}
	completed := probe.CompletedSteps()
	wantCompleted := []readiness.Step{readiness.StepDatabase, readiness.StepConfiguration}
	if len(completed) != len(wantCompleted) {
		t.Fatalf("CompletedSteps() = %v, want %v (gate must stop at registry_registration)", completed, wantCompleted)
	}
	for i, step := range wantCompleted {
		if completed[i] != step {
			t.Fatalf("CompletedSteps()[%d] = %q, want %q", i, completed[i], step)
		}
	}

	router := api.NewRouter(api.Deps{Readiness: probe})
	server := httptest.NewServer(router)
	defer server.Close()

	resp, body := doGet(t, server, "/ready")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /ready with failed Registry check status = %d, want %d, body=%s", resp.StatusCode, http.StatusServiceUnavailable, body)
	}
	if code := errorCode(t, body); code != "DEPENDENCY_UNAVAILABLE" {
		t.Errorf("GET /ready with failed Registry check error.code = %q, want %q", code, "DEPENDENCY_UNAVAILABLE")
	}
}

// TestAPI_Ready_AfterFullGatePasses_Returns200 is the positive control for the two 503
// tests above: once every step of the fixed seven-step gate has passed, /ready must
// answer 200. This drives the same real registries/Node Types/work Pool assembly every
// other test in this package uses (newTestEnv), so it also incidentally proves the whole
// stack constructs and answers requests.
func TestAPI_Ready_AfterFullGatePasses_Returns200(t *testing.T) {
	env := newTestEnv(t)

	resp, body := doGet(t, env.server, "/ready")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ready after full gate status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
}

// TestAPI_Health_Live_WithFailedRunAndBacklog_Still200 covers docs/09-testing-and-
// acceptance.md §3.9's liveness row: business backlog or a single failed Run must never
// turn liveness (or, post-gate, readiness) false -- an orchestrator restart is not how
// Emberling handles business failure. The test observes both halves of the row against
// one fully wired Backend:
//
//  1. Backlog: a barrier stalls the Run's first Model call at the exact point production
//     code invokes external code, so the Backend verifiably holds unfinished business
//     work (a RUNNING Attempt plus downstream NodeRuns that cannot advance) while
//     /health and /ready are probed.
//  2. Failed Run: the Mock Provider Script then fails that Model call outright; the
//     fixture's nodes declare no ExecutionPolicy, so DecideRetry refuses a retry and the
//     Run deterministically reaches FAILED (bounded polling via waitForTerminal, no
//     arbitrary sleeps), after which both probes must still answer 200.
func TestAPI_Health_Live_WithFailedRunAndBacklog_Still200(t *testing.T) {
	env := newTestEnv(t)
	workflowID, version := createFixtureDefinition(t, env)

	b := newBarrierAtCall(1)
	env.provider.BeforeReturn = b.beforeReturn
	env.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFail}
	}

	resp, body := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version,
		"input": map[string]any{"document": "a document whose summary call will fail"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)
	if runID == "" {
		t.Fatalf("POST /api/runs response missing id: %s", body)
	}

	// The Run is now stalled mid-Attempt: unfinished business work exists by
	// construction, and neither probe may care.
	b.waitEntered(t, 5*time.Second)
	if resp, body := doGet(t, env.server, "/health"); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /health with backlogged Run status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	if resp, body := doGet(t, env.server, "/ready"); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ready with backlogged Run status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}

	b.Release()
	snap := env.waitForTerminal(t, runID, 10*time.Second)
	run, _ := snap["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "FAILED" {
		t.Fatalf("Run status = %q, want %q (the Script fails every Model call and the fixture permits no retry)", status, "FAILED")
	}

	if resp, body := doGet(t, env.server, "/health"); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /health with FAILED Run status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	if resp, body := doGet(t, env.server, "/ready"); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ready with FAILED Run status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
}

var errUnavailable = &staticError{"database unavailable"}

type staticError struct{ msg string }

func (e *staticError) Error() string { return e.msg }

func doGet(t *testing.T, server *httptest.Server, path string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(server.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp, body
}
