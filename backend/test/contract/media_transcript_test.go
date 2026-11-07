//go:build integration

package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/externaltask"
	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/nodes/mediatranscript"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// Media Transcript contract: a transcript the node stores is served through the Run's
// artifact content route and appears in the Run's projections only as a reference.

const (
	transcriptSourceID  = "src_contract"
	transcriptSourceSHA = "1a2b3c4d5e6f70819a2b3c4d5e6f70819a2b3c4d5e6f70819a2b3c4d5e6f7081"
	transcriptDuration  = 90000
	transcriptMaterial  = "S1\t0.000\t3.000\tThe ember glows quietly.\nS2\t3.000\t6.000\tThen it catches.\n"
	transcriptWait      = 10 * time.Second
)

// contractMediaService records submitted TRANSCRIBE tasks and reports each on a channel;
// it never answers a status query with a finished task, so only the callback the test
// delivers completes the node.
type contractMediaService struct {
	mu       sync.Mutex
	submits  []map[string]any
	received chan map[string]any
}

func (s *contractMediaService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/tasks":
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.submits = append(s.submits, body)
		s.mu.Unlock()
		// The acceptance names the task it queued, as the task protocol requires.
		taskID, _ := body["taskId"].(string)
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"taskId":"`+taskID+`","status":"QUEUED"}`)
		s.received <- body
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/tasks/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/tasks/")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"taskId":"`+id+`","status":"RUNNING"}`)
	default:
		http.NotFound(w, r)
	}
}

// newTranscriptEnv wires a Backend whose media_transcript node dispatches to the fake
// media service and writes transcripts into the Backend's own artifact subtree.
func newTranscriptEnv(t *testing.T) (*testEnv, *contractMediaService) {
	t.Helper()
	media := &contractMediaService{received: make(chan map[string]any, 4)}
	server := httptest.NewServer(media)
	t.Cleanup(server.Close)
	root := t.TempDir()
	reg, err := mediatranscript.Registration(mediatranscript.Ports{
		Tasks:     externaltask.New(server.URL, server.Client()),
		Artifacts: asset.NewArtifactStore(root, defaultTestArtifactMaxBytes),
	})
	if err != nil {
		t.Fatalf("media transcript registration: %v", err)
	}
	env := newTestEnvWithOptions(t, testEnvOptions{AssetStorageRoot: root, ExtraNodeRegistrations: []registry.NodeRegistration{reg}})
	env.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: "cut"}
	}
	return env, media
}

// saveTranscriptDefinition saves task_input -> media_transcript -> agent -> text_output
// through the API, starting from the shared agent fixture for the Agent node.
func saveTranscriptDefinition(t *testing.T, env *testEnv) (string, int) {
	t.Helper()
	fx := loadAgentLookupFixture(t)
	var agentNode, outputNode json.RawMessage
	for _, node := range fx.Nodes {
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
		"name": "Transcript then agent", "description": "task_input -> media_transcript -> agent -> text_output",
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

func transcriptRunInput() map[string]any {
	return map[string]any{"task": map[string]any{
		"sourceId": transcriptSourceID, "sourceSha256": transcriptSourceSHA, "durationMs": transcriptDuration,
		"requirement": "a short clip", "goal": "DELIVER_CLIP",
	}}
}

// TestMediaTranscript_NodeArtifact_ServedThroughRunContentRouteAndProjectedAsReference:
// the callback completes the node; the Run Snapshot and the NodeRun detail project the
// transcript reference and never the text; the artifact content route serves the text
// for this Run and answers 404 for another Run.
func TestMediaTranscript_NodeArtifact_ServedThroughRunContentRouteAndProjectedAsReference(t *testing.T) {
	env, media := newTranscriptEnv(t)
	workflowID, version := saveTranscriptDefinition(t, env)

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
	if submit["type"] != mediatranscript.TaskType || taskID == "" || token == "" {
		t.Fatalf("submitted task = %v, want a TRANSCRIBE task with id and callback token", submit)
	}

	sum := sha256.Sum256([]byte(transcriptMaterial))
	materialSHA := hex.EncodeToString(sum[:])
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
	run, _ := snapshot["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", status, snapshot)
	}
	nodeRunID := nodeRunIDOf(t, snapshot, "node_transcript")
	detailResp, detailBody := env.doJSON(t, http.MethodGet, "/api/runs/"+runID+"/nodes/"+nodeRunID, nil)
	if detailResp.StatusCode != http.StatusOK {
		t.Fatalf("GET node run detail status = %d, body=%s", detailResp.StatusCode, detailBody)
	}
	detail := decodeBody[struct {
		NodeRun struct {
			Output map[string]json.RawMessage `json:"output"`
		} `json:"nodeRun"`
		Attempts []struct {
			Status string                     `json:"status"`
			Result map[string]json.RawMessage `json:"result"`
		} `json:"attempts"`
	}](t, detailBody)
	var transcript mediatranscript.Transcript
	if err := json.Unmarshal(detail.NodeRun.Output["transcript"], &transcript); err != nil {
		t.Fatalf("decode transcript port of the NodeRun detail: %v; body=%s", err, detailBody)
	}
	if transcript.ArtifactID == "" || transcript.SHA256 != materialSHA || transcript.LineCount != 2 || transcript.SourceSHA256 != transcriptSourceSHA {
		t.Fatalf("transcript projection = %+v, want the reference of the stored material", transcript)
	}
	if len(detail.Attempts) != 1 || detail.Attempts[0].Status != "SUCCEEDED" || string(detail.Attempts[0].Result["transcript"]) != string(detail.NodeRun.Output["transcript"]) {
		t.Fatalf("attempts = %+v, want one SUCCEEDED Attempt whose result carries the same transcript reference", detail.Attempts)
	}
	snapshotRaw, _ := json.Marshal(snapshot)
	for name, raw := range map[string][]byte{"snapshot": snapshotRaw, "node run detail": detailBody} {
		if strings.Contains(string(raw), "ember glows") {
			t.Fatalf("the %s carries the transcript text", name)
		}
	}

	contentResp, content := getArtifactContent(t, env, runID, transcript.ArtifactID)
	if contentResp.StatusCode != http.StatusOK {
		t.Fatalf("GET artifact content status = %d, body=%s", contentResp.StatusCode, content)
	}
	if string(content) != transcriptMaterial || !strings.HasPrefix(contentResp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("artifact content = %q (%s), want the transcript as text/plain", content, contentResp.Header.Get("Content-Type"))
	}

	otherResp, otherBody := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version, "input": transcriptRunInput(),
	})
	if otherResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs (other) status = %d, body=%s", otherResp.StatusCode, otherBody)
	}
	otherRunID, _ := decodeBody[map[string]any](t, otherBody)["id"].(string)
	assertArtifactNotFound(t, env, otherRunID, transcript.ArtifactID)
}
