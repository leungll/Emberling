//go:build integration

// Agent Trace fact ledger and generation budget contract:
// GET /runs/{runId}/nodes/{nodeRunId}/agent carries, next to the Turns, every execution
// fact the Agent Run recorded (oldest first, bounded, with a truncation flag) and the
// generation budget: the frozen limit, null when the Agent has none, and the generation
// calls already made, counted from persisted Tool Attempts of Tools the Registry marks as
// counting toward the limit.
package contract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/mockprovider"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
	"github.com/leungll/Emberling/backend/internal/tools/reviewasset"
)

const (
	traceFactsPhotoA = "photo_trace_a"
	traceFactsPhotoB = "photo_trace_b"
	traceFactsFinal  = "two images generated, the first one reviewed"
)

// traceFactsAssetRef finds the generated asset reference in a Tool result message.
var traceFactsAssetRef = regexp.MustCompile(`"assetRef"\s*:\s*"(img_[0-9a-f]+)"`)

type traceFactsResponse struct {
	Turns []struct {
		TurnNo       int `json:"turnNo"`
		ToolAttempts []struct {
			ID       string `json:"id"`
			ToolName string `json:"toolName"`
		} `json:"toolAttempts"`
	} `json:"turns"`
	Facts struct {
		Items []struct {
			ID            string            `json:"id"`
			FactType      string            `json:"factType"`
			Subject       string            `json:"subject"`
			Bindings      map[string]string `json:"bindings"`
			Verdict       *bool             `json:"verdict"`
			BasisFactID   *string           `json:"basisFactId"`
			ToolAttemptID string            `json:"toolAttemptId"`
			CreatedAt     time.Time         `json:"createdAt"`
		} `json:"items"`
		Truncated *bool `json:"truncated"`
	} `json:"facts"`
	GenerationBudget struct {
		MaxGenerationCalls  *int `json:"maxGenerationCalls"`
		GenerationCallsUsed *int `json:"generationCallsUsed"`
	} `json:"generationBudget"`
}

// newTraceFactsEnv starts a Backend whose generate_image calls reach a real Mock Provider.
func newTraceFactsEnv(t *testing.T) *testEnv {
	t.Helper()
	dispatcher := mockprovider.NewDispatcher(nil)
	provider := httptest.NewServer(mockprovider.NewServer(dispatcher))
	t.Cleanup(func() {
		provider.Close()
		_ = dispatcher.Shutdown(context.Background())
	})
	return newTestEnvWithOptions(t, testEnvOptions{MockTaskBaseURL: provider.URL})
}

// scriptGenerateReviewGenerate makes the model generate an image from photo A, review the
// asset that generation returned, generate an image from photo B, then answer FINAL.
func scriptGenerateReviewGenerate(env *testEnv) {
	var calls atomic.Int32
	env.provider.Script = func(request registry.ModelRequest) *mockmodel.Scenario {
		switch calls.Add(1) {
		case 1:
			return traceFactsGeneration(traceFactsPhotoA)
		case 2:
			assetRef := ""
			for i := len(request.Messages) - 1; i >= 0; i-- {
				if request.Messages[i].Role == "tool" {
					if m := traceFactsAssetRef.FindStringSubmatch(string(request.Messages[i].Content)); m != nil {
						assetRef = m[1]
					}
					break
				}
			}
			return &mockmodel.Scenario{
				Kind:     mockmodel.ScenarioToolCall,
				ToolName: reviewasset.ToolName,
				ToolArguments: json.RawMessage(`{"assetRef":"` + assetRef + `","photoAssetId":"` +
					traceFactsPhotoA + `","mock":"pass"}`),
			}
		case 3:
			return traceFactsGeneration(traceFactsPhotoB)
		default:
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: traceFactsFinal}
		}
	}
}

