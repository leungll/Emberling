//go:build integration

// Photo effect-template scenarios, end to end over the public API: an Agent driven by the
// scripted mock model applies one effect template across three uploaded photos against a
// real Mock Provider whose dispatch record is the external evidence. The main story ends
// with one reviewed example per photo; the gate story shows a video request for an
// unreviewed asset rejected before any dispatch; the limit story shows a generation past
// the frozen limit rejected before any dispatch.
package contract

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/mockprovider"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
	"github.com/leungll/Emberling/backend/internal/tools/reviewasset"
)

// photoSetWait bounds one photo-set Run: up to fourteen Turns plus one callback.
const photoSetWait = 60 * time.Second

type photoSetEnv struct {
	*testEnv
	providerURL string
}

// newPhotoSetEnv starts a Mock Provider with test controls and a dispatch record, wires a
// Backend to it, and routes video callbacks back to that Backend without holding them.
func newPhotoSetEnv(t *testing.T) *photoSetEnv {
	t.Helper()
	record, err := mockprovider.OpenRecord(filepath.Join(t.TempDir(), "dispatch.jsonl"))
	if err != nil {
		t.Fatalf("open mock provider record: %v", err)
	}
	callbacks := newCallbackTransport()
	callbacks.release()
	dispatcher := mockprovider.NewDispatcher(&http.Client{Transport: callbacks, Timeout: e2eHTTPTimeout})
	provider := httptest.NewServer(mockprovider.NewServer(dispatcher, mockprovider.WithTestControls(record)))
	t.Cleanup(func() {
		provider.Close()
		close(callbacks.done)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := dispatcher.Shutdown(ctx); err != nil {
			t.Logf("mock provider dispatcher shutdown: %v", err)
		}
		_ = record.Close()
	})
	env := newTestEnvWithOptions(t, testEnvOptions{MockTaskBaseURL: provider.URL})
	callbacks.setTarget(env.server.URL)
	return &photoSetEnv{testEnv: env, providerURL: provider.URL}
}

// photoSetRecordLine is the part of one Mock Provider dispatch record line these stories read.
type photoSetRecordLine struct {
	Event          string `json:"event"`
	Kind           string `json:"kind"`
	AssetID        string `json:"assetId"`
	ExternalTaskID string `json:"externalTaskId"`
}

// providerRecord reads the whole dispatch record of this test's Mock Provider.
func (e *photoSetEnv) providerRecord(t *testing.T) (generated []string, videoTasks []string) {
	t.Helper()
	resp, err := (&http.Client{Timeout: e2eHTTPTimeout}).Get(e.providerURL + "/control/record?after=0")
	if err != nil {
		t.Fatalf("GET /control/record: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /control/record status = %d, body=%s", resp.StatusCode, body)
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		var line photoSetRecordLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("decode record line %q: %v", scanner.Text(), err)
		}
		switch line.Event {
		case "generated":
			generated = append(generated, line.AssetID)
		case "video_dispatched":
			videoTasks = append(videoTasks, line.ExternalTaskID)
		}
	}
	return generated, videoTasks
}

// photoSetTrace is the part of the Agent Trace these stories assert on.
type photoSetTrace struct {
	AgentRun struct {
		Termination *string `json:"termination"`
	} `json:"agentRun"`
	Turns []struct {
		TurnNo int `json:"turnNo"`
		Action *struct {
			Status string `json:"status"`
			Error  *struct {
				Code    string          `json:"code"`
				Details json.RawMessage `json:"details"`
			} `json:"error"`
		} `json:"action"`
		ToolAttempts []struct {
			ToolName        string `json:"toolName"`
			CallbackBinding *struct {
				ExternalTaskID string `json:"externalTaskId"`
			} `json:"callbackBinding"`
		} `json:"toolAttempts"`
	} `json:"turns"`
	Facts struct {
		Items []struct {
			ID          string            `json:"id"`
			FactType    string            `json:"factType"`
			Subject     string            `json:"subject"`
			Bindings    map[string]string `json:"bindings"`
			Verdict     *bool             `json:"verdict"`
			BasisFactID *string           `json:"basisFactId"`
		} `json:"items"`
	} `json:"facts"`
	GenerationBudget struct {
		MaxGenerationCalls  *int `json:"maxGenerationCalls"`
		GenerationCallsUsed int  `json:"generationCallsUsed"`
	} `json:"generationBudget"`
}

