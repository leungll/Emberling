//go:build integration

package integration

import (
	"strings"
	"testing"
)

// waitingApprovalViolations runs the invariant report's waiting_approval_has_pending check
// for runID and returns how many violations it reports. The report reads the Run from a
// psql variable, which becomes the query's parameter here.
func waitingApprovalViolations(t *testing.T, f *releaseFixture, runID string) int {
	t.Helper()
	check := strings.ReplaceAll(invariantCheckSQL(t, "v_waiting_approval_pending"), ":'run_id'", "$1")
	var n int
	if err := f.h.pool.QueryRow(f.h.ctx, "SELECT count(*) FROM ("+check+") AS violations", runID).Scan(&n); err != nil {
		t.Fatalf("run the waiting_approval_has_pending check: %v", err)
	}
	return n
}

// Invariant: a release request waiting for a person has a PENDING approval for as long as
// it waits; the report's own check does report a waiting Action whose
// approval is no longer PENDING.
func TestReleaseInvariant_WaitingApprovalAction_HasPendingApproval(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{bounds: humanWaitBounds})
	action := f.startToRelease(t, "wf_invariant_waiting_approval")
	if n := waitingApprovalViolations(t, f, f.run.ID); n != 0 {
		t.Fatalf("waiting Actions without a PENDING approval = %d, want 0", n)
	}

	// The check is not vacuous: an approval moved out of PENDING behind the Action's back
	// is reported.
	approval := approvalOfAction(f.h.ctx, t, f.h.uow, action.ID)
	if _, err := f.h.pool.Exec(f.h.ctx, `UPDATE approvals SET status = 'EXPIRED', decided_at = requested_at WHERE id = $1`, approval.ID); err != nil {
		t.Fatalf("expire the approval behind the Action: %v", err)
	}
	if n := waitingApprovalViolations(t, f, f.run.ID); n != 1 {
		t.Errorf("waiting Actions without a PENDING approval = %d, want the one whose approval was moved", n)
	}
}
