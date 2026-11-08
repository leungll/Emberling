//go:build integration

// Agent Trace contract for the Registry-declared display of Tool calls: the top-level
// `media` list built from the media links a Tool declares, and the `shown` members a Tool
// declares for its run card. Both come from committed Tool results and arguments only, are
// bounded by the Trace field bound and never carry a URL with a credential. A Tool that
// declares nothing shows nothing of its arguments or result, so the Trace still carries no
// Tool input or result as a document.
package contract

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
)

// displayTraceToolName is a synchronous Tool registered only by these tests. Its result
// carries a clip reference, the URL that serves it and its media type, which its media
// link names, plus one declared and one undeclared scalar.
const displayTraceToolName = "test_media_render"

// displayTraceSignedURL and displayTraceInternal are values the Trace must never carry:
// a URL with a signature in its query, and an undeclared result member.
const (
	displayTraceSignedURL = "https://media.example/clips/signed.mp4?sig=SIGNATURE-NOT-SERVED"
	displayTraceInternal  = "internal-not-declared"
	displayTraceLongTail  = "LONG-URL-TAIL-NOT-SERVED"
)

// displayTraceExecutor answers by the `mode` argument so one Run can exercise several
// result shapes in order.
type displayTraceExecutor struct{}

func (displayTraceExecutor) Execute(_ context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	var args struct {
		Mode string `json:"mode"`
	}
	_ = json.Unmarshal(action.Arguments, &args)
	result := map[string]any{"mediaType": "video/mp4", "durationMs": 1200, "internal": displayTraceInternal}
	switch args.Mode {
	case "fail":
		return registry.ToolExecutionResult{}, errors.New("the renderer refused the clip")
	case "signed":
		result["clipRef"], result["clipUrl"] = "sha256:signed", displayTraceSignedURL
	case "no-ref":
		result["clipUrl"] = "https://media.example/clips/anonymous.mp4"
	case "long-url":
		result["clipRef"], result["clipUrl"] = "sha256:long", "https://media.example/clips/"+strings.Repeat("x", 80)+displayTraceLongTail+".mp4"
	default:
		result["clipRef"], result["clipUrl"] = "sha256:clip-1", "https://media.example/clips/clip-1.mp4"
	}
	output, err := json.Marshal(result)
	if err != nil {
		return registry.ToolExecutionResult{}, err
	}
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{Output: output}}, nil
}

func displayTraceToolRegistration() registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:        displayTraceToolName,
			Title:       "Render test clip",
			Description: "Render a clip for the display contract test",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"},"mode":{"type":"string"}},
				"required":["key"],"additionalProperties":false}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{
				"clipRef":{"type":"string"},"clipUrl":{"type":"string"},"mediaType":{"type":"string"},
				"durationMs":{"type":"integer"},"internal":{"type":"string"}},
				"required":["mediaType","durationMs"],"additionalProperties":false}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencySafe},
			ExecutionKind: domain.ToolExecutionSync,
			MediaLinks:    []domain.ToolMediaLink{{RefPointer: "/clipRef", URLPointer: "/clipUrl", MediaTypePointer: "/mediaType"}},
			Display: []domain.ToolDisplayField{
				{Source: domain.FactPointerArguments, Pointer: "/key", Title: "Key"},
				{Source: domain.FactPointerResult, Pointer: "/durationMs", Title: "Duration (ms)"},
			},
		},
		Executor: displayTraceExecutor{},
	}
}

// displayTraceResponse is the part of the Agent Trace these tests read.
type displayTraceResponse struct {
	Turns []struct {
		TurnNo       int `json:"turnNo"`
		ToolAttempts []struct {
			ID       string           `json:"id"`
			ToolName string           `json:"toolName"`
			Status   string           `json:"status"`
			Shown    []map[string]any `json:"shown"`
		} `json:"toolAttempts"`
	} `json:"turns"`
	Media struct {
		Items     []map[string]any `json:"items"`
		Truncated bool             `json:"truncated"`
	} `json:"media"`
}

// scriptDisplayTraceCalls makes the model call the test Tool once per mode, in order, then
// the lookup Tool, then FINAL.
func scriptDisplayTraceCalls(env *testEnv, modes ...string) {
	var calls atomic.Int32
	env.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		n := int(calls.Add(1))
		switch {
		case n <= len(modes):
			return &mockmodel.Scenario{
				Kind: mockmodel.ScenarioToolCall, ToolName: displayTraceToolName,
				ToolArguments: json.RawMessage(`{"key":"clip-key-` + modes[n-1] + `","mode":"` + modes[n-1] + `"}`),
			}
		case n == len(modes)+1:
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioToolCall, ToolName: lookup.ToolName, ToolArguments: json.RawMessage(`{"key":"undeclared-key"}`)}
		default:
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: "rendered"}
		}
	}
}