// savePhotoSetDefinition saves the photo effect-template fixture with the Agent bound to
// script, overriding its generation limit when limit is positive.
func savePhotoSetDefinition(t *testing.T, env *testEnv, script string, limit int) (string, int) {
	t.Helper()
	raw, err := os.ReadFile("../fixtures/definitions/photo_effect_template.json")
	if err != nil {
		t.Fatalf("read photo_effect_template.json: %v", err)
	}
	var fx documentProcessingFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("unmarshal photo_effect_template.json: %v", err)
	}
	for i, node := range fx.Nodes {
		if node.ID != "node_agent" {
			continue
		}
		var config map[string]any
		if err := json.Unmarshal(node.Config, &config); err != nil {
			t.Fatalf("decode node_agent config: %v", err)
		}
		config["modelConfig"] = map[string]any{"script": script}
		if limit > 0 {
			config["maxGenerationCalls"] = limit
		}
		if fx.Nodes[i].Config, err = json.Marshal(config); err != nil {
			t.Fatalf("encode node_agent config: %v", err)
		}
	}
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions (photo effect template) status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)
	return workflowID, int(version)
}

// runPhotoSet uploads three distinct photos, runs the saved Definition over them and
// returns the terminal Snapshot, the Agent Trace and the uploaded photo asset ids.
func runPhotoSet(t *testing.T, env *photoSetEnv, script string, limit int) (map[string]any, photoSetTrace, []string) {
	t.Helper()
	workflowID, version := savePhotoSetDefinition(t, env.testEnv, script, limit)
	photos := make([]any, 0, 3)
	photoIDs := make([]string, 0, 3)
	for _, content := range []string{"photo-one", "photo-two", "photo-three"} {
		ref := env.uploadPNG(t, []byte("\x89PNG\r\n\x1a\n"+content))
		photos = append(photos, ref)
		id, _ := ref["assetId"].(string)
		photoIDs = append(photoIDs, id)
	}
	resp, body := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version,
		"input": map[string]any{"brief": "one warm film look for the whole set", "photos": photos},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs (photo set) status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)
	snapshot := env.waitForTerminal(t, runID, photoSetWait)
	status, traceBody := getAgentTrace(t, env.testEnv, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, traceBody)
	}
	return snapshot, decodeBody[photoSetTrace](t, traceBody), photoIDs
}

func runStatusOf(snapshot map[string]any) string {
	run, _ := snapshot["run"].(map[string]any)
	status, _ := run["status"].(string)
	return status
}

func nodeRunOutputOf(t *testing.T, snapshot map[string]any, nodeID string) map[string]any {
	t.Helper()
	nodeRuns, _ := snapshot["nodeRuns"].([]any)
	for _, nr := range nodeRuns {
		m, _ := nr.(map[string]any)
		if id, _ := m["nodeId"].(string); id == nodeID {
			output, _ := m["output"].(map[string]any)
			return output
		}
	}
	t.Fatalf("snapshot has no nodeRun for node %q", nodeID)
	return nil
}

// TestE2E_PhotoSet_MainStory_DeliversOneReviewedExamplePerPhotoMatchingTheProviderRecord
// proves the two-round story: six generations each followed by a review, one video
// completed by callback, and a delivery the media result accepts. The fact ledger and the
// Mock Provider record agree on every generated asset and the one video task.
func TestE2E_PhotoSet_MainStory_DeliversOneReviewedExamplePerPhotoMatchingTheProviderRecord(t *testing.T) {
	env := newPhotoSetEnv(t)

	snapshot, trace, photoIDs := runPhotoSet(t, env, "photo-set", 0)

	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", got, snapshot)
	}
	captionText, _ := nodeRunOutputOf(t, snapshot, "node_result")["caption"].(string)
	var caption struct {
		Accepted       bool   `json:"accepted"`
		PassedCount    int    `json:"passedCount"`
		PhotoCount     int    `json:"photoCount"`
		PolicyVersion  string `json:"policyVersion"`
		SettingsDigest string `json:"settingsDigest"`
	}
	if err := json.Unmarshal([]byte(captionText), &caption); err != nil {
		t.Fatalf("media_result caption %q is not JSON: %v", captionText, err)
	}
	if !caption.Accepted || caption.PassedCount != 3 || caption.PhotoCount != 3 ||
		caption.PolicyVersion == "" || caption.SettingsDigest == "" {
		t.Errorf("media_result caption = %+v, want accepted with 3 of 3 passed under one policy and one settings digest", caption)
	}

	generationFacts := map[string]string{}
	generationsPerPhoto := map[string]int{}
	var generatedSubjects []string
	reviews := 0
	for _, fact := range trace.Facts.Items {
		switch fact.FactType {
		case generateimage.FactType:
			generationFacts[fact.ID] = fact.Subject
			generationsPerPhoto[fact.Bindings["photoAssetId"]]++
			generatedSubjects = append(generatedSubjects, fact.Subject)
		case reviewasset.FactType:
			reviews++
			if fact.BasisFactID == nil || generationFacts[*fact.BasisFactID] != fact.Subject {
				t.Errorf("review fact %s basisFactId = %v, want the generation fact of %s", fact.ID, fact.BasisFactID, fact.Subject)
			}
		}
	}
	if len(generatedSubjects) != 6 || reviews != 6 {
		t.Errorf("ledger has %d generation and %d review facts, want 6 and 6", len(generatedSubjects), reviews)
	}
	for _, photoID := range photoIDs {
		if generationsPerPhoto[photoID] != 2 {
			t.Errorf("photo %s has %d generations, want 2 (one per round)", photoID, generationsPerPhoto[photoID])
		}
	}
	if trace.GenerationBudget.GenerationCallsUsed != 6 {
		t.Errorf("generationBudget.generationCallsUsed = %d, want 6", trace.GenerationBudget.GenerationCallsUsed)
	}

	var boundTasks []string
	for _, turn := range trace.Turns {
		for _, attempt := range turn.ToolAttempts {
			if attempt.CallbackBinding != nil && attempt.CallbackBinding.ExternalTaskID != "" {
				boundTasks = append(boundTasks, attempt.CallbackBinding.ExternalTaskID)
			}
		}
	}
	generated, videoTasks := env.providerRecord(t)
	slices.Sort(generated)
	slices.Sort(generatedSubjects)
	if !slices.Equal(generated, generatedSubjects) {
		t.Errorf("provider generated %v, ledger generated %v, want the same six assets", generated, generatedSubjects)
	}
	if len(videoTasks) != 1 || !slices.Equal(videoTasks, boundTasks) {
		t.Errorf("provider video tasks %v, Trace callback bindings %v, want the same single task", videoTasks, boundTasks)
	}
}

