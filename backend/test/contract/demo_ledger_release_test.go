//go:build integration

package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// The demo's ledger-vs-record.sh compares a Run's release Attempts and occupations with
// the mock production service's own record. These tests drive the script against
// constructed data served by fake endpoints, so a record that disagrees with the ledger is
// proven to fail the comparison without a Compose stack.

// ledgerOperation is one release operation of the constructed ledger: its Attempt and
// what the Trace says production answered for it.
type ledgerOperation struct {
	attemptNo int
	status    string // the Attempt status
	errorCode string
	outcome   string // the release view's outcome
}

// ledgerOccupation is the constructed occupation of the release Action.
type ledgerOccupation struct {
	attemptNo int
	status    string
	reason    string
	unchecked string
}

// ledgerCase is one release Action of one Run and production's record lines.
type ledgerCase struct {
	actionStatus string
	operations   []ledgerOperation
	occupation   *ledgerOccupation
	// record lines as production would have written them, without seq.
	record []map[string]any
	// verification, when set, is the external id of a verify_patch Attempt bound to the
	// sandbox runner, and runner holds the runner's record lines, without seq.
	verification string
	runner       []map[string]any
}

const ledgerAction = "act_ledger"

func ledgerKey(attemptNo int) string { return fmt.Sprintf("op_%s_%d", ledgerAction, attemptNo) }

// trace builds the Agent Trace slice the script reads.
func (c ledgerCase) trace() map[string]any {
	var attempts, operations []map[string]any
	for _, op := range c.operations {
		attempt := map[string]any{
			"id": fmt.Sprintf("ta_%d", op.attemptNo), "toolName": "deploy_change", "attemptNo": op.attemptNo, "status": op.status,
			"callbackBinding": map[string]any{"providerId": "release", "externalTaskId": ledgerKey(op.attemptNo)},
		}
		if op.errorCode != "" {
			attempt["error"] = map[string]any{"code": op.errorCode, "message": "constructed"}
		}
		attempts = append(attempts, attempt)
		operations = append(operations, map[string]any{
			"operationKey": ledgerKey(op.attemptNo), "toolAttemptId": attempt["id"], "attemptNo": op.attemptNo,
			"status": op.status, "outcome": op.outcome,
		})
	}
	release := map[string]any{"operations": operations, "occupation": nil, "humanWait": nil}
	if occ := c.occupation; occ != nil {
		occupation := map[string]any{
			"id": "occupation_1", "targetKey": "token-service/staging", "operationKey": ledgerKey(occ.attemptNo),
			"status": occ.status, "releaseReason": nil, "nextQueryAt": nil, "queryCount": 1, "deployedUncheckedVersion": nil,
		}
		if occ.reason != "" {
			occupation["releaseReason"] = occ.reason
		}
		if occ.unchecked != "" {
			occupation["deployedUncheckedVersion"] = occ.unchecked
		}
		release["occupation"] = occupation
	}
	turns := []map[string]any{}
	if c.verification != "" {
		turns = append(turns, map[string]any{
			"turnNo":   0,
			"decision": map[string]any{"kind": "TOOL_CALL", "tool": "verify_patch"},
			"action":   map[string]any{"id": "act_verify", "status": "SUCCEEDED"},
			"toolAttempts": []map[string]any{{
				"id": "ta_verify", "toolName": "verify_patch", "attemptNo": 1, "status": "SUCCEEDED",
				"callbackBinding": map[string]any{"providerId": "sandboxrunner", "externalTaskId": c.verification},
			}},
		})
	}
	return map[string]any{
		"agentRun": map[string]any{"termination": "FINAL_RESPONSE"},
		"turns": append(turns, map[string]any{
			"turnNo":       1,
			"decision":     map[string]any{"kind": "TOOL_CALL", "tool": "deploy_change"},
			"action":       map[string]any{"id": ledgerAction, "status": c.actionStatus, "release": release},
			"toolAttempts": attempts,
		}),
	}
}

// events are the Run's committed Events the script reads: a PROVIDER_POLL completion for
// every SUCCEEDED Attempt.
func (c ledgerCase) events() []map[string]any {
	var out []map[string]any
	if c.verification != "" {
		out = append(out, map[string]any{
			"seq": 1, "type": "AGENT_ACTION_COMPLETED",
			"payload": map[string]any{"completionSource": "CALLBACK", "toolAttemptId": "ta_verify"},
		})
	}
	for _, op := range c.operations {
		if op.status == "SUCCEEDED" {
			out = append(out, map[string]any{
				"seq": len(out) + 1, "type": "AGENT_ACTION_COMPLETED",
				"payload": map[string]any{"completionSource": "PROVIDER_POLL", "toolAttemptId": fmt.Sprintf("ta_%d", op.attemptNo)},
			})
		}
	}
	return out
}

