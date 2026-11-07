//go:build integration

package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/readmaterial"
)

// read_material contract: an Agent that reads a range of the transcript its Run
// transcribed sees that range in its next Turn, and the Agent Trace records the read as a
// MATERIAL_RANGE item carrying the artifact, its version, the lines and the digest of the
// text, with the exact member set Studio's type declares, while the transcript text
// itself appears nowhere in the Trace.

// transcriptSecondLine is the one line the Agent reads; the first line never leaves the
// artifact.
const transcriptSecondLine = "S2\t3.000\t6.000\tThen it catches.\n"

// saveReadMaterialDefinition is saveTranscriptDefinition with read_material allowed on
// the Agent beside lookup.
func saveReadMaterialDefinition(t *testing.T, env *testEnv) (string, int) {
	t.Helper()
	fx := loadAgentLookupFixture(t)
	var agentNode, outputNode json.RawMessage
	for _, node := range fx.Nodes {
		if node.ID == "node_agent" {
			var config map[string]any
			if err := json.Unmarshal(node.Config, &config); err != nil {
				t.Fatal(err)
			}
			config["allowedTools"] = []string{"lookup", readmaterial.ToolName}
			node.Config, _ = json.Marshal(config)
		}
		raw, _ := json.Marshal(node)
		switch node.ID {
		case "node_agent":
			agentNode = raw
		case "node_output":
			outputNode = raw
		}
	}
	nodes := []json.RawMessage{
		json.RawMessage(`{"id":"node_task","type":"task_input","name":"Task","config":{"inputKey":"task","required":true}}`),
		json.RawMessage(`{"id":"node_transcript","type":"media_transcript","name":"Transcript","config":{},"executionPolicy":{"timeoutMs":600000,"maxAttempts":1,"backoff":"FIXED"}}`),
		agentNode, outputNode,
	}
	edges := []map[string]any{
		{"id": "e1", "source": "node_task", "sourceHandle": "task", "target": "node_transcript", "targetHandle": "task"},
		{"id": "e2", "source": "node_transcript", "sourceHandle": "transcript", "target": "node_agent", "targetHandle": "input"},
		{"id": "e3", "source": "node_agent", "sourceHandle": "text", "target": "node_output", "targetHandle": "text"},
	}
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": "Transcript then reading agent", "description": "task_input -> media_transcript -> agent(read_material) -> text_output",
		"nodes": nodes, "edges": edges,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)
	return workflowID, int(version)
}

func TestReadMaterial_AgentReadsTranscriptRange_TraceRecordsMaterialRange(t *testing.T) {
	env, media := newTranscriptEnv(t)
	sum := sha256.Sum256([]byte(transcriptMaterial))
	materialSHA := hex.EncodeToString(sum[:])
	artifactID, err := domain.ArtifactIDForSHA256(materialSHA)
	if err != nil {
		t.Fatal(err)
	}
	// Turn 1 reads the second line of the transcript; Turn 2 finishes.
	var calls atomic.Int32
	env.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		if calls.Add(1) == 1 {
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioToolCall, ToolName: readmaterial.ToolName,
				ToolArguments: json.RawMessage(`{"artifactId":"` + artifactID + `","startLine":2,"lineCount":1}`)}
		}
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: "cut"}
	}
	workflowID, version := saveReadMaterialDefinition(t, env)

	resp, body := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version, "input": transcriptRunInput(),
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, want 201, body=%s", resp.StatusCode, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)
	var submit map[string]any
	select {
	case submit = <-media.received:
	case <-time.After(transcriptWait):
		t.Fatalf("the media service received no task within %s", transcriptWait)
	}
	taskID, _ := submit["taskId"].(string)
	token, _ := submit["callbackToken"].(string)
	callbackResp, callbackBody := env.doCallback(t, token, taskID, map[string]any{
		"status": "SUCCEEDED",
		"result": map[string]any{
			"sourceId": transcriptSourceID, "sourceSha256": transcriptSourceSHA, "transcriptSha256": materialSHA,
			"language": "en", "sentenceCount": 2, "lineCount": 2, "durationMs": transcriptDuration, "model": "small.en",
			"material": transcriptMaterial,
		},
	})
	if callbackResp.StatusCode != http.StatusAccepted && callbackResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/callbacks status = %d, body=%s", callbackResp.StatusCode, callbackBody)
	}
	snapshot := env.waitForTerminal(t, runID, transcriptWait)
	if run, _ := snapshot["run"].(map[string]any); run["status"] != "COMPLETED" {
		t.Fatalf("Run status = %v, want COMPLETED; snapshot=%v", run["status"], snapshot)
	}

	status, trace := getAgentTrace(t, env, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, trace)
	}
	var decoded struct {
		Turns []struct {
			Manifest     []map[string]any `json:"manifest"`
			ToolAttempts []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"toolAttempts"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(trace, &decoded); err != nil {
		t.Fatalf("decode trace: %v", err)
	}
	if len(decoded.Turns) != 2 || len(decoded.Turns[0].ToolAttempts) != 1 || decoded.Turns[0].ToolAttempts[0].Status != "SUCCEEDED" {
		t.Fatalf("turns = %+v, want two Turns with one SUCCEEDED read on the first", decoded.Turns)
	}

	// The second Turn's request carried the task input (which names the transcript as a
	// reference), the read result and the range it read.
	kinds := map[string]int{}
	var rangeItem map[string]any
	for _, item := range decoded.Turns[1].Manifest {
		kind, _ := item["kind"].(string)
		kinds[kind]++
		if kind == "MATERIAL_RANGE" {
			rangeItem = item
		}
	}
	want := map[string]int{"INSTRUCTIONS": 1, "TASK_INPUT": 1, "ARTIFACT": 1, "TOOL_RESULT": 1, "MATERIAL_RANGE": 1, "STATE": 1}
	for kind, n := range want {
		if kinds[kind] != n {
			t.Errorf("second Turn has %d %s items, want %d: %v", kinds[kind], kind, n, kinds)
		}
	}
	if rangeItem == nil {
		t.Fatalf("second Turn carries no MATERIAL_RANGE item: %v", decoded.Turns[1].Manifest)
	}
	lineSum := sha256.Sum256([]byte(transcriptSecondLine))
	wantItem := map[string]any{
		"kind": "MATERIAL_RANGE", "label": artifactID, "ref": artifactID, "version": materialSHA[:12],
		"sha256": hex.EncodeToString(lineSum[:]), "length": float64(len(transcriptSecondLine)), "included": true, "omittedReason": nil,
		"range": map[string]any{"startLine": float64(2), "lineCount": float64(1), "totalLines": float64(2)},
	}
	gotJSON, _ := json.Marshal(rangeItem)
	wantJSON, _ := json.Marshal(wantItem)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("MATERIAL_RANGE item = %s\nwant %s", gotJSON, wantJSON)
	}

	// The Manifest describes the read; the Trace carries neither the line the Agent read
	// nor the one it never read.
	for _, line := range []string{"Then it catches.", "ember glows"} {
		if strings.Contains(string(trace), line) {
			t.Errorf("the Agent Trace carries transcript text %q: %s", line, trace)
		}
	}
}
