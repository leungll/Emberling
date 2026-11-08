//go:build integration

// Run Snapshot contract for the media a Run's steps produced: the top-level `media` list
// built from the media links a Node Type declares. Items come from committed SUCCEEDED
// NodeRun outputs only, pass the Trace field bound and never carry a URL with a
// credential; a Node Type that declares nothing contributes nothing.
package contract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const (
	transcriptSourceURL        = "https://media.example/v1/sources/" + transcriptSourceID + "/content"
	transcriptSourceSignedURL  = "https://user:SECRET-NOT-SERVED@media.example/v1/sources/" + transcriptSourceID + "/content"
	transcriptSourceMediaType  = "video/webm"
	runMediaCapTestNodeType    = "test_media_fanout"
	runMediaCapTestPort        = "media"
	runMediaCapTestItemsBeyond = 257
)

// runTranscriptToCompletion creates a Run of the transcript Definition, completes the
// transcript node with a result that names the served source by sourceURL, and returns
// the terminal Snapshot and the transcript NodeRun's id.
func runTranscriptToCompletion(t *testing.T, env *testEnv, media *contractMediaService, workflowID string, version int, sourceURL string) (map[string]any, string) {
	t.Helper()
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
	sum := sha256.Sum256([]byte(transcriptMaterial))
	callbackResp, callbackBody := env.doCallback(t, token, taskID, map[string]any{
		"status": "SUCCEEDED",
		"result": map[string]any{
			"sourceId": transcriptSourceID, "sourceSha256": transcriptSourceSHA, "transcriptSha256": hex.EncodeToString(sum[:]),
			"language": "en", "sentenceCount": 2, "lineCount": 2, "durationMs": transcriptDuration, "model": "small.en",
			"material": transcriptMaterial, "url": sourceURL, "mediaType": transcriptSourceMediaType,
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
	return snapshot, nodeRunIDOf(t, snapshot, "node_transcript")
}

// TestRunSnapshot_Media_ListsTheTranscriptSourceByItsDeclaredLink: the transcript node's
// SUCCEEDED output names the source video, so the Snapshot lists it once by the node's
// registered link; the task_input, agent and text_output steps of the same Run declare no
// links and contribute nothing.
func TestRunSnapshot_Media_ListsTheTranscriptSourceByItsDeclaredLink(t *testing.T) {
	env, media := newTranscriptEnv(t)
	workflowID, version := saveTranscriptDefinition(t, env)
	snapshot, nodeRunID := runTranscriptToCompletion(t, env, media, workflowID, version, transcriptSourceURL)

	want := map[string]any{
		"items": []any{map[string]any{
			"ref": transcriptSourceID, "url": transcriptSourceURL, "mediaType": transcriptSourceMediaType, "nodeRunId": nodeRunID,
		}},
		"truncated": false,
	}
	if diff := cmp.Diff(want, snapshot["media"]); diff != "" {
		t.Fatalf("snapshot media (-want +got):\n%s", diff)
	}
}

// TestRunSnapshot_Media_URLWithCredential_WithheldKeepsTheReference: a source URL that
// carries userinfo is never served; the item keeps the reference and names the reason.
func TestRunSnapshot_Media_URLWithCredential_WithheldKeepsTheReference(t *testing.T) {
	env, media := newTranscriptEnv(t)
	workflowID, version := saveTranscriptDefinition(t, env)
	snapshot, nodeRunID := runTranscriptToCompletion(t, env, media, workflowID, version, transcriptSourceSignedURL)

	want := map[string]any{
		"items": []any{map[string]any{
			"ref": transcriptSourceID, "url": nil, "mediaType": transcriptSourceMediaType, "nodeRunId": nodeRunID, "withheld": "UNSAFE_URL",
		}},
		"truncated": false,
	}
	if diff := cmp.Diff(want, snapshot["media"]); diff != "" {
		t.Fatalf("snapshot media (-want +got):\n%s", diff)
	}
}

// runMediaCapExecutor publishes one json port carrying more media items than the Snapshot
// list shows, and the text its output port needs.
type runMediaCapExecutor struct{}

func (runMediaCapExecutor) ValidateSemantics(context.Context, map[string]any) error { return nil }

func (runMediaCapExecutor) Execute(_ context.Context, input registry.NodeInput, _ map[string]any) (registry.NodeResult, error) {
	items := make([]map[string]string, runMediaCapTestItemsBeyond)
	for i := range items {
		items[i] = map[string]string{"ref": fmt.Sprintf("clip-%03d", i), "url": fmt.Sprintf("https://media.example/clips/%03d.mp4", i)}
	}
	raw, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		return registry.NodeResult{}, err
	}
	return registry.CompletedResult(map[string]json.RawMessage{runMediaCapTestPort: raw, "text": input.Ports["in"]}), nil
}

// runMediaCapRegistration declares one media link per item the executor publishes.
func runMediaCapRegistration() registry.NodeRegistration {
	links := make([]domain.MediaLink, runMediaCapTestItemsBeyond)
	for i := range links {
		links[i] = domain.MediaLink{
			Port: runMediaCapTestPort, RefPointer: fmt.Sprintf("/items/%d/ref", i), URLPointer: fmt.Sprintf("/items/%d/url", i), MediaType: "video/mp4",
		}
	}
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type: runMediaCapTestNodeType, DisplayName: "Test Media Fan-out", Category: domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs:        []domain.PortMetadata{{Name: "in", DataType: domain.PortTypeText, Required: true}},
			Outputs: []domain.PortMetadata{
				{Name: runMediaCapTestPort, DataType: domain.PortTypeJSON, Required: true},
				{Name: "text", DataType: domain.PortTypeText, Required: true},
			},
			ConfigSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			SideEffect:   domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			MediaLinks:   links,
		},
		Binding: registry.ExecutorBinding{Executor: runMediaCapExecutor{}},
	}
}

