//go:build integration

// Review model scenarios over the public API: review_asset reviews a generated image with
// a real vision model through the OpenAI-compatible Adapter, here pointed at a local
// Chat Completions stand-in. The stand-in sees the original photo and the candidate as
// inline image parts; a failed, slow or malformed answer ends the Agent as TOOL_ERROR
// without an asset_reviewed fact, an image over the inline bound is never sent, and no
// credential, image content or data URL reaches Trace, Events, the Snapshot or the logs.
package contract

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/adapters/openaimodel"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
	"github.com/leungll/Emberling/backend/internal/tools/reviewasset"
)

const (
	contractReviewModelID = "vision-review-1"
	contractReviewKey     = "sk-review-contract-secret"
	contractReviewStyle   = "hand-made clay stop-motion look"
	reviewModelPath       = "nodes[node_agent].config.reviewModelId"
)

// visionUpstream is a Chat Completions stand-in for the review model. answer decides
// each reply from the request body.
type visionUpstream struct {
	server   *httptest.Server
	requests atomic.Int64
	mu       sync.Mutex
	bodies   [][]byte
}

func newVisionUpstream(t *testing.T, answer func(w http.ResponseWriter, r *http.Request, body []byte)) *visionUpstream {
	t.Helper()
	u := &visionUpstream{}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.requests.Add(1)
		u.mu.Lock()
		u.bodies = append(u.bodies, body)
		u.mu.Unlock()
		answer(w, r, body)
	}))
	t.Cleanup(u.server.Close)
	return u
}

func (u *visionUpstream) lastBody() []byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.bodies) == 0 {
		return nil
	}
	return u.bodies[len(u.bodies)-1]
}

// answerVerdict replies with a FINAL whose output is verdict.
func answerVerdict(verdict string) func(http.ResponseWriter, *http.Request, []byte) {
	return func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		content, _ := json.Marshal(`{"kind":"FINAL","tool":null,"arguments":null,"output":` + verdict + `,"statePatch":null}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-review","choices":[{"message":{"content":`+string(content)+`},"finish_reason":"stop"}]}`)
	}
}

// answerEchoingFailure replies HTTP 500 with an error body that echoes the request,
// including its data URLs, the way a careless upstream might.
func answerEchoingFailure(w http.ResponseWriter, _ *http.Request, body []byte) {
	echo, _ := json.Marshal("upstream review failure detail: " + string(body))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = io.WriteString(w, `{"error":{"message":`+string(echo)+`,"type":"server_error"}}`)
}

// answerAfterCallerGivesUp holds the reply until the client has gone.
func answerAfterCallerGivesUp(_ http.ResponseWriter, r *http.Request, _ []byte) {
	<-r.Context().Done()
}

// withReviewModel registers the review model against upstream with the given per-call
// timeout.
func withReviewModel(t *testing.T, upstream *visionUpstream, timeout time.Duration) func(string, *testEnvOptions) {
	return func(_ string, opts *testEnvOptions) {
		model, err := openaimodel.New(openaimodel.Config{
			ModelID:        contractReviewModelID,
			DisplayName:    "Vision Review",
			BaseURL:        upstream.server.URL + "/v1",
			APIKey:         contractReviewKey,
			Model:          "vendor/vision-review",
			HTTPClient:     &http.Client{Timeout: timeout},
			DecisionSchema: registry.DecisionSchema(),
			VisionInput:    true,
		})
		if err != nil {
			t.Fatalf("openaimodel.New: %v", err)
		}
		opts.ExtraModelProviders = append(opts.ExtraModelProviders, model)
	}
}

// reviewRun is one single-photo Run whose scripted Agent generates one image, reviews it
// with styleRequirement and finishes with that image.
type reviewRun struct {
	runID    string
	snapshot map[string]any
	trace    photoSetTrace
	rawTrace []byte
	photo    []byte
}