func traceFactsGeneration(photoID string) *mockmodel.Scenario {
	return &mockmodel.Scenario{
		Kind:          mockmodel.ScenarioToolCall,
		ToolName:      generateimage.ToolName,
		ToolArguments: json.RawMessage(`{"photoAssetId":"` + photoID + `","settings":{"style":"natural"}}`),
	}
}

// saveTraceFactsDefinition saves the agent fixture allowing generate_image and
// review_asset, with maxGenerationCalls set to limit, or left out when limit is nil.
func saveTraceFactsDefinition(t *testing.T, env *testEnv, limit *int) (string, int) {
	t.Helper()
	fx := loadAgentLookupFixture(t)
	for i, node := range fx.Nodes {
		if node.ID != "node_agent" {
			continue
		}
		var config map[string]any
		if err := json.Unmarshal(node.Config, &config); err != nil {
			t.Fatalf("decode node_agent config: %v", err)
		}
		config["allowedTools"] = []string{generateimage.ToolName, reviewasset.ToolName}
		config["maxTurns"] = 6
		if limit != nil {
			config["maxGenerationCalls"] = *limit
		}
		encoded, err := json.Marshal(config)
		if err != nil {
			t.Fatalf("encode node_agent config: %v", err)
		}
		fx.Nodes[i].Config = encoded
	}
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)
	return workflowID, int(version)
}

// runTraceFactsAgent drives one completed generate, review, generate Run and returns its
// Agent Trace, decoded and raw.
func runTraceFactsAgent(t *testing.T, limit *int) (traceFactsResponse, string) {
	t.Helper()
	env := newTraceFactsEnv(t)
	scriptGenerateReviewGenerate(env)
	workflowID, version := saveTraceFactsDefinition(t, env, limit)
	runID := createAgentRun(t, env, workflowID, version, "make two images")
	snapshot := env.waitForTerminal(t, runID, agentTraceWait)
	run, _ := snapshot["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", status, snapshot)
	}
	status, body := getAgentTrace(t, env, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, want %d, body=%s", status, http.StatusOK, body)
	}
	return decodeBody[traceFactsResponse](t, body), string(body)
}

