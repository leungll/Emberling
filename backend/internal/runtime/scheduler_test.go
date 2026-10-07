package runtime

import (
	"context"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/domain"
)

func compilePlan(t *testing.T, fixture string) *CompiledDefinition {
	t.Helper()
	compiler, _ := newTestCompiler()
	def := loadFixture(t, fixture)
	plan, err := compiler.Compile(context.Background(), def)
	if err != nil {
		t.Fatalf("Compile(%s) unexpected error: %v", fixture, err)
	}
	return plan
}

func sorted(ss []string) []string {
	out := append([]string(nil), ss...)
	sort.Strings(out)
	return out
}

// TestScheduler_NextReady_OnlyWhenAllUpstreamSucceeded asserts a node only becomes ready
// once every one of its direct upstream dependencies has SUCCEEDED, using the linear
// document_processing.json chain: with only node_input SUCCEEDED, node_prompt (its sole
// downstream) is ready, but node_summary (two hops away) is not yet.
func TestScheduler_NextReady_OnlyWhenAllUpstreamSucceeded(t *testing.T) {
	plan := compilePlan(t, "document_processing.json")

	existing := map[string]NodeRunState{
		"node_input": {NodeID: "node_input", Status: domain.NodeRunSucceeded},
	}
	ready := NextReady(plan, existing)

	want := []string{"node_prompt"}
	if diff := cmp.Diff(want, sorted(ready)); diff != "" {
		t.Errorf("NextReady mismatch (-want +got):\n%s", diff)
	}
}

// TestScheduler_NextReady_NeverDuplicatesExistingNodeRun asserts NextReady never proposes
// a node id already present in existing, matching the (run_id, node_id) uniqueness
// invariant.
func TestScheduler_NextReady_NeverDuplicatesExistingNodeRun(t *testing.T) {
	plan := compilePlan(t, "document_processing.json")

	existing := map[string]NodeRunState{
		"node_input":  {NodeID: "node_input", Status: domain.NodeRunSucceeded},
		"node_prompt": {NodeID: "node_prompt", Status: domain.NodeRunReady},
	}
	ready := NextReady(plan, existing)

	for _, id := range ready {
		if _, has := existing[id]; has {
			t.Errorf("NextReady proposed %q, which already has a NodeRunState", id)
		}
	}
}

// TestScheduler_NextReady_NothingAfterFailure asserts the Run-wide stop rule: once ANY
// NodeRun in the Run is FAILED, NextReady returns nothing at all, even for a node in a
// completely different branch whose own upstream dependencies are all satisfied. FAILED
// is terminal and MVP has no cancellation, so a Run
// that already contains a FAILED NodeRun can never reach COMPLETED; scheduling more work
// on an unrelated branch (node_caption, downstream only of node_rewrite/node_brief) would
// be dead work with no recovery path. This uses aigc_media.json: node_image has FAILED
// while node_caption's own upstream (node_rewrite) has SUCCEEDED, so a purely
// per-node-dependency scheduler would incorrectly propose node_caption here.
func TestScheduler_NextReady_NothingAfterFailure(t *testing.T) {
	plan := compilePlan(t, "aigc_media.json")

	existing := map[string]NodeRunState{
		"node_brief":     {NodeID: "node_brief", Status: domain.NodeRunSucceeded},
		"node_reference": {NodeID: "node_reference", Status: domain.NodeRunSucceeded},
		"node_prompt":    {NodeID: "node_prompt", Status: domain.NodeRunSucceeded},
		"node_rewrite":   {NodeID: "node_rewrite", Status: domain.NodeRunSucceeded},
		"node_image":     {NodeID: "node_image", Status: domain.NodeRunFailed},
	}
	ready := NextReady(plan, existing)

	if len(ready) != 0 {
		t.Errorf("NextReady = %v, want empty (a FAILED NodeRun anywhere in the Run must stop all further scheduling, including node_caption in an unrelated branch)", ready)
	}
}

