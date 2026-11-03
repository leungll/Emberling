//go:build integration

package contract

import (
	"strings"
	"testing"
)

// A verification bound to the sandbox runner may be completed by the Provider poll instead
// of the callback: the runner's status route already carries a finished result while the
// callback is still on its way. The ledger then judges that completion by the runner's own
// record of the status queries it answered, not by the Mock Provider's record.

// runnerVerificationLines is the runner's record of verification ver_ledger arriving,
// running and finishing, followed by the given status-query answers and one callback
// delivery the Backend answered as a duplicate.
func runnerVerificationLines(polls ...string) []map[string]any {
	lines := []map[string]any{
		{"event": "arrived", "kind": "verification", "verificationId": "ver_ledger"},
		{"event": "started", "kind": "verification", "verificationId": "ver_ledger"},
	}
	for _, status := range polls {
		lines = append(lines, map[string]any{"event": "polled", "kind": "verification", "verificationId": "ver_ledger", "status": 200, "taskStatus": status})
	}
	return append(lines,
		map[string]any{"event": "completed", "kind": "verification", "verificationId": "ver_ledger"},
		map[string]any{"event": "callback", "testId": "ver_ledger", "httpStatus": 200},
	)
}

func pollCompletedVerification(runner []map[string]any) ledgerCase {
	return ledgerCase{
		actionStatus:       "SUCCEEDED",
		operations:         []ledgerOperation{{attemptNo: 1, status: "SUCCEEDED", outcome: "DEPLOYED"}},
		occupation:         &ledgerOccupation{attemptNo: 1, status: "RELEASED", reason: "DEPLOYED"},
		record:             deployedOnce(1, "v1"),
		verification:       "ver_ledger",
		verificationSource: "PROVIDER_POLL",
		runner:             runner,
	}
}

func TestDemoLedger_RunnerVerificationCompletedByPoll_RunnerAnsweredFinished_Passes(t *testing.T) {
	c := pollCompletedVerification(runnerVerificationLines("RUNNING", "SUCCEEDED"))
	assertLedgerPasses(t, c)
	out, _ := runLedger(t, c)
	if !strings.Contains(out, "runner_polled=2 runner_finished=1 runner_running=1 runner_poll_completions=1") {
		t.Fatalf("poll check does not count the runner's answers\n%s", out)
	}
	if !strings.Contains(out, "further 2xx callback delivery(ies) answered as duplicates, no new Event") {
		t.Fatalf("the callback delivered after the poll completion is not noted as a duplicate\n%s", out)
	}
}

func TestDemoLedger_RunnerVerificationCompletedByPoll_NoFinishedAnswer_Fails(t *testing.T) {
	assertLedgerFails(t, pollCompletedVerification(runnerVerificationLines("RUNNING")), "poll_answers_cover_completions")
}

// A finished answer about another verification does not cover this one.
func TestDemoLedger_RunnerVerificationCompletedByPoll_AnswerForAnotherID_Fails(t *testing.T) {
	runner := runnerVerificationLines()
	runner = append(runner, map[string]any{"event": "polled", "kind": "verification", "verificationId": "ver_other", "status": 200, "taskStatus": "SUCCEEDED"})
	assertLedgerFails(t, pollCompletedVerification(runner), "poll_answers_cover_completions")
}