// TestAgentTraceFacts_GenerateReviewGenerate_ProjectsLedgerInOrder proves the ledger
// carries each recorded fact oldest first, tied to the Tool Attempt that produced it, with
// the review's verdict and its basis link to the generation it judged.
func TestAgentTraceFacts_GenerateReviewGenerate_ProjectsLedgerInOrder(t *testing.T) {
	limit := 5
	trace, _ := runTraceFactsAgent(t, &limit)

	attemptTool := map[string]string{}
	for _, turn := range trace.Turns {
		for _, attempt := range turn.ToolAttempts {
			attemptTool[attempt.ID] = attempt.ToolName
		}
	}
	facts := trace.Facts.Items
	if trace.Facts.Truncated == nil || *trace.Facts.Truncated {
		t.Errorf("facts.truncated = %v, want false", trace.Facts.Truncated)
	}
	if len(facts) != 3 {
		t.Fatalf("ledger has %d facts, want 3: %+v", len(facts), facts)
	}
	wantTypes := []string{generateimage.FactType, reviewasset.FactType, generateimage.FactType}
	wantTools := []string{generateimage.ToolName, reviewasset.ToolName, generateimage.ToolName}
	for i, fact := range facts {
		if fact.FactType != wantTypes[i] {
			t.Errorf("facts[%d].factType = %q, want %q", i, fact.FactType, wantTypes[i])
		}
		if got := attemptTool[fact.ToolAttemptID]; got != wantTools[i] {
			t.Errorf("facts[%d].toolAttemptId %q belongs to %q, want a %s Attempt of this Trace", i, fact.ToolAttemptID, got, wantTools[i])
		}
		if i > 0 && fact.CreatedAt.Before(facts[i-1].CreatedAt) {
			t.Errorf("facts[%d] created %v before facts[%d] at %v, want oldest first", i, fact.CreatedAt, i-1, facts[i-1].CreatedAt)
		}
	}

	generatedA, reviewed, generatedB := facts[0], facts[1], facts[2]
	if !strings.HasPrefix(generatedA.Subject, "img_") || generatedA.Bindings["photoAssetId"] != traceFactsPhotoA ||
		generatedA.Bindings["settingsDigest"] == "" {
		t.Errorf("first generation fact = %+v, want an asset of %s with its settings digest", generatedA, traceFactsPhotoA)
	}
	if generatedA.Verdict != nil || generatedA.BasisFactID != nil {
		t.Errorf("generation fact verdict/basis = %v/%v, want null/null", generatedA.Verdict, generatedA.BasisFactID)
	}
	if reviewed.Subject != generatedA.Subject || reviewed.Bindings["photoAssetId"] != traceFactsPhotoA {
		t.Errorf("review fact = %+v, want it about %s from %s", reviewed, generatedA.Subject, traceFactsPhotoA)
	}
	if reviewed.Verdict == nil || !*reviewed.Verdict {
		t.Errorf("review fact verdict = %v, want true", reviewed.Verdict)
	}
	if reviewed.BasisFactID == nil || *reviewed.BasisFactID != generatedA.ID {
		t.Errorf("review fact basisFactId = %v, want the first generation fact %s", reviewed.BasisFactID, generatedA.ID)
	}
	if generatedB.Bindings["photoAssetId"] != traceFactsPhotoB || generatedB.Subject == generatedA.Subject {
		t.Errorf("second generation fact = %+v, want a distinct asset of %s", generatedB, traceFactsPhotoB)
	}

	budget := trace.GenerationBudget
	if budget.MaxGenerationCalls == nil || *budget.MaxGenerationCalls != limit {
		t.Errorf("generationBudget.maxGenerationCalls = %v, want %d", budget.MaxGenerationCalls, limit)
	}
	if budget.GenerationCallsUsed == nil || *budget.GenerationCallsUsed != 2 {
		t.Errorf("generationBudget.generationCallsUsed = %v, want 2 (review_asset does not count)", budget.GenerationCallsUsed)
	}
}

// TestAgentTraceFacts_NoGenerationLimit_LimitNullCallsStillCounted proves an Agent without
// maxGenerationCalls reports a null limit while its generation calls are still counted.
func TestAgentTraceFacts_NoGenerationLimit_LimitNullCallsStillCounted(t *testing.T) {
	trace, raw := runTraceFactsAgent(t, nil)

	if !strings.Contains(raw, `"maxGenerationCalls":null`) {
		t.Errorf("trace does not render the absent limit as null: %s", raw)
	}
	if used := trace.GenerationBudget.GenerationCallsUsed; used == nil || *used != 2 {
		t.Errorf("generationBudget.generationCallsUsed = %v, want 2", used)
	}
	if len(trace.Facts.Items) != 3 {
		t.Errorf("ledger has %d facts, want 3", len(trace.Facts.Items))
	}
}

// TestAgentTraceFacts_Ledger_CarriesNoSecretsOrPayloads proves the ledger adds no Tool
// result, credential, callback token or storage key to the Trace response.
func TestAgentTraceFacts_Ledger_CarriesNoSecretsOrPayloads(t *testing.T) {
	_, raw := runTraceFactsAgent(t, nil)

	for _, forbidden := range []string{traceFactsFinal, `"result"`, `"arguments"`, `"settings"`, `"passed"`} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("agent trace response contains %q: %s", forbidden, raw)
		}
	}
	for _, forbidden := range []string{"storage_key", "storagekey", "token", "authorization", "secret", "credential", "api_key", "apikey"} {
		if strings.Contains(strings.ToLower(withoutTokenUsageFieldNames(raw)), forbidden) {
			t.Errorf("agent trace response contains %q: %s", forbidden, raw)
		}
	}
}