// serveRecord answers GET /control/record?after=N with the lines whose seq is above N.
func serveRecord(lines []map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		after, _ := strconv.Atoi(r.URL.Query().Get("after"))
		for i, line := range lines {
			if i+1 <= after {
				continue
			}
			numbered := map[string]any{"seq": i + 1}
			for k, v := range line {
				numbered[k] = v
			}
			raw, _ := json.Marshal(numbered)
			_, _ = w.Write(append(raw, '\n'))
		}
	}
}

// runLedger serves c through fake Backend, Mock Provider and production endpoints, runs
// ledger-vs-record.sh over the one Run and returns its output and exit status.
func runLedger(t *testing.T, c ledgerCase) (string, int) {
	t.Helper()
	for _, tool := range []string{"bash", "jq", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is required to run the demo script: %v", tool, err)
		}
	}
	const runID, nodeRunID = "run_ledger", "nr_ledger"
	backend := http.NewServeMux()
	backend.HandleFunc("GET /api/runs/"+runID, func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, map[string]any{
			"run":      map[string]any{"status": "FAILED"},
			"nodeRuns": []map[string]any{{"id": nodeRunID, "nodeId": "node_agent", "nodeType": "agent"}},
		})
	})
	backend.HandleFunc("GET /api/runs/"+runID+"/events", func(w http.ResponseWriter, r *http.Request) {
		items := c.events()
		if r.URL.Query().Get("afterSeq") != "0" {
			items = nil
		}
		writeTestJSON(w, map[string]any{"items": items})
	})
	backend.HandleFunc("GET /api/runs/"+runID+"/nodes/"+nodeRunID, func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, map[string]any{"attempts": []any{}})
	})
	backend.HandleFunc("GET /api/runs/"+runID+"/nodes/"+nodeRunID+"/agent", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, c.trace())
	})
	backendHTTP := httptest.NewServer(backend)
	t.Cleanup(backendHTTP.Close)

	mock := http.NewServeMux()
	mock.HandleFunc("GET /control/record", serveRecord(nil))
	mock.HandleFunc("GET /control/inflight", func(w http.ResponseWriter, _ *http.Request) { writeTestJSON(w, map[string]any{}) })
	mockHTTP := httptest.NewServer(mock)
	t.Cleanup(mockHTTP.Close)

	runner := http.NewServeMux()
	runner.HandleFunc("GET /control/record", serveRecord(c.runner))
	runnerHTTP := httptest.NewServer(runner)
	t.Cleanup(runnerHTTP.Close)

	production := http.NewServeMux()
	production.HandleFunc("GET /control/record", serveRecord(c.record))
	productionHTTP := httptest.NewServer(production)
	t.Cleanup(productionHTTP.Close)

	cmd := exec.Command("bash", "../../../scripts/demo/ledger-vs-record.sh", runID, "0")
	cmd.Env = append(os.Environ(),
		"EMBERLING_DEMO_BACKEND_URL="+backendHTTP.URL, "EMBERLING_DEMO_MOCK_URL="+mockHTTP.URL,
		"EMBERLING_DEMO_PRODUCTION_URL="+productionHTTP.URL, "EMBERLING_DEMO_RUNNER_URL="+runnerHTTP.URL,
		"EMBERLING_MODEL_PROVIDER_API_KEY=unused", "EMBERLING_CALLBACK_SIGNING_SECRET=unused",
		"LEDGER_SUBMISSIONS=", "LEDGER_REQUIRE_COMPLETED=0")
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exitErr):
		return string(out), exitErr.ExitCode()
	default:
		t.Fatalf("run ledger-vs-record.sh: %v\n%s", err, out)
		return "", -1
	}
}

func writeTestJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// releaseLine is one release line of production's record about the n-th operation.
func releaseLine(event string, attemptNo int, extra map[string]any) map[string]any {
	line := map[string]any{"event": event, "kind": "release", "operationId": ledgerKey(attemptNo)}
	for k, v := range extra {
		line[k] = v
	}
	return line
}

// deployedOnce is production's record of the n-th operation accepted and deployed as version.
func deployedOnce(attemptNo int, version string) []map[string]any {
	return []map[string]any{
		releaseLine("arrived", attemptNo, map[string]any{"service": "token-service", "environment": "staging"}),
		releaseLine("responded", attemptNo, map[string]any{"status": 202}),
		releaseLine("deployed", attemptNo, map[string]any{"patchDigest": "d2d2", "version": version, "replayed": false}),
	}
}

// failedChecks returns the names of the checks the report marked FAIL.
func failedChecks(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(strings.TrimPrefix(line, "FAIL ")); strings.HasPrefix(line, "FAIL ") && len(fields) > 0 {
			names = append(names, fields[0])
		}
	}
	return names
}

