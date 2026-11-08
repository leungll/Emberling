//go:build integration

// Agent Trace Context Manifest contract: every claimed Turn of
// GET /runs/{runId}/nodes/{nodeRunId}/agent carries a `manifest` array whose items have
// exactly the member set Studio's ManifestItem type declares, and no item carries the
// request content it describes.
package contract

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// manifestItemKeys is the member set of Studio's ManifestItem type, sorted.
var manifestItemKeys = []string{"included", "kind", "label", "length", "omittedReason", "range", "ref", "sha256", "version"}

var manifestItemKinds = map[string]bool{
	"INSTRUCTIONS": true, "TASK_INPUT": true, "STATE": true,
	"TOOL_RESULT": true, "WORKSPACE_FILE": true, "ARTIFACT": true, "MATERIAL_RANGE": true,
}

// TestAgentTrace_Manifest_ShapeMatchesStudioType drives the two-round loop and checks the
// projected Manifest of both Turns: a present array on every claimed Turn, exactly the
// declared keys on every item with the declared JSON types, the lookup result recorded on
// the second Turn as a TOOL_RESULT referring to its Tool Attempt, and no omitted reason on
// an included item.
func TestAgentTrace_Manifest_ShapeMatchesStudioType(t *testing.T) {
	env := newTestEnv(t)
	runID, snapshot := runCompletedAgentLoop(t, env)
	status, body := getAgentTrace(t, env, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, want 200: %s", status, body)
	}

	var trace struct {
		Turns []struct {
			ID           string            `json:"id"`
			TurnNo       int               `json:"turnNo"`
			Manifest     []map[string]any  `json:"manifest"`
			ToolAttempts []json.RawMessage `json:"toolAttempts"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(body, &trace); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if len(trace.Turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(trace.Turns))
	}

	// The key must be present on every Turn, as an array, not merely absent for null.
	var rawTurns struct {
		Turns []map[string]json.RawMessage `json:"turns"`
	}
	if err := json.Unmarshal(body, &rawTurns); err != nil {
		t.Fatalf("decode turns: %v", err)
	}
	for i, turn := range rawTurns.Turns {
		raw, present := turn["manifest"]
		if !present {
			t.Fatalf("turns[%d] has no manifest key: %s", i, body)
		}
		if !strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
			t.Fatalf("turns[%d].manifest = %s, want an array on a claimed Turn", i, raw)
		}
	}

	kindsByTurn := make([]map[string]int, len(trace.Turns))
	for i, turn := range trace.Turns {
		kindsByTurn[i] = map[string]int{}
		if len(turn.Manifest) == 0 {
			t.Fatalf("turns[%d].manifest is empty", i)
		}
		for j, item := range turn.Manifest {
			keys := make([]string, 0, len(item))
			for key := range item {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			if strings.Join(keys, ",") != strings.Join(manifestItemKeys, ",") {
				t.Errorf("turns[%d].manifest[%d] keys = %v, want %v", i, j, keys, manifestItemKeys)
			}
			kind, _ := item["kind"].(string)
			if !manifestItemKinds[kind] {
				t.Errorf("turns[%d].manifest[%d].kind = %v, not a declared kind", i, j, item["kind"])
			}
			kindsByTurn[i][kind]++
			for _, key := range []string{"label", "ref", "version", "sha256"} {
				if _, ok := item[key].(string); !ok {
					t.Errorf("turns[%d].manifest[%d].%s = %v, want a string", i, j, key, item[key])
				}
			}
			if _, ok := item["length"].(float64); !ok {
				t.Errorf("turns[%d].manifest[%d].length = %v, want a number", i, j, item["length"])
			}
			included, ok := item["included"].(bool)
			if !ok {
				t.Errorf("turns[%d].manifest[%d].included = %v, want a boolean", i, j, item["included"])
			}
			switch reason := item["omittedReason"]; {
			case included && reason != nil:
				t.Errorf("turns[%d].manifest[%d] is included but carries omittedReason %v", i, j, reason)
			case !included && reason != "REFERENCE_ONLY":
				t.Errorf("turns[%d].manifest[%d] is omitted with reason %v, want REFERENCE_ONLY", i, j, reason)
			}
			if kind != "MATERIAL_RANGE" && item["range"] != nil {
				t.Errorf("turns[%d].manifest[%d] is a %s but carries range %v", i, j, kind, item["range"])
			}
		}
	}

	// The first request carried the instructions, the task input and the state; the
	// second added the lookup result, recorded by its Tool Attempt.
	for i, want := range []map[string]int{
		{"INSTRUCTIONS": 1, "TASK_INPUT": 1, "STATE": 1},
		{"INSTRUCTIONS": 1, "TASK_INPUT": 1, "STATE": 1, "TOOL_RESULT": 1},
	} {
		for kind, n := range want {
			if kindsByTurn[i][kind] != n {
				t.Errorf("turns[%d] has %d %s items, want %d: %v", i, kindsByTurn[i][kind], kind, n, kindsByTurn[i])
			}
		}
		if len(kindsByTurn[i]) != len(want) {
			t.Errorf("turns[%d] kinds = %v, want exactly %v", i, kindsByTurn[i], want)
		}
	}
	var firstAttempt struct {
		ID        string `json:"id"`
		AttemptNo int    `json:"attemptNo"`
	}
	if len(trace.Turns[0].ToolAttempts) != 1 {
		t.Fatalf("turns[0].toolAttempts = %d, want 1", len(trace.Turns[0].ToolAttempts))
	}
	if err := json.Unmarshal(trace.Turns[0].ToolAttempts[0], &firstAttempt); err != nil {
		t.Fatalf("decode attempt: %v", err)
	}
	for _, item := range trace.Turns[1].Manifest {
		if item["kind"] != "TOOL_RESULT" {
			continue
		}
		if item["ref"] != firstAttempt.ID || item["version"] != "1" || item["label"] != "lookup result" {
			t.Errorf("TOOL_RESULT item = %v, want ref %s, version \"1\", label \"lookup result\"", item, firstAttempt.ID)
		}
	}

	// The Manifest describes the request; it never carries it.
	for _, payload := range []string{agentTraceToolKey, "record for", agentTraceFinalOutput, "what is the answer?"} {
		if strings.Contains(string(body), payload) {
			t.Errorf("Agent Trace body carries request content %q through the manifest: %s", payload, body)
		}
	}
}