// TestE2E_PhotoSet_GateStory_VideoForUnreviewedAssetRejectedBeforeDispatch proves that a
// video request for an asset with no passing review fails the Action at claim with
// PRECONDITION_UNMET, ends the Agent as INVALID_ACTION, fails the Run, and never reaches
// the Mock Provider.
func TestE2E_PhotoSet_GateStory_VideoForUnreviewedAssetRejectedBeforeDispatch(t *testing.T) {
	env := newPhotoSetEnv(t)

	snapshot, trace, _ := runPhotoSet(t, env, "photo-set-skip-review", 0)

	if got := runStatusOf(snapshot); got != "FAILED" {
		t.Fatalf("Run status = %q, want FAILED; snapshot=%v", got, snapshot)
	}
	if trace.AgentRun.Termination == nil || *trace.AgentRun.Termination != "INVALID_ACTION" {
		t.Errorf("agentRun.termination = %v, want INVALID_ACTION", trace.AgentRun.Termination)
	}
	if code := rejectedActionCode(trace); code != "PRECONDITION_UNMET" {
		t.Errorf("rejected Action error code = %q, want PRECONDITION_UNMET", code)
	}
	generated, videoTasks := env.providerRecord(t)
	if len(generated) != 1 || len(videoTasks) != 0 {
		t.Errorf("provider record has %d generations and %d video tasks, want 1 and 0", len(generated), len(videoTasks))
	}
}

// TestE2E_PhotoSet_LimitStory_ThirdGenerationRejectedAtTheFrozenLimit proves that with a
// generation limit of two, the third generation fails at claim with
// GENERATION_LIMIT_REACHED and the Mock Provider records exactly two generations.
func TestE2E_PhotoSet_LimitStory_ThirdGenerationRejectedAtTheFrozenLimit(t *testing.T) {
	env := newPhotoSetEnv(t)

	snapshot, trace, _ := runPhotoSet(t, env, "photo-set-limit", 2)

	if got := runStatusOf(snapshot); got != "FAILED" {
		t.Fatalf("Run status = %q, want FAILED; snapshot=%v", got, snapshot)
	}
	if code := rejectedActionCode(trace); code != "GENERATION_LIMIT_REACHED" {
		t.Errorf("rejected Action error code = %q, want GENERATION_LIMIT_REACHED", code)
	}
	if trace.GenerationBudget.MaxGenerationCalls == nil || *trace.GenerationBudget.MaxGenerationCalls != 2 ||
		trace.GenerationBudget.GenerationCallsUsed != 2 {
		t.Errorf("generationBudget = %+v, want limit 2 with 2 used", trace.GenerationBudget)
	}
	generated, videoTasks := env.providerRecord(t)
	if len(generated) != 2 || len(videoTasks) != 0 {
		t.Errorf("provider record has %d generations and %d video tasks, want 2 and 0", len(generated), len(videoTasks))
	}
}

// rejectedActionCode returns the error code of the first FAILED Action in the Trace.
func rejectedActionCode(trace photoSetTrace) string {
	for _, turn := range trace.Turns {
		if turn.Action != nil && turn.Action.Status == "FAILED" && turn.Action.Error != nil {
			return turn.Action.Error.Code
		}
	}
	return ""
}