func scriptGenerateReviewFinish(env *photoSetEnv, photoID *atomic.Value) {
	env.provider.Script = func(request registry.ModelRequest) *mockmodel.Scenario {
		id, _ := photoID.Load().(string)
		var results []map[string]any
		for _, message := range request.Messages {
			if message.Role != "tool" {
				continue
			}
			var result map[string]any
			_ = json.Unmarshal(message.Content, &result)
			results = append(results, result)
		}
		switch len(results) {
		case 0:
			arguments, _ := json.Marshal(map[string]any{"photoAssetId": id, "settings": map[string]any{"prompt": "clay look"}})
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioToolCall, ToolName: generateimage.ToolName, ToolArguments: arguments}
		case 1:
			arguments, _ := json.Marshal(map[string]any{
				"assetRef": results[0]["assetRef"], "photoAssetId": id, "styleRequirement": contractReviewStyle,
			})
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioToolCall, ToolName: reviewasset.ToolName, ToolArguments: arguments}
		default:
			final, _ := json.Marshal(map[string]any{
				"examples":       []any{map[string]any{"photoAssetId": id, "assetRef": results[0]["assetRef"]}},
				"settings":       map[string]any{"prompt": "clay look"},
				"settingsDigest": results[0]["settingsDigest"],
			})
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: string(final)}
		}
	}
}

func runReviewStory(t *testing.T, env *photoSetEnv) reviewRun {
	t.Helper()
	var photoID atomic.Value
	scriptGenerateReviewFinish(env, &photoID)
	workflowID, version := savePhotoSetDefinition(t, env.testEnv, "photo-set", 0, map[string]any{"reviewModelId": contractReviewModelID})
	photo := []byte("\x89PNG\r\n\x1a\nreview-photo-canary-bytes")
	ref := env.uploadPNG(t, photo)
	id, _ := ref["assetId"].(string)
	photoID.Store(id)
	resp, body := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version,
		"input": map[string]any{"brief": contractReviewStyle, "photos": []any{ref}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, body=%s", resp.StatusCode, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)
	snapshot := env.waitForTerminal(t, runID, photoSetWait)
	status, traceBody := getAgentTrace(t, env.testEnv, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, traceBody)
	}
	return reviewRun{runID: runID, snapshot: snapshot, trace: decodeBody[photoSetTrace](t, traceBody), rawTrace: traceBody, photo: photo}
}

func reviewFacts(trace photoSetTrace) []map[string]string {
	var out []map[string]string
	for _, fact := range trace.Facts.Items {
		if fact.FactType == reviewasset.FactType {
			out = append(out, fact.Bindings)
		}
	}
	return out
}

// TestE2E_ReviewModel_ReviewsPhotoAndCandidate_FactBindsModelPolicyAndStyle runs one
// generation and one review with a real review model. The model receives exactly one
// request carrying the original photo and the candidate as inline data URLs, unchanged,
// and the asset_reviewed fact binds the review model, the vision policy and the style
// digest; the delivery is accepted.
func TestE2E_ReviewModel_ReviewsPhotoAndCandidate_FactBindsModelPolicyAndStyle(t *testing.T) {
	upstream := newVisionUpstream(t, answerVerdict(`{"passed":true,"reasons":["Keeps the subject and reads as clay."]}`))
	env := newPhotoSetEnv(t, withReviewModel(t, upstream, e2eHTTPTimeout))
	run := runReviewStory(t, env)

	if got := runStatusOf(run.snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; trace=%s", got, run.rawTrace)
	}
	if got := upstream.requests.Load(); got != 1 {
		t.Fatalf("review model requests = %d, want 1", got)
	}
	var request struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(upstream.lastBody(), &request); err != nil || len(request.Messages) != 2 {
		t.Fatalf("review request = %s, want a system and one user message", upstream.lastBody())
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(request.Messages[1].Content, &parts); err != nil {
		t.Fatalf("user content is not a part array: %s", request.Messages[1].Content)
	}
	var images []string
	sawStyle := false
	for _, part := range parts {
		if part.ImageURL != nil {
			images = append(images, part.ImageURL.URL)
		}
		if strings.Contains(part.Text, contractReviewStyle) {
			sawStyle = true
		}
	}
	if len(images) != 2 || images[0] != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(run.photo) ||
		!strings.HasPrefix(images[1], "data:image/png;base64,") || !sawStyle {
		t.Errorf("review request parts = %d images (photo first: %t), style present %t; want the photo, the candidate and the style",
			len(images), len(images) > 0 && strings.HasSuffix(images[0], base64.StdEncoding.EncodeToString(run.photo)), sawStyle)
	}

	facts := reviewFacts(run.trace)
	if len(facts) != 1 {
		t.Fatalf("asset_reviewed facts = %v, want one", facts)
	}
	fact := facts[0]
	if fact["reviewModelId"] != contractReviewModelID || fact["policyVersion"] != reviewasset.VisionPolicyVersion ||
		fact["styleDigest"] != reviewasset.StyleDigest(contractReviewStyle) {
		t.Errorf("asset_reviewed bindings = %v, want the review model, %s and the style digest", fact, reviewasset.VisionPolicyVersion)
	}
	captionText, _ := nodeRunOutputOf(t, run.snapshot, "node_result")["caption"].(string)
	var caption struct {
		Accepted      bool   `json:"accepted"`
		PassedCount   int    `json:"passedCount"`
		PolicyVersion string `json:"policyVersion"`
	}
	if err := json.Unmarshal([]byte(captionText), &caption); err != nil {
		t.Fatalf("media_result caption %q is not JSON: %v", captionText, err)
	}
	if !caption.Accepted || caption.PassedCount != 1 || caption.PolicyVersion != reviewasset.VisionPolicyVersion {
		t.Errorf("media_result caption = %+v, want accepted with the one example passed under %s", caption, reviewasset.VisionPolicyVersion)
	}
}

// TestReviewAsset_ReviewModelFails_ToolErrorWithoutFact covers a review model that fails,
// times out or answers outside the review result Schema: the Agent ends TOOL_ERROR after
// exactly one review request and no asset_reviewed fact exists.
func TestReviewAsset_ReviewModelFails_ToolErrorWithoutFact(t *testing.T) {
	cases := map[string]struct {
		answer  func(http.ResponseWriter, *http.Request, []byte)
		timeout time.Duration
	}{
		"provider failure":        {answer: answerEchoingFailure, timeout: e2eHTTPTimeout},
		"provider timeout":        {answer: answerAfterCallerGivesUp, timeout: 300 * time.Millisecond},
		"output off schema":       {answer: answerVerdict(`{"passed":"yes","reasons":[]}`), timeout: e2eHTTPTimeout},
		"text instead of verdict": {answer: answerVerdict(`"looks fine"`), timeout: e2eHTTPTimeout},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			upstream := newVisionUpstream(t, tc.answer)
			env := newPhotoSetEnv(t, withReviewModel(t, upstream, tc.timeout))
			run := runReviewStory(t, env)
			assertReviewToolError(t, run)
			if got := upstream.requests.Load(); got != 1 {
				t.Errorf("review model requests = %d, want exactly 1: a failed review is never resent", got)
			}
		})
	}
}