func assertLedgerPasses(t *testing.T, c ledgerCase) {
	t.Helper()
	out, status := runLedger(t, c)
	if status != 0 || len(failedChecks(out)) != 0 {
		t.Fatalf("ledger-vs-record.sh exit %d with failed checks %v, want every check passed\n%s", status, failedChecks(out), out)
	}
	for _, want := range []string{"PASS release_operations_match_record", "PASS occupation_traces_to_operation"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
}

func assertLedgerFails(t *testing.T, c ledgerCase, check string) {
	t.Helper()
	out, status := runLedger(t, c)
	failed := failedChecks(out)
	if status != 1 || !strings.Contains(strings.Join(failed, " "), check) {
		t.Fatalf("ledger-vs-record.sh exit %d with failed checks %v, want exit 1 with %s failed\n%s", status, failed, check, out)
	}
}

func TestDemoLedger_DeployedRelease_RecordAgrees_Passes(t *testing.T) {
	assertLedgerPasses(t, ledgerCase{
		actionStatus: "SUCCEEDED",
		operations:   []ledgerOperation{{attemptNo: 1, status: "SUCCEEDED", outcome: "DEPLOYED"}},
		occupation:   &ledgerOccupation{attemptNo: 1, status: "RELEASED", reason: "DEPLOYED"},
		record:       deployedOnce(1, "v1"),
	})
}

// TestDemoLedger_StoppedReleases_SettledAfterTheTask_Pass covers the two operations a
// person stopped waiting for: one production never received and voided afterwards, one it
// deployed afterwards with the version the occupation records as unchecked.
func TestDemoLedger_StoppedReleases_SettledAfterTheTask_Pass(t *testing.T) {
	t.Run("voided", func(t *testing.T) {
		assertLedgerPasses(t, ledgerCase{
			actionStatus: "FAILED",
			operations:   []ledgerOperation{{attemptNo: 1, status: "FAILED", errorCode: "RELEASE_UNKNOWN", outcome: "UNKNOWN"}},
			occupation:   &ledgerOccupation{attemptNo: 1, status: "RELEASED", reason: "VOIDED"},
			record:       []map[string]any{releaseLine("voided", 1, nil)},
		})
	})
	t.Run("deployed", func(t *testing.T) {
		assertLedgerPasses(t, ledgerCase{
			actionStatus: "FAILED",
			operations:   []ledgerOperation{{attemptNo: 1, status: "FAILED", errorCode: "RELEASE_UNKNOWN", outcome: "UNKNOWN"}},
			occupation:   &ledgerOccupation{attemptNo: 1, status: "RELEASED", reason: "DEPLOYED", unchecked: "v3"},
			record:       deployedOnce(1, "v3"),
		})
	})
}

func TestDemoLedger_SucceededAttemptWithoutDeployedLine_Fails(t *testing.T) {
	assertLedgerFails(t, ledgerCase{
		actionStatus: "SUCCEEDED",
		operations:   []ledgerOperation{{attemptNo: 1, status: "SUCCEEDED", outcome: "DEPLOYED"}},
		occupation:   &ledgerOccupation{attemptNo: 1, status: "RELEASED", reason: "DEPLOYED"},
		record:       deployedOnce(1, "v1")[:2],
	}, "release_operations_match_record")
}

func TestDemoLedger_SecondDeployedUnderOneAction_Fails(t *testing.T) {
	assertLedgerFails(t, ledgerCase{
		actionStatus: "SUCCEEDED",
		operations: []ledgerOperation{
			{attemptNo: 1, status: "FAILED", errorCode: "VOIDED", outcome: "VOIDED"},
			{attemptNo: 2, status: "SUCCEEDED", outcome: "DEPLOYED"},
		},
		occupation: &ledgerOccupation{attemptNo: 2, status: "RELEASED", reason: "DEPLOYED"},
		record:     append(append(deployedOnce(1, "v1"), releaseLine("voided", 1, nil)), deployedOnce(2, "v2")...),
	}, "release_deployed_once_per_action")
}

// TestDemoLedger_VoidedAttemptWithoutProductionVoid_Fails is a ledger that says production
// voided an operation its record never voided.
func TestDemoLedger_VoidedAttemptWithoutProductionVoid_Fails(t *testing.T) {
	assertLedgerFails(t, ledgerCase{
		actionStatus: "FAILED",
		operations:   []ledgerOperation{{attemptNo: 1, status: "FAILED", errorCode: "VOIDED", outcome: "VOIDED"}},
		occupation:   &ledgerOccupation{attemptNo: 1, status: "RELEASED", reason: "NOT_DEPLOYED"},
		record:       []map[string]any{releaseLine("arrived", 1, nil)},
	}, "release_operations_match_record")
}

func TestDemoLedger_RefusedAttemptWithoutRefusal_Fails(t *testing.T) {
	assertLedgerFails(t, ledgerCase{
		actionStatus: "FAILED",
		operations:   []ledgerOperation{{attemptNo: 1, status: "FAILED", errorCode: "OUTSIDE_RELEASE_WINDOW", outcome: "REFUSED"}},
		occupation:   &ledgerOccupation{attemptNo: 1, status: "RELEASED", reason: "REFUSED"},
		record:       deployedOnce(1, "v1")[:2],
	}, "release_operations_match_record")
}

// TestDemoLedger_OccupationNotTraceable_Fails covers occupations freed for a reason
// production's record of their operation does not support.
func TestDemoLedger_OccupationNotTraceable_Fails(t *testing.T) {
	t.Run("freed as voided but deployed", func(t *testing.T) {
		assertLedgerFails(t, ledgerCase{
			actionStatus: "FAILED",
			operations:   []ledgerOperation{{attemptNo: 1, status: "FAILED", errorCode: "RELEASE_UNKNOWN", outcome: "UNKNOWN"}},
			occupation:   &ledgerOccupation{attemptNo: 1, status: "RELEASED", reason: "VOIDED"},
			record:       deployedOnce(1, "v1"),
		}, "occupation_traces_to_operation")
	})
	t.Run("unchecked version is not the deployed one", func(t *testing.T) {
		assertLedgerFails(t, ledgerCase{
			actionStatus: "FAILED",
			operations:   []ledgerOperation{{attemptNo: 1, status: "FAILED", errorCode: "RELEASE_UNKNOWN", outcome: "UNKNOWN"}},
			occupation:   &ledgerOccupation{attemptNo: 1, status: "RELEASED", reason: "DEPLOYED", unchecked: "v9"},
			record:       deployedOnce(1, "v3"),
		}, "occupation_traces_to_operation")
	})
	t.Run("names an operation the action never made", func(t *testing.T) {
		assertLedgerFails(t, ledgerCase{
			actionStatus: "SUCCEEDED",
			operations:   []ledgerOperation{{attemptNo: 1, status: "SUCCEEDED", outcome: "DEPLOYED"}},
			occupation:   &ledgerOccupation{attemptNo: 2, status: "RELEASED", reason: "DEPLOYED"},
			record:       deployedOnce(1, "v1"),
		}, "occupation_traces_to_operation")
	})
}

// TestDemoLedger_ProductionOperationOutsideLedger_Fails is a record holding an operation
// of the Run's release Action that no Attempt of the ledger made.
func TestDemoLedger_ProductionOperationOutsideLedger_Fails(t *testing.T) {
	assertLedgerFails(t, ledgerCase{
		actionStatus: "SUCCEEDED",
		operations:   []ledgerOperation{{attemptNo: 1, status: "SUCCEEDED", outcome: "DEPLOYED"}},
		occupation:   &ledgerOccupation{attemptNo: 1, status: "RELEASED", reason: "DEPLOYED"},
		record:       append(deployedOnce(1, "v1"), releaseLine("arrived", 2, nil), releaseLine("voided", 2, nil)),
	}, "release_operations_match_record")
}

// TestDemoLedger_LargeRecords_ComparedWithoutArgumentLimit runs the comparison over runner
// and production records of ten thousand lines each, several megabytes, as a real runner
// writes in one demo. The records never travel on a command line, so the system's argument
// length limit cannot fail the ledger.
func TestDemoLedger_LargeRecords_ComparedWithoutArgumentLimit(t *testing.T) {
	const lines = 10000
	padding := strings.Repeat("x", 240)
	runner := []map[string]any{
		{"event": "arrived", "kind": "verification", "verificationId": "ver_ledger"},
		{"event": "started", "kind": "verification", "verificationId": "ver_ledger"},
		{"event": "completed", "kind": "verification", "verificationId": "ver_ledger"},
		{"event": "callback", "testId": "ver_ledger", "httpStatus": 200},
	}
	record := deployedOnce(1, "v1")
	for i := 0; i < lines; i++ {
		runner = append(runner, map[string]any{"event": "log", "kind": "verification", "verificationId": fmt.Sprintf("ver_other_%d", i), "detail": padding})
		record = append(record, map[string]any{"event": "health", "kind": "release", "service": "other", "detail": padding})
	}
	assertLedgerPasses(t, ledgerCase{
		actionStatus: "SUCCEEDED",
		operations:   []ledgerOperation{{attemptNo: 1, status: "SUCCEEDED", outcome: "DEPLOYED"}},
		occupation:   &ledgerOccupation{attemptNo: 1, status: "RELEASED", reason: "DEPLOYED"},
		record:       record,
		verification: "ver_ledger",
		runner:       runner,
	})
}