// runDisplayTrace saves an Agent Definition allowing the test Tool and lookup, drives one
// Run to its terminal status and returns the decoded Agent Trace with its raw body.
func runDisplayTrace(t *testing.T, env *testEnv, maxTurns int) (displayTraceResponse, string) {
	t.Helper()
	req := agentGraphRequest(baseAgentConfig(map[string]any{
		"allowedTools": []string{displayTraceToolName, lookup.ToolName}, "maxTurns": maxTurns,
	}))
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, body=%s", resp.StatusCode, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)
	runID := createAgentRun(t, env, workflowID, int(version), "render the clip")
	snapshot := env.waitForTerminal(t, runID, agentTraceWait)
	status, traceBody := getAgentTrace(t, env, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, traceBody)
	}
	return decodeBody[displayTraceResponse](t, traceBody), string(traceBody)
}

// TestAgentTraceDisplay_DeclaringTool_ListsMediaAndShownMembers drives a clean result, a
// result whose URL carries a signature and a result without a reference, then an undeclared
// lookup call. The media list holds the first two (the signed URL withheld, its reference
// kept), the declaring Tool's Attempts show exactly their declared members, and the
// undeclared Tool shows nothing of its arguments or result.
func TestAgentTraceDisplay_DeclaringTool_ListsMediaAndShownMembers(t *testing.T) {
	env := newTestEnvWithOptions(t, testEnvOptions{ExtraToolRegistrations: []registry.ToolRegistration{displayTraceToolRegistration()}})
	scriptDisplayTraceCalls(env, "ok", "signed", "no-ref")
	trace, raw := runDisplayTrace(t, env, 6)

	if len(trace.Turns) != 5 {
		t.Fatalf("turns = %d, want three test calls, one lookup call and FINAL: %s", len(trace.Turns), raw)
	}
	attemptID := func(turn int) string {
		if len(trace.Turns[turn].ToolAttempts) != 1 {
			t.Fatalf("turn %d has %d attempts, want one: %s", turn+1, len(trace.Turns[turn].ToolAttempts), raw)
		}
		return trace.Turns[turn].ToolAttempts[0].ID
	}

	if trace.Media.Truncated || len(trace.Media.Items) != 2 {
		t.Fatalf("media = %+v, want two items and no truncation", trace.Media)
	}
	wantItems := []map[string]any{
		{"ref": "sha256:clip-1", "url": "https://media.example/clips/clip-1.mp4", "mediaType": "video/mp4", "toolAttemptId": attemptID(0)},
		{"ref": "sha256:signed", "url": nil, "mediaType": "video/mp4", "toolAttemptId": attemptID(1), "withheld": "UNSAFE_URL"},
	}
	for i, want := range wantItems {
		if got := mustMarshal(t, trace.Media.Items[i]); string(got) != string(mustMarshal(t, want)) {
			t.Errorf("media.items[%d] = %s, want %s", i, got, mustMarshal(t, want))
		}
	}

	wantShown := func(key string, durationMs any) []map[string]any {
		return []map[string]any{
			{"title": "Key", "source": "ARGUMENTS", "pointer": "/key", "value": key},
			{"title": "Duration (ms)", "source": "RESULT", "pointer": "/durationMs", "value": durationMs},
		}
	}
	for turn, mode := range []string{"ok", "signed", "no-ref"} {
		attempt := trace.Turns[turn].ToolAttempts[0]
		if attempt.Status != "SUCCEEDED" {
			t.Errorf("turn %d attempt status = %s, want SUCCEEDED", turn+1, attempt.Status)
		}
		if got, want := mustMarshal(t, attempt.Shown), mustMarshal(t, wantShown("clip-key-"+mode, float64(1200))); string(got) != string(want) {
			t.Errorf("turn %d shown = %s, want %s", turn+1, got, want)
		}
	}
	lookupAttempt := trace.Turns[3].ToolAttempts[0]
	if lookupAttempt.ToolName != lookup.ToolName || lookupAttempt.Shown != nil {
		t.Errorf("lookup attempt = %+v, want no shown members for an undeclared Tool", lookupAttempt)
	}

	for _, forbidden := range []string{displayTraceSignedURL, "SIGNATURE-NOT-SERVED", displayTraceInternal,
		"undeclared-key", "record for undeclared-key", `"input"`, `"result"`, `"arguments"`, `"output"`} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("agent trace carries %q: %s", forbidden, raw)
		}
	}
}

// TestAgentTraceDisplay_FailedAttempt_NoMediaAndNoResultMembers proves a FAILED Attempt
// contributes no media item and shows no result member: only a SUCCEEDED Attempt holds a
// committed Tool result, so the argument member is all its card can show.
func TestAgentTraceDisplay_FailedAttempt_NoMediaAndNoResultMembers(t *testing.T) {
	env := newTestEnvWithOptions(t, testEnvOptions{ExtraToolRegistrations: []registry.ToolRegistration{displayTraceToolRegistration()}})
	scriptDisplayTraceCalls(env, "fail")
	trace, raw := runDisplayTrace(t, env, 4)

	if len(trace.Turns) == 0 || len(trace.Turns[0].ToolAttempts) != 1 {
		t.Fatalf("trace has no first Tool Attempt: %s", raw)
	}
	attempt := trace.Turns[0].ToolAttempts[0]
	if attempt.Status != "FAILED" {
		t.Fatalf("attempt status = %s, want FAILED: %s", attempt.Status, raw)
	}
	if len(trace.Media.Items) != 0 || trace.Media.Truncated {
		t.Errorf("media = %+v, want an empty list", trace.Media)
	}
	want := []map[string]any{{"title": "Key", "source": "ARGUMENTS", "pointer": "/key", "value": "clip-key-fail"}}
	if got := mustMarshal(t, attempt.Shown); string(got) != string(mustMarshal(t, want)) {
		t.Errorf("shown = %s, want only the argument member %s", got, mustMarshal(t, want))
	}
	if strings.Contains(raw, "Duration") {
		t.Errorf("a result member is shown for a FAILED attempt: %s", raw)
	}
}

