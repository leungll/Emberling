//go:build integration

// Carry-over execution coverage for three acceptance scenarios that the existing
// execution integration tests leave unproven:
//
//   - A successful claim of an ordinary EXECUTOR NodeRun writes a NODE_STARTED that
//     carries attemptNo: the Event payload itself, decoded as JSON, must carry the
//     Attempt's number -- including after a retry, where the second NODE_STARTED of the
//     same node must carry attemptNo 2.
//   - Two different Runs with READY work at once may be advanced concurrently by the worker
//     pool, and the Run lock does not serialize across Runs: each Run's claim touches only
//     its own NodeRuns.
//   - A long DAG that keeps producing runnable nodes must not grow call-stack depth with
//     the node count: a 200-node linear DAG driven by the same Advance/Execute loop the
//     work pool and Reconciler use.
//
// Everything here reuses execHarness and its helpers from execution_test.go unchanged.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
)

// ---------------------------------------------------------------------------
// Helpers specific to these three scenarios
// ---------------------------------------------------------------------------

// execNodeStartedPayload decodes only the field this file asserts on. AttemptNo is a
// pointer so a payload that omits `attemptNo` entirely is distinguishable from one that
// carries 0 -- both are contract violations, but they fail with different messages.
type execNodeStartedPayload struct {
	AttemptNo *int `json:"attemptNo"`
}

// execListAllEvents pages through the whole Event log of runID. The shared listEvents
// helper caps at 100 rows, which the 200-node DAG below exceeds by a factor of six.
func execListAllEvents(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID string) []domain.Event {
	t.Helper()
	const page = 200
	var out []domain.Event
	after := int64(0)
	for {
		var batch []domain.Event
		if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			batch, err = tx.Events().ListAfter(ctx, runID, after, page)
			return err
		}); err != nil {
			t.Fatalf("list events of %s after seq %d: %v", runID, after, err)
		}
		out = append(out, batch...)
		if len(batch) < page {
			return out
		}
		after = batch[len(batch)-1].Seq
	}
}

func execListAttempts(ctx context.Context, t *testing.T, uow store.UnitOfWork, nodeRunID string) []domain.NodeAttempt {
	t.Helper()
	var out []domain.NodeAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		out, err = tx.NodeAttempts().ListByNodeRun(ctx, nodeRunID)
		return err
	}); err != nil {
		t.Fatalf("list attempts of node run %s: %v", nodeRunID, err)
	}
	return out
}

// execRunOutputText decodes the Run's single `text` output port.
func execRunOutputText(t *testing.T, run domain.Run) string {
	t.Helper()
	var output map[string]json.RawMessage
	if err := json.Unmarshal(run.Output, &output); err != nil {
		t.Fatalf("decode run %s output: %v", run.ID, err)
	}
	var text string
	if err := json.Unmarshal(output["text"], &text); err != nil {
		t.Fatalf("decode run %s output text port: %v", run.ID, err)
	}
	return text
}

// ---------------------------------------------------------------------------
// 1. NODE_STARTED carries the Attempt's own attemptNo, on the first Attempt and on a
//    retried one.
// ---------------------------------------------------------------------------