func assertReviewToolError(t *testing.T, run reviewRun) {
	t.Helper()
	if got := runStatusOf(run.snapshot); got != "FAILED" {
		t.Fatalf("Run status = %q, want FAILED", got)
	}
	if run.trace.AgentRun.Termination == nil || *run.trace.AgentRun.Termination != "TOOL_ERROR" {
		t.Errorf("agentRun.termination = %v, want TOOL_ERROR", run.trace.AgentRun.Termination)
	}
	if facts := reviewFacts(run.trace); len(facts) != 0 {
		t.Errorf("asset_reviewed facts = %v, want none", facts)
	}
}

// TestReviewAsset_ImageOverInlineBound_NoModelCall covers images over the inline bound:
// the review fails as TOOL_ERROR without any request to the review model.
func TestReviewAsset_ImageOverInlineBound_NoModelCall(t *testing.T) {
	upstream := newVisionUpstream(t, answerVerdict(`{"passed":true,"reasons":[]}`))
	env := newPhotoSetEnv(t, withReviewModel(t, upstream, e2eHTTPTimeout), func(_ string, opts *testEnvOptions) {
		opts.ReviewMaxImageBytes = 16
	})
	run := runReviewStory(t, env)
	assertReviewToolError(t, run)
	if got := upstream.requests.Load(); got != 0 {
		t.Errorf("review model requests = %d, want 0", got)
	}
}