// TestAgentTraceDisplay_OverBound_URLWithheldNotCut lowers the Trace field bound so the
// clip URL exceeds it: the media item keeps its reference and media type, its URL is null
// with TOO_LARGE, and no part of the URL is served.
func TestAgentTraceDisplay_OverBound_URLWithheldNotCut(t *testing.T) {
	env := newTestEnvWithOptions(t, testEnvOptions{
		ExtraToolRegistrations: []registry.ToolRegistration{displayTraceToolRegistration()},
		TraceMaxFieldBytes:     64,
	})
	scriptDisplayTraceCalls(env, "long-url")
	trace, raw := runDisplayTrace(t, env, 4)

	if len(trace.Media.Items) != 1 {
		t.Fatalf("media.items = %+v, want one", trace.Media.Items)
	}
	want := map[string]any{"ref": "sha256:long", "url": nil, "mediaType": "video/mp4", "toolAttemptId": trace.Turns[0].ToolAttempts[0].ID, "withheld": "TOO_LARGE"}
	if got := mustMarshal(t, trace.Media.Items[0]); string(got) != string(mustMarshal(t, want)) {
		t.Errorf("media.items[0] = %s, want %s", got, mustMarshal(t, want))
	}
	if strings.Contains(raw, displayTraceLongTail) || strings.Contains(raw, "media.example/clips/xxxx") {
		t.Errorf("the oversized URL is served, cut or whole: %s", raw)
	}
}

// TestAPI_ToolsCatalog_DisplayDeclarations_Serialised proves the catalogue serialises a
// Tool's media links and display fields in their DTO shape, and omits both keys for a Tool
// that declares neither.
func TestAPI_ToolsCatalog_DisplayDeclarations_Serialised(t *testing.T) {
	env := newTestEnvWithOptions(t, testEnvOptions{ExtraToolRegistrations: []registry.ToolRegistration{displayTraceToolRegistration()}})
	resp, body := env.doJSON(t, http.MethodGet, "/api/tools", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/tools status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	items := catalogItemsByKey(t, body, "name")

	item, ok := items[displayTraceToolName]
	if !ok {
		t.Fatalf("GET /api/tools does not list %s: %s", displayTraceToolName, body)
	}
	wantLinks := `[{"refPointer":"/clipRef","urlPointer":"/clipUrl","mediaTypePointer":"/mediaType"}]`
	if got := compactJSON(t, string(item["mediaLinks"])); got != wantLinks {
		t.Errorf("%s.mediaLinks = %s, want %s", displayTraceToolName, got, wantLinks)
	}
	wantDisplay := `[{"source":"ARGUMENTS","pointer":"/key","title":"Key"},{"source":"RESULT","pointer":"/durationMs","title":"Duration (ms)"}]`
	if got := compactJSON(t, string(item["display"])); got != wantDisplay {
		t.Errorf("%s.display = %s, want %s", displayTraceToolName, got, wantDisplay)
	}

	video, ok := items["generate_video"]
	if !ok {
		t.Fatalf("GET /api/tools does not list generate_video: %s", body)
	}
	wantVideoLinks := `[{"refPointer":"/videoUrl","urlPointer":"/videoUrl","mediaType":"video/mp4"}]`
	if got := compactJSON(t, string(video["mediaLinks"])); got != wantVideoLinks {
		t.Errorf("generate_video.mediaLinks = %s, want %s", got, wantVideoLinks)
	}
	image, ok := items["generate_image"]
	if !ok {
		t.Fatalf("GET /api/tools does not list generate_image: %s", body)
	}
	if !strings.Contains(string(image["mediaLinks"]), `"mediaTypePointer":"/artifact/mediaType"`) || len(image["display"]) == 0 {
		t.Errorf("generate_image declares mediaLinks=%s display=%s, want a nested media type pointer and display fields", image["mediaLinks"], image["display"])
	}

	undeclared, ok := items[lookup.ToolName]
	if !ok {
		t.Fatalf("GET /api/tools does not list %s: %s", lookup.ToolName, body)
	}
	for _, key := range []string{"mediaLinks", "display"} {
		if raw, present := undeclared[key]; present {
			t.Errorf("%s.%s = %s, want the key absent for an undeclared Tool", lookup.ToolName, key, raw)
		}
	}
}
