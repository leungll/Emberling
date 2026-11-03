//go:build integration

package contract

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The demo stories wait for steps only a running Run can take: a PENDING approval, an open
// human wait, a request held at a service. await_running in the demo library must stop such
// a wait as soon as the Run ends instead of waiting out its deadline. These tests source the
// library against a fake Backend serving one Run.

// runAwaitRunning serves a Run with the given status and failed NodeRun error code, runs
// await_running over it with COND as the condition and a deadline of SECONDS, and returns
// the combined output and exit status. A run_ended_hook prints what it was given.
func runAwaitRunning(t *testing.T, status, code, cond, seconds string) (string, int) {
	t.Helper()
	for _, tool := range []string{"bash", "jq", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is required to run the demo script: %v", tool, err)
		}
	}
	backend := http.NewServeMux()
	backend.HandleFunc("GET /api/runs/run_wait", func(w http.ResponseWriter, _ *http.Request) {
		nodeRun := map[string]any{"id": "nr_agent", "nodeId": "node_agent", "status": status, "error": nil}
		if code != "" {
			nodeRun["error"] = map[string]any{"code": code, "message": "constructed"}
		}
		writeTestJSON(w, map[string]any{"run": map[string]any{"id": "run_wait", "status": status}, "nodeRuns": []any{nodeRun}})
	})
	server := httptest.NewServer(backend)
	t.Cleanup(server.Close)

	// The script's own deadline is far beyond this one: the test fails when await_running
	// waits instead of returning on the Run's end.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	script := `source ../../../scripts/demo/lib.sh
run_ended_hook() { printf 'hook run=%s status=%s code=%s waiting=%s\n' "$1" "$2" "$3" "$4"; }
await_running run_wait "an approval to be PENDING" "` + seconds + `" ` + cond + `
echo returned`
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Env = append(os.Environ(), "EMBERLING_DEMO_BACKEND_URL="+server.URL,
		"EMBERLING_MODEL_PROVIDER_API_KEY=unused", "EMBERLING_CALLBACK_SIGNING_SECRET=unused")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("await_running did not return within 60s\n%s", out)
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exitErr):
		return string(out), exitErr.ExitCode()
	default:
		t.Fatalf("run await_running: %v\n%s", err, out)
		return "", -1
	}
}

func TestDemoAwaitRunning_RunFailedWhileWaiting_ExitsAtOnceWithStatusAndCode(t *testing.T) {
	out, status := runAwaitRunning(t, "FAILED", "INVALID_ACTION", "false", "1800")
	if status != 1 {
		t.Fatalf("exit %d, want 1\n%s", status, out)
	}
	for _, want := range []string{
		"run run_wait ended FAILED (error INVALID_ACTION) while waiting for: an approval to be PENDING",
		"hook run=run_wait status=FAILED code=INVALID_ACTION waiting=an approval to be PENDING",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "returned") || strings.Contains(out, "timed out") {
		t.Errorf("await_running went on or timed out instead of stopping on the Run's end\n%s", out)
	}
}

func TestDemoAwaitRunning_RunCompletedWhileWaiting_Exits(t *testing.T) {
	out, status := runAwaitRunning(t, "COMPLETED", "", "false", "1800")
	if status != 1 || !strings.Contains(out, "ended COMPLETED (error none)") {
		t.Fatalf("exit %d, want 1 naming the COMPLETED Run\n%s", status, out)
	}
}

func TestDemoAwaitRunning_ConditionHolds_Returns(t *testing.T) {
	out, status := runAwaitRunning(t, "FAILED", "INVALID_ACTION", "true", "1800")
	if status != 0 || !strings.Contains(out, "returned") || strings.Contains(out, "hook") {
		t.Fatalf("exit %d, want the wait to return because its condition holds\n%s", status, out)
	}
}

func TestDemoAwaitRunning_RunStillRunning_TimesOut(t *testing.T) {
	out, status := runAwaitRunning(t, "RUNNING", "", "false", "0")
	if status == 0 || !strings.Contains(out, "timed out after 0s waiting for: an approval to be PENDING") || strings.Contains(out, "hook") {
		t.Fatalf("exit %d, want the deadline to fail the wait of a running Run\n%s", status, out)
	}
}