// TestTrace_ReviewModelFailure_NoSecretOrImageInTraceEventsLogs covers a review model that
// rejects the call with a body echoing the request. Neither the Agent Trace, the Node
// Detail, the Events, the Snapshot nor the Backend logs carry the API key, a data URL,
// either image's base64 content or the upstream body.
func TestTrace_ReviewModelFailure_NoSecretOrImageInTraceEventsLogs(t *testing.T) {
	logs := &syncBuffer{}
	upstream := newVisionUpstream(t, answerEchoingFailure)
	env := newPhotoSetEnv(t, withReviewModel(t, upstream, e2eHTTPTimeout), func(_ string, opts *testEnvOptions) {
		opts.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	run := runReviewStory(t, env)
	assertReviewToolError(t, run)

	agentNodeRunID := nodeRunIDOf(t, run.snapshot, "node_agent")
	detailResp, detailBody := env.doJSON(t, http.MethodGet, "/api/runs/"+run.runID+"/nodes/"+agentNodeRunID, nil)
	if detailResp.StatusCode != http.StatusOK {
		t.Fatalf("GET node detail status = %d, body=%s", detailResp.StatusCode, detailBody)
	}
	events, _ := json.Marshal(env.listEvents(t, run.runID))

	var sent struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(upstream.lastBody(), &sent)
	forbidden := []string{contractReviewKey, "Bearer", "data:image", "upstream review failure detail", base64.StdEncoding.EncodeToString(run.photo)}
	if len(sent.Messages) == 2 {
		var parts []struct {
			ImageURL *struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		_ = json.Unmarshal(sent.Messages[1].Content, &parts)
		for _, part := range parts {
			if part.ImageURL != nil {
				encoded := part.ImageURL.URL[strings.Index(part.ImageURL.URL, ",")+1:]
				forbidden = append(forbidden, encoded[:24])
			}
		}
	}
	if len(forbidden) != 7 {
		t.Fatalf("captured %d image encodings from the review request, want 2", len(forbidden)-5)
	}
	surfaces := map[string]string{
		"agent trace": string(run.rawTrace),
		"node detail": string(detailBody),
		"events":      string(events),
		"snapshot":    string(mustMarshal(t, run.snapshot)),
		"logs":        logs.String(),
	}
	for label, surface := range surfaces {
		for _, value := range forbidden {
			if strings.Contains(surface, value) {
				t.Errorf("%s carries %q", label, value)
			}
		}
	}
	if !strings.Contains(string(run.rawTrace), "TOOL_ERROR") {
		t.Errorf("agent trace does not record the TOOL_ERROR it must show: %s", run.rawTrace)
	}
}

// TestDefinitionSave_ReviewModelUnusable_RejectedWithPath covers a reviewModelId that is
// not registered or does not declare vision input: saving fails at the field instead of
// falling back to the mock review policy.
func TestDefinitionSave_ReviewModelUnusable_RejectedWithPath(t *testing.T) {
	env := newTestEnv(t)
	t.Run("unregistered", func(t *testing.T) {
		assertDefinitionRejected(t, env, agentGraphRequest(baseAgentConfig(map[string]any{"reviewModelId": "vision-missing"})),
			"MODEL_NOT_FOUND", reviewModelPath)
	})
	t.Run("lacks vision input", func(t *testing.T) {
		assertDefinitionRejected(t, env, agentGraphRequest(baseAgentConfig(map[string]any{"reviewModelId": mockmodel.ModelID})),
			"SEMANTIC_VALIDATION_FAILED", reviewModelPath)
	})
}

// TestCatalog_ReviewModel_ExposedInCatalog covers the catalogue side of the review model
// selection: review_asset declares usesReviewModel, a vision model lists vision_input,
// and the Agent node offers reviewModelId as a model selector filtered by it.
func TestCatalog_ReviewModel_ExposedInCatalog(t *testing.T) {
	upstream := newVisionUpstream(t, answerVerdict(`{"passed":true,"reasons":[]}`))
	opts := testEnvOptions{}
	withReviewModel(t, upstream, e2eHTTPTimeout)("", &opts)
	env := newTestEnvWithOptions(t, opts)

	resp, body := env.doJSON(t, http.MethodGet, "/api/tools", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/tools status = %d, body=%s", resp.StatusCode, body)
	}
	tools := catalogItemsByKey(t, body, "name")
	for name, want := range map[string]string{reviewasset.ToolName: "true", generateimage.ToolName: "false", "lookup": "false"} {
		if got := string(tools[name]["usesReviewModel"]); got != want {
			t.Errorf("%s.usesReviewModel = %q, want %s", name, got, want)
		}
	}

	resp, body = env.doJSON(t, http.MethodGet, "/api/models", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/models status = %d, body=%s", resp.StatusCode, body)
	}
	models := catalogItemsByKey(t, body, "id")
	if got := compactJSON(t, string(models[contractReviewModelID]["capabilities"])); got != `["text_generation","structured_decision","vision_input"]` {
		t.Errorf("%s.capabilities = %s, want vision_input declared", contractReviewModelID, got)
	}

	resp, body = env.doJSON(t, http.MethodGet, "/api/node-types", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/node-types status = %d, body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `{"path":"reviewModelId","order":17,"group":"MODEL","widget":"MODEL_SELECTOR","capability":"vision_input"}`) {
		t.Errorf("agent node uiSchema does not offer reviewModelId filtered by vision_input: %s", body)
	}
}
