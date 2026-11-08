package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

// displayTestTool is a Tool no named view knows, so everything shown for it comes from the
// declaration alone.
const displayTestTool = "test_render"

func displayTestFields() []domain.ToolDisplayField {
	return []domain.ToolDisplayField{
		{Source: domain.FactPointerArguments, Pointer: "/settings/prompt", Title: "Prompt"},
		{Source: domain.FactPointerArguments, Pointer: "/count", Title: "Count"},
		{Source: domain.FactPointerResult, Pointer: "/passed", Title: "Passed"},
		{Source: domain.FactPointerResult, Pointer: "/summary/total", Title: "Tests run"},
		{Source: domain.FactPointerResult, Pointer: "/tags", Title: "Tags"},
	}
}

// projectShown renders one Tool Attempt with the given declarations through the whole Agent
// Trace response and returns the served `shown` member (nil when absent).
func projectShown(t *testing.T, attempt domain.ToolAttempt, fields []domain.ToolDisplayField, bound int) []map[string]any {
	t.Helper()
	trace := service.AgentTrace{
		Turns:       []service.AgentTraceTurn{{ToolAttempts: []domain.ToolAttempt{attempt}}},
		ToolDisplay: map[string][]domain.ToolDisplayField{attempt.ToolName: fields},
	}
	body, err := json.Marshal(toAgentTraceResponse(trace, bound))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Turns []struct {
			ToolAttempts []struct {
				Shown []map[string]any `json:"shown"`
			} `json:"toolAttempts"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return decoded.Turns[0].ToolAttempts[0].Shown
}

func shownField(title, source, pointer string, value any) map[string]any {
	return map[string]any{"title": title, "source": source, "pointer": pointer, "value": value}
}

func TestAgentTraceShown_SucceededAttempt_ShowsDeclaredMembersOnly(t *testing.T) {
	attempt := toolAttempt(displayTestTool, domain.ToolAttemptSucceeded,
		`{"settings":{"prompt":"warm light","seed":7},"count":2,"secretKey":"sk-not-shown"}`,
		`{"passed":true,"summary":{"total":3,"failed":0},"tags":["a","b"],"log":"the whole runner log"}`)
	got := projectShown(t, attempt, displayTestFields(), toolViewFieldBound)
	want := []map[string]any{
		shownField("Prompt", "ARGUMENTS", "/settings/prompt", "warm light"),
		shownField("Count", "ARGUMENTS", "/count", float64(2)),
		shownField("Passed", "RESULT", "/passed", true),
		shownField("Tests run", "RESULT", "/summary/total", float64(3)),
		shownField("Tags", "RESULT", "/tags", []any{"a", "b"}),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("shown (-want +got):\n%s", diff)
	}
	raw := mustJSON(t, got)
	for _, forbidden := range []string{"sk-not-shown", "the whole runner log", "seed", "failed"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("shown carries the undeclared member %q: %s", forbidden, raw)
		}
	}
}

// TestAgentTraceShown_NotSucceeded_ResultMembersAbsent proves a result member is read only
// from a SUCCEEDED Attempt: a STARTED or FAILED one shows its argument members alone, even
// when a result document is present on the row.
func TestAgentTraceShown_NotSucceeded_ResultMembersAbsent(t *testing.T) {
	for _, status := range []domain.ToolAttemptStatus{domain.ToolAttemptStarted, domain.ToolAttemptDispatched, domain.ToolAttemptFailed} {
		attempt := toolAttempt(displayTestTool, status, `{"settings":{"prompt":"warm light"},"count":2}`, `{"passed":true,"summary":{"total":3}}`)
		got := projectShown(t, attempt, displayTestFields(), toolViewFieldBound)
		want := []map[string]any{
			shownField("Prompt", "ARGUMENTS", "/settings/prompt", "warm light"),
			shownField("Count", "ARGUMENTS", "/count", float64(2)),
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s: shown (-want +got):\n%s", status, diff)
		}
	}
}

func TestAgentTraceShown_UndeclaredTool_KeyAbsent(t *testing.T) {
	attempt := toolAttempt("lookup", domain.ToolAttemptSucceeded, `{"key":"k"}`, `{"key":"k","record":"record for k"}`)
	got := projectAttempt(t, `{}`, attempt, toolViewFieldBound)
	assertKey(t, got, "shown", "")
	if raw := mustJSON(t, got); strings.Contains(raw, "record for k") {
		t.Errorf("an undeclared Tool's result reached the projection: %s", raw)
	}
}

// TestAgentTraceShown_MissingOrNullMember_NotShown proves a pointer that selects nothing,
// or null, yields no field: absence is never rendered as a value.
func TestAgentTraceShown_MissingOrNullMember_NotShown(t *testing.T) {
	attempt := toolAttempt(displayTestTool, domain.ToolAttemptSucceeded, `{"settings":{},"count":null}`, `{"summary":{}}`)
	if got := projectShown(t, attempt, displayTestFields(), toolViewFieldBound); got != nil {
		t.Errorf("shown = %v, want the key absent", got)
	}
}

func TestAgentTraceShown_OverBound_WithheldNeverCut(t *testing.T) {
	const bound = 32
	long := strings.Repeat("p", bound)
	attempt := toolAttempt(displayTestTool, domain.ToolAttemptSucceeded, `{"settings":{"prompt":"`+long+`"},"count":2}`, `{"passed":true}`)
	got := projectShown(t, attempt, displayTestFields(), bound)
	want := []map[string]any{
		{"title": "Prompt", "source": "ARGUMENTS", "pointer": "/settings/prompt", "value": nil, "withheld": "TOO_LARGE"},
		shownField("Count", "ARGUMENTS", "/count", float64(2)),
		shownField("Passed", "RESULT", "/passed", true),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("shown (-want +got):\n%s", diff)
	}
	if strings.Contains(mustJSON(t, got), long[:bound/2]) {
		t.Errorf("the oversized prompt still appears, cut or whole: %s", mustJSON(t, got))
	}
}

// TestAgentTraceShown_URLWithCredentials_Withheld proves a URL-shaped string, alone or
// inside a list, is withheld when it carries userinfo, a query or a fragment, while a
// digest or path that is not a URL is shown as it is.
func TestAgentTraceShown_URLWithCredentials_Withheld(t *testing.T) {
	attempt := toolAttempt(displayTestTool, domain.ToolAttemptSucceeded,
		`{"settings":{"prompt":"https://img.example/ref.png?sig=SECRET"},"count":2}`,
		`{"passed":true,"tags":["sha256:abc","HTTPS://media.example/v.mp4#SECRET"]}`)
	got := projectShown(t, attempt, displayTestFields(), toolViewFieldBound)
	want := []map[string]any{
		{"title": "Prompt", "source": "ARGUMENTS", "pointer": "/settings/prompt", "value": nil, "withheld": "UNSAFE_URL"},
		shownField("Count", "ARGUMENTS", "/count", float64(2)),
		shownField("Passed", "RESULT", "/passed", true),
		{"title": "Tags", "source": "RESULT", "pointer": "/tags", "value": nil, "withheld": "UNSAFE_URL"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("shown (-want +got):\n%s", diff)
	}
	if strings.Contains(mustJSON(t, got), "SECRET") {
		t.Errorf("a withheld URL still appears: %s", mustJSON(t, got))
	}
}

func TestAgentTraceShown_CleanURL_Shown(t *testing.T) {
	fields := []domain.ToolDisplayField{{Source: domain.FactPointerResult, Pointer: "/videoUrl", Title: "Video"}}
	attempt := toolAttempt(displayTestTool, domain.ToolAttemptSucceeded, `{}`, `{"videoUrl":"https://media.example/v.mp4"}`)
	got := projectShown(t, attempt, fields, toolViewFieldBound)
	want := []map[string]any{shownField("Video", "RESULT", "/videoUrl", "https://media.example/v.mp4")}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("shown (-want +got):\n%s", diff)
	}
}