// TestRunSnapshot_Media_OverCap_TruncatedInDeclaredOrder: a step whose output carries more
// media than the list shows yields the first items in declared order and reports the cut;
// the stored output is not touched.
func TestRunSnapshot_Media_OverCap_TruncatedInDeclaredOrder(t *testing.T) {
	env := newTestEnvWithOptions(t, testEnvOptions{ExtraNodeRegistrations: []registry.NodeRegistration{runMediaCapRegistration()}})
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": "Media fan-out", "description": "",
		"nodes": []map[string]any{
			{"id": "in", "type": "text_input", "config": map[string]any{"inputKey": "prompt", "required": true}},
			{"id": "fanout", "type": runMediaCapTestNodeType, "config": map[string]any{}},
			{"id": "out", "type": "text_output", "config": map[string]any{}},
		},
		"edges": []map[string]any{
			{"id": "e1", "source": "in", "sourceHandle": "text", "target": "fanout", "targetHandle": "in"},
			{"id": "e2", "source": "fanout", "sourceHandle": "text", "target": "out", "targetHandle": "text"},
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, body=%s", resp.StatusCode, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)

	resp, body = env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": int(version), "input": map[string]any{"prompt": "cut"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, body=%s", resp.StatusCode, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)
	snapshot := env.waitForTerminal(t, runID, transcriptWait)
	run, _ := snapshot["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", status, snapshot)
	}

	media, _ := snapshot["media"].(map[string]any)
	items, _ := media["items"].([]any)
	if truncated, _ := media["truncated"].(bool); !truncated || len(items) != runMediaCapTestItemsBeyond-1 {
		t.Fatalf("media items = %d, truncated = %v; want %d items and the cut reported", len(items), media["truncated"], runMediaCapTestItemsBeyond-1)
	}
	first, _ := items[0].(map[string]any)
	last, _ := items[len(items)-1].(map[string]any)
	if first["ref"] != "clip-000" || last["ref"] != fmt.Sprintf("clip-%03d", runMediaCapTestItemsBeyond-2) || first["nodeRunId"] != nodeRunIDOf(t, snapshot, "fanout") {
		t.Fatalf("items are not the first %d in declared order: first=%v last=%v", runMediaCapTestItemsBeyond-1, first, last)
	}
	// The committed output still carries every item; only the projection is cut.
	nodeRuns, _ := snapshot["nodeRuns"].([]any)
	for _, nr := range nodeRuns {
		m, _ := nr.(map[string]any)
		if m["nodeId"] != "fanout" {
			continue
		}
		output, _ := m["output"].(map[string]any)
		port, _ := output[runMediaCapTestPort].(map[string]any)
		if stored, _ := port["items"].([]any); len(stored) != runMediaCapTestItemsBeyond {
			t.Fatalf("stored output carries %d items, want %d untouched", len(stored), runMediaCapTestItemsBeyond)
		}
	}
}
