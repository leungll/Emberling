//go:build integration

// Token usage contract: the Snapshot Run carries a read-time total of its NodeRuns'
// tokenUsage, and the Agent Trace exposes each Turn's reported usage. Both are nullable:
// an absent report is null, never a fabricated zero.
package contract

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// tokenUsageFieldNames are the JSON keys of a Token usage summary. A secrecy scan that
// looks for the substring "token" strips exactly these keys first, so counting usage stays
// allowed while any other "token" field, such as a callback token, still fails the scan.
var tokenUsageFieldNames = []string{`"tokenUsage"`, `"inputTokens"`, `"outputTokens"`, `"totalTokens"`}

func withoutTokenUsageFieldNames(raw string) string {
	for _, name := range tokenUsageFieldNames {
		raw = strings.ReplaceAll(raw, name, `""`)
	}
	return raw
}

type tokenUsageBody struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	TotalTokens  int `json:"totalTokens"`
}

// snapshotTokenUsage decodes the raw Snapshot so a missing key and an explicit null are
// told apart.
type snapshotTokenUsage struct {
	Run      map[string]json.RawMessage `json:"run"`
	NodeRuns []struct {
		NodeID     string          `json:"nodeId"`
		TokenUsage *tokenUsageBody `json:"tokenUsage"`
	} `json:"nodeRuns"`
}

func getSnapshotTokenUsage(t *testing.T, env *testEnv, runID string) snapshotTokenUsage {
	t.Helper()
	resp, body := env.doJSON(t, http.MethodGet, "/api/runs/"+runID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/runs/%s status = %d, want %d, body=%s", runID, resp.StatusCode, http.StatusOK, body)
	}
	return decodeBody[snapshotTokenUsage](t, body)
}

type agentTraceTurnUsage struct {
	Turns []struct {
		TurnNo int             `json:"turnNo"`
		Usage  *tokenUsageBody `json:"usage"`
	} `json:"turns"`
}

// TestRunSnapshot_TokenUsage_EqualsNodeRunSum pins the read-time total: after an Agent
// loop whose two Turns report usage, run.tokenUsage equals the field-by-field sum of the
// NodeRuns' tokenUsage, and the Agent Trace exposes each Turn's usage.
func TestRunSnapshot_TokenUsage_EqualsNodeRunSum(t *testing.T) {
	env := newTestEnv(t)
	runID, _ := runCompletedAgentLoop(t, env)
	snapshot := getSnapshotTokenUsage(t, env, runID)

	var want tokenUsageBody
	reported := 0
	for _, nr := range snapshot.NodeRuns {
		if nr.TokenUsage == nil {
			continue
		}
		reported++
		want.InputTokens += nr.TokenUsage.InputTokens
		want.OutputTokens += nr.TokenUsage.OutputTokens
		want.TotalTokens += nr.TokenUsage.TotalTokens
	}
	if reported == 0 || want.TotalTokens == 0 {
		t.Fatalf("no NodeRun reported usage: the Agent NodeRun must carry its Turns' sum; nodeRuns=%+v", snapshot.NodeRuns)
	}
	raw, ok := snapshot.Run["tokenUsage"]
	if !ok {
		t.Fatalf("snapshot run has no tokenUsage key: %v", snapshot.Run)
	}
	var got *tokenUsageBody
	if err := json.Unmarshal(raw, &got); err != nil || got == nil {
		t.Fatalf("snapshot run tokenUsage = %s, want an object (err %v)", raw, err)
	}
	if *got != want {
		t.Errorf("snapshot run tokenUsage = %+v, want the NodeRun sum %+v", *got, want)
	}

	nodeRunID := nodeRunIDOfSnapshot(t, env, runID, "node_agent")
	status, body := getAgentTrace(t, env, runID, nodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, body)
	}
	trace := decodeBody[agentTraceTurnUsage](t, body)
	if len(trace.Turns) != 2 {
		t.Fatalf("agent trace has %d turns, want 2", len(trace.Turns))
	}
	var turnSum tokenUsageBody
	for _, turn := range trace.Turns {
		if turn.Usage == nil {
			t.Fatalf("turn %d usage = null, want the reported usage", turn.TurnNo)
		}
		turnSum.InputTokens += turn.Usage.InputTokens
		turnSum.OutputTokens += turn.Usage.OutputTokens
		turnSum.TotalTokens += turn.Usage.TotalTokens
	}
	if turnSum != want {
		t.Errorf("sum of trace turn usage = %+v, want the Run total %+v: Agent usage reaches the Run only through its NodeRun",
			turnSum, want)
	}
}

// TestRunSnapshot_TokenUsage_NullWhenNoNodeRunReports covers the absence rule: when the
// model reports no usage the Run total is an explicit null and the Trace Turn usage is
// null as well.
func TestRunSnapshot_TokenUsage_NullWhenNoNodeRunReports(t *testing.T) {
	env := newTestEnv(t)
	env.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: "unmetered", OmitTokenUsage: true}
	}
	workflowID, version := saveAgentFixture(t, env)
	runID := createAgentRun(t, env, workflowID, version, "what is the answer?")
	terminal := env.waitForTerminal(t, runID, agentTraceWait)
	if run, _ := terminal["run"].(map[string]any); run["status"] != "COMPLETED" {
		t.Fatalf("agent Run terminal status = %v, want COMPLETED", run["status"])
	}

	snapshot := getSnapshotTokenUsage(t, env, runID)
	raw, ok := snapshot.Run["tokenUsage"]
	if !ok {
		t.Fatalf("snapshot run has no tokenUsage key: %v", snapshot.Run)
	}
	if string(raw) != "null" {
		t.Errorf("snapshot run tokenUsage = %s, want null when no NodeRun reported usage", raw)
	}

	nodeRunID := nodeRunIDOfSnapshot(t, env, runID, "node_agent")
	_, body := getAgentTrace(t, env, runID, nodeRunID)
	var trace struct {
		Turns []map[string]json.RawMessage `json:"turns"`
	}
	if err := json.Unmarshal(body, &trace); err != nil || len(trace.Turns) != 1 {
		t.Fatalf("agent trace turns = %s (err %v), want one Turn", body, err)
	}
	if usage, ok := trace.Turns[0]["usage"]; !ok || string(usage) != "null" {
		t.Errorf("trace turn usage = %s (present %v), want an explicit null", usage, ok)
	}
}

func nodeRunIDOfSnapshot(t *testing.T, env *testEnv, runID, nodeID string) string {
	t.Helper()
	resp, body := env.doJSON(t, http.MethodGet, "/api/runs/"+runID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/runs/%s status = %d, body=%s", runID, resp.StatusCode, body)
	}
	return nodeRunIDOf(t, decodeBody[map[string]any](t, body), nodeID)
}

// TestWithoutTokenUsageFieldNames_KeepsOtherTokenFieldsVisible proves the secrecy scans
// stay strict after the usage keys are stripped: any other key or value mentioning a token
// is still found.
func TestWithoutTokenUsageFieldNames_KeepsOtherTokenFieldsVisible(t *testing.T) {
	stripped := withoutTokenUsageFieldNames(
		`{"usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3},"tokenUsage":null}`)
	if strings.Contains(strings.ToLower(stripped), "token") {
		t.Errorf("usage keys survived stripping: %s", stripped)
	}
	for _, leak := range []string{`{"callbackToken":"x"}`, `{"note":"bearer token abc"}`, `{"inputTokensSecret":1}`} {
		if !strings.Contains(strings.ToLower(withoutTokenUsageFieldNames(leak)), "token") {
			t.Errorf("stripping hid a non-usage token mention in %s", leak)
		}
	}
}