// TestScheduler_AIGCBranch_CaptionReadyWhileImageWaiting asserts NextReady evaluates each
// node's own dependencies independently: aigc_media.json has two sibling branches downstream
// of node_rewrite (node_image and node_caption). With node_image WAITING_CALLBACK (an
// async dispatch in flight) and every other upstream of node_caption SUCCEEDED,
// node_caption must be ready even though node_image is not -- an unrelated branch does
// not block scheduling (multiple concurrent READY/WAITING_CALLBACK NodeRuns are
// permitted).
func TestScheduler_AIGCBranch_CaptionReadyWhileImageWaiting(t *testing.T) {
	plan := compilePlan(t, "aigc_media.json")

	existing := map[string]NodeRunState{
		"node_brief":     {NodeID: "node_brief", Status: domain.NodeRunSucceeded},
		"node_reference": {NodeID: "node_reference", Status: domain.NodeRunSucceeded},
		"node_prompt":    {NodeID: "node_prompt", Status: domain.NodeRunSucceeded},
		"node_rewrite":   {NodeID: "node_rewrite", Status: domain.NodeRunSucceeded},
		"node_image":     {NodeID: "node_image", Status: domain.NodeRunWaitingCallback},
	}
	ready := NextReady(plan, existing)

	want := []string{"node_caption"}
	if diff := cmp.Diff(want, sorted(ready)); diff != "" {
		t.Errorf("NextReady mismatch (-want +got):\n%s", diff)
	}
}

// TestScheduler_SelectNext_RefusesSecondRunningSlot asserts SelectNextToExecute refuses
// to pick anything while existing already holds a RUNNING NodeRun, since MVP calls only
// one Node Executor per Run at a time.
func TestScheduler_SelectNext_RefusesSecondRunningSlot(t *testing.T) {
	plan := compilePlan(t, "aigc_media.json")

	existing := map[string]NodeRunState{
		"node_rewrite": {NodeID: "node_rewrite", Status: domain.NodeRunRunning},
	}
	ready := []string{"node_brief", "node_reference"}

	_, ok := SelectNextToExecute(plan, existing, ready)
	if ok {
		t.Error("SelectNextToExecute() = ok, want refused while a RUNNING NodeRun exists")
	}
}

// TestScheduler_SelectNext_RefusesAfterFailure asserts the Run-wide stop rule: once ANY
// NodeRun in the Run is FAILED, SelectNextToExecute refuses to claim anything at all --
// including a node that already reached READY before the failure landed, in an unrelated
// branch. FAILED is terminal and MVP has no cancellation, so dispatching already-READY work after a failure would still be dead work with
// no recovery path.
func TestScheduler_SelectNext_RefusesAfterFailure(t *testing.T) {
	plan := compilePlan(t, "aigc_media.json")

	existing := map[string]NodeRunState{
		"node_image": {NodeID: "node_image", Status: domain.NodeRunFailed},
	}
	// node_caption is already READY (an unrelated branch), unblocked by node_image's
	// failure -- must still be refused, not merely deprioritized.
	ready := []string{"node_caption"}

	_, ok := SelectNextToExecute(plan, existing, ready)
	if ok {
		t.Error("SelectNextToExecute() = ok, want refused once any NodeRun is FAILED")
	}
}

// TestScheduler_SelectNext_PicksFirstInStableOrder asserts that, absent any RUNNING
// NodeRun, SelectNextToExecute picks the ready node earliest in the plan's stable Order,
// not merely the first element of the ready slice as passed in (a caller might not sort
// it).
func TestScheduler_SelectNext_PicksFirstInStableOrder(t *testing.T) {
	plan := compilePlan(t, "aigc_media.json")

	ready := []string{"node_reference", "node_brief"} // deliberately out of Order
	id, ok := SelectNextToExecute(plan, nil, ready)
	if !ok {
		t.Fatal("SelectNextToExecute() = not ok, want a pick")
	}
	if id != "node_brief" {
		t.Errorf("SelectNextToExecute() = %q, want node_brief (earlier in stable Order)", id)
	}
}

// TestScheduler_InitialReady_IsDAGRoots asserts InitialReady returns exactly the nodes
// with no upstream dependency, in stable Order, the moment a Run is created.
func TestScheduler_InitialReady_IsDAGRoots(t *testing.T) {
	plan := compilePlan(t, "aigc_media.json")

	got := InitialReady(plan)
	want := []string{"node_brief", "node_reference"}
	if diff := cmp.Diff(want, sorted(got)); diff != "" {
		t.Errorf("InitialReady mismatch (-want +got):\n%s", diff)
	}
}