func TestExecution_NodeStartedEvent_CarriesAttemptNo(t *testing.T) {
	h := newExecHarness(t)
	def := execWithPolicy(execDocDefinition("wf_node_started_attempt_no"), "generate",
		domain.ExecutionPolicy{TimeoutMs: 5000, MaxAttempts: 3, Backoff: domain.BackoffFixed})
	h.saveDefinition(def)
	run := h.createRun("wf_node_started_attempt_no", 1, `{"brief":"a small ember creature"}`)

	// Same transient-failure shape as TestExecution_RetryPermitted_...: the first Model
	// call fails definitively (not a timeout), so DecideRetry permits a second Attempt of
	// the "generate" node, which is what gives this Run a NODE_STARTED with attemptNo 2.
	var calls int32
	h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		if atomic.AddInt32(&calls, 1) == 1 {
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioFail}
		}
		return nil
	}

	h.drain(run.ID) // input, prompt, generate (first Attempt fails, retry scheduled)
	h.clock.Advance(2 * time.Second)
	h.drain(run.ID) // generate's second Attempt succeeds, then output

	got := getRun(h.ctx, t, h.uow, run.ID)
	if got.Status != domain.RunCompleted {
		t.Fatalf("run status: want COMPLETED, got %s (error=%v)", got.Status, got.Error)
	}

	nodeRuns := listNodeRuns(h.ctx, t, h.uow, run.ID)
	nodeIDByNodeRunID := make(map[string]string, len(nodeRuns))
	for _, nr := range nodeRuns {
		nodeIDByNodeRunID[nr.ID] = nr.NodeID
	}

	// Collect the attemptNo carried by each NODE_STARTED, in seq order, per NodeRun.
	startedByNodeRun := make(map[string][]int)
	for _, ev := range execListAllEvents(h.ctx, t, h.uow, run.ID) {
		if ev.Type != domain.EventNodeStarted {
			continue
		}
		if ev.NodeRunID == nil {
			t.Fatalf("NODE_STARTED event %s (seq %d): want a nodeRunId, got nil", ev.ID, ev.Seq)
		}
		var payload execNodeStartedPayload
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("decode NODE_STARTED payload of event %s (seq %d): %v", ev.ID, ev.Seq, err)
		}
		if payload.AttemptNo == nil {
			t.Fatalf("NODE_STARTED event %s (seq %d) payload has no \"attemptNo\" key: %s", ev.ID, ev.Seq, ev.Payload)
		}
		startedByNodeRun[*ev.NodeRunID] = append(startedByNodeRun[*ev.NodeRunID], *payload.AttemptNo)
	}
	if len(startedByNodeRun) != 4 {
		t.Fatalf("NODE_STARTED events: want one group per node run (4), got %d groups", len(startedByNodeRun))
	}

	// Every NODE_STARTED's attemptNo must equal the number of the persisted Attempt it
	// announced: one NODE_STARTED per Attempt, in the same order.
	for nodeRunID, gotNos := range startedByNodeRun {
		attempts := execListAttempts(h.ctx, t, h.uow, nodeRunID)
		if len(attempts) != len(gotNos) {
			t.Fatalf("node %q (%s): %d NODE_STARTED events but %d persisted Attempts",
				nodeIDByNodeRunID[nodeRunID], nodeRunID, len(gotNos), len(attempts))
		}
		for i, attempt := range attempts {
			if gotNos[i] != attempt.AttemptNo {
				t.Fatalf("node %q (%s) NODE_STARTED[%d]: attemptNo = %d, want %d (Attempt %s)",
					nodeIDByNodeRunID[nodeRunID], nodeRunID, i, gotNos[i], attempt.AttemptNo, attempt.ID)
			}
		}
	}

	// And the specific per-node expectation: every node's first Attempt is announced as
	// attemptNo 1, and only the retried "generate" node gets a second NODE_STARTED, which
	// must carry attemptNo 2.
	wantByNodeID := map[string][]int{
		"input":    {1},
		"prompt":   {1},
		"generate": {1, 2},
		"output":   {1},
	}
	for nodeID, want := range wantByNodeID {
		nr := execNodeRunByNodeID(t, nodeRuns, nodeID)
		gotNos := startedByNodeRun[nr.ID]
		if len(gotNos) != len(want) {
			t.Fatalf("node %q: want %d NODE_STARTED events with attemptNo %v, got %d with %v", nodeID, len(want), want, len(gotNos), gotNos)
		}
		for i := range want {
			if gotNos[i] != want[i] {
				t.Fatalf("node %q NODE_STARTED[%d]: attemptNo = %d, want %d (full sequence %v)", nodeID, i, gotNos[i], want[i], gotNos)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 2. Two different Runs with READY work advance concurrently: the Run aggregate lock is
//    per-Run, so neither claim touches the other Run's NodeRuns.
// ---------------------------------------------------------------------------

func TestExecution_TwoRuns_ConcurrentAdvance_EachProgressesIndependently(t *testing.T) {
	h := newExecHarness(t)
	h.saveDefinition(execDocDefinition("wf_two_runs_concurrent"))

	// Distinct inputs so each Run's final output proves it executed its own data, not the
	// other's.
	runs := [2]domain.Run{
		h.createRun("wf_two_runs_concurrent", 1, `{"brief":"first ember"}`),
		h.createRun("wf_two_runs_concurrent", 1, `{"brief":"second ember"}`),
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	var outcomes [2]service.AdvanceOutcome
	var errs [2]error

	wg.Add(len(runs))
	for i := range runs {
		go func(i int) {
			defer wg.Done()
			<-start // released by close(start): both goroutines contend, no sleeps
			outcomes[i], errs[i] = h.svc.Advance(h.ctx, runs[i].ID)
		}(i)
	}
	close(start)
	wg.Wait()

	for i := range runs {
		if errs[i] != nil {
			t.Fatalf("advance run %s: unexpected error: %v", runs[i].ID, errs[i])
		}
		if !outcomes[i].Claimed {
			t.Fatalf("advance run %s: want a claim (each Run has its own READY input NodeRun; the Run lock must not serialize across Runs), got none", runs[i].ID)
		}
		if outcomes[i].RunID != runs[i].ID {
			t.Fatalf("advance run %s: claimed a NodeRun of run %s instead", runs[i].ID, outcomes[i].RunID)
		}
	}
	if outcomes[0].NodeRunID == outcomes[1].NodeRunID {
		t.Fatalf("both Advance calls claimed the same NodeRun %s", outcomes[0].NodeRunID)
	}

	// Each Run owns exactly one claimed, RUNNING NodeRun with exactly one Attempt, and the
	// NodeRun the other goroutine claimed is not among its rows.
	for i := range runs {
		nodeRuns := listNodeRuns(h.ctx, t, h.uow, runs[i].ID)
		if len(nodeRuns) != 1 {
			t.Fatalf("run %s: want exactly 1 NodeRun after one Advance each, got %d", runs[i].ID, len(nodeRuns))
		}
		nr := nodeRuns[0]
		if nr.ID != outcomes[i].NodeRunID {
			t.Fatalf("run %s: claimed NodeRun %s but the Run's only NodeRun is %s", runs[i].ID, outcomes[i].NodeRunID, nr.ID)
		}
		if nr.Status != domain.NodeRunRunning {
			t.Fatalf("run %s node run %s status: want RUNNING, got %s", runs[i].ID, nr.ID, nr.Status)
		}
		if nr.AttemptCount != 1 {
			t.Fatalf("run %s node run %s attempt count: want exactly 1 (only its own Advance started an Attempt), got %d", runs[i].ID, nr.ID, nr.AttemptCount)
		}
		if other := outcomes[1-i].NodeRunID; nr.ID == other {
			t.Fatalf("run %s: its NodeRun %s was claimed by the other Run's Advance", runs[i].ID, other)
		}
	}

	// Both Runs, driven to the end by the same Advance/Execute loop the pool uses, reach
	// COMPLETED with their own input's output.
	wants := [2]string{"echo: Write about first ember", "echo: Write about second ember"}
	for i := range runs {
		h.execute(outcomes[i])
		h.drain(runs[i].ID)

		got := getRun(h.ctx, t, h.uow, runs[i].ID)
		if got.Status != domain.RunCompleted {
			t.Fatalf("run %s status: want COMPLETED, got %s (error=%v)", runs[i].ID, got.Status, got.Error)
		}
		if text := execRunOutputText(t, got); text != wants[i] {
			t.Fatalf("run %s output text: want %q, got %q", runs[i].ID, wants[i], text)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. A long linear DAG advances one transaction at a time, with call-stack depth
//    independent of node count.
// ---------------------------------------------------------------------------

// execLinearDefinition builds a chain of n nodes: text_input -> (n-2) x prompt_template ->
// text_output, every edge carrying the `text` port. Each template is the bare `{{text}}`
// placeholder, so the Run's output is exactly the brief it was created with no matter how
// long the chain is.
func execLinearDefinition(workflowID string, n int) domain.Definition {
	nodes := make([]domain.Node, 0, n)
	edges := make([]domain.Edge, 0, n-1)
	nodeID := func(i int) string { return fmt.Sprintf("n%03d", i) }

	for i := 0; i < n; i++ {
		switch i {
		case 0:
			nodes = append(nodes, domain.Node{ID: nodeID(i), Type: "text_input", Name: "Brief",
				Config: json.RawMessage(`{"inputKey":"brief","required":true}`)})
		case n - 1:
			nodes = append(nodes, domain.Node{ID: nodeID(i), Type: "text_output", Name: "Output",
				Config: json.RawMessage(`{}`)})
		default:
			nodes = append(nodes, domain.Node{ID: nodeID(i), Type: "prompt_template", Name: fmt.Sprintf("Step %d", i),
				Config: json.RawMessage(`{"template":"{{text}}"}`)})
		}
		if i > 0 {
			edges = append(edges, domain.Edge{
				ID: fmt.Sprintf("e%03d", i), Source: nodeID(i - 1), SourceHandle: "text",
				Target: nodeID(i), TargetHandle: "text",
			})
		}
	}

	return domain.Definition{
		WorkflowID:  workflowID,
		Version:     1,
		Name:        "Long Linear Chain",
		Description: fmt.Sprintf("linear chain of %d nodes", n),
		Nodes:       nodes,
		Edges:       edges,
		CreatedAt:   fixtureTime,
	}
}

func TestExecution_LongLinearDAG_StackDepthIndependentOfNodeCount(t *testing.T) {
	const nodeCount = 200

	h := newExecHarness(t)
	h.saveDefinition(execLinearDefinition("wf_long_linear_dag", nodeCount))
	run := h.createRun("wf_long_linear_dag", 1, `{"brief":"a small ember creature"}`)

	// Depth guard: the whole drive loop runs under a 256 KiB maximum goroutine stack. Each
	// node must be advanced by a fresh call and a fresh transaction (the work pool and the
	// Reconciler both re-enter Advance per work item), so stack depth stays constant
	// regardless of chain length. An implementation that instead recursed into the next
	// node -- one frame per node, 200 deep with a transaction live in each -- would exceed
	// this limit and crash the test rather than pass it silently.
	previousMaxStack := debug.SetMaxStack(256 << 10)
	defer debug.SetMaxStack(previousMaxStack)

	maxIterations := 2*nodeCount + 10
	claims := 0
	for i := 0; i < maxIterations; i++ {
		outcome, err := h.svc.Advance(h.ctx, run.ID)
		if err != nil {
			t.Fatalf("advance (claim %d): %v", claims, err)
		}
		if !outcome.Claimed {
			break
		}
		claims++
		if err := h.svc.Execute(h.ctx, outcome); err != nil {
			t.Fatalf("execute node_run=%s (claim %d): %v", outcome.NodeRunID, claims, err)
		}
	}
	if claims != nodeCount {
		t.Fatalf("claims driven: want exactly %d (one Advance per node), got %d", nodeCount, claims)
	}

	got := getRun(h.ctx, t, h.uow, run.ID)
	if got.Status != domain.RunCompleted {
		t.Fatalf("run status: want COMPLETED, got %s (error=%v)", got.Status, got.Error)
	}
	if text := execRunOutputText(t, got); text != "a small ember creature" {
		t.Fatalf("run output text: want %q, got %q", "a small ember creature", text)
	}

	nodeRuns := listNodeRuns(h.ctx, t, h.uow, run.ID)
	if len(nodeRuns) != nodeCount {
		t.Fatalf("node runs: want %d, got %d", nodeCount, len(nodeRuns))
	}
	for _, nr := range nodeRuns {
		if nr.Status != domain.NodeRunSucceeded {
			t.Fatalf("node run %s (node %s) status: want SUCCEEDED, got %s (error=%v)", nr.ID, nr.NodeID, nr.Status, nr.Error)
		}
	}

	events := execListAllEvents(h.ctx, t, h.uow, run.ID)
	if len(events) < 3*nodeCount {
		t.Fatalf("events: want at least %d (READY/STARTED/COMPLETED per node), got %d", 3*nodeCount, len(events))
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Fatalf("event seq not strictly increasing at index %d: %d -> %d", i, events[i-1].Seq, events[i].Seq)
		}
	}
}
