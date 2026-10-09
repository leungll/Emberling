//go:build integration

package contract

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const (
	pollableTestNodeType = "test_pollable_echo"
	pollableTestToolName = "test_pollable_lookup"
)

// pollableTestNodeExecutor declares a Poll implementation so the catalogue can expose a
// registered poll policy. It is never dispatched or polled.
type pollableTestNodeExecutor struct {
	testAsyncExecutor
}

func (pollableTestNodeExecutor) Poll(context.Context, registry.NodeAsyncState) (registry.PollResult, error) {
	return registry.PollResult{Status: registry.PollRunning}, nil
}

// pollableTestToolExecutor is the Tool equivalent of pollableTestNodeExecutor.
type pollableTestToolExecutor struct{}

func (pollableTestToolExecutor) Execute(context.Context, registry.ToolAction) (registry.ToolExecutionResult, error) {
	return registry.ToolExecutionResult{}, nil
}

func (pollableTestToolExecutor) OnCallback(context.Context, registry.ToolAsyncState, []byte) (registry.ToolResult, error) {
	return registry.ToolResult{}, nil
}

func (pollableTestToolExecutor) Poll(context.Context, registry.ToolAsyncState) (registry.PollResult, error) {
	return registry.PollResult{Status: registry.PollRunning}, nil
}

func pollableTestRegistrations() (registry.NodeRegistration, registry.ToolRegistration) {
	node := testAsyncNodeRegistration(nil)
	node.Metadata.Type = pollableTestNodeType
	node.Metadata.DisplayName = "Test Pollable Echo"
	node.Metadata.Poll = &domain.PollPolicy{IntervalMs: 1500, MaxPolls: 4}
	node.Binding = registry.ExecutorBinding{Executor: pollableTestNodeExecutor{}}

	tool := registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          pollableTestToolName,
			Description:   "Pollable lookup used only by catalogue contract tests",
			InputSchema:   json.RawMessage(`{"type":"object"}`),
			OutputSchema:  json.RawMessage(`{"type":"object"}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed},
			ExecutionKind: domain.ToolExecutionAsync,
			Poll:          &domain.PollPolicy{IntervalMs: 2500, MaxPolls: 6},
		},
		Executor: pollableTestToolExecutor{},
	}
	return node, tool
}

// catalogItemsByKey decodes a catalogue list envelope into raw items keyed by the given
// identity field, so the poll key can be checked for presence rather than only value.
func catalogItemsByKey(t *testing.T, body []byte, key string) map[string]map[string]json.RawMessage {
	t.Helper()
	envelope := decodeBody[map[string]json.RawMessage](t, body)
	items := decodeBody[[]map[string]json.RawMessage](t, envelope["items"])
	byKey := make(map[string]map[string]json.RawMessage, len(items))
	for _, item := range items {
		var id string
		if err := json.Unmarshal(item[key], &id); err != nil {
			t.Fatalf("catalogue item %s is not a string: %v, item=%v", key, err, item)
		}
		byKey[id] = item
	}
	return byKey
}

func TestAPI_NodeTypesCatalog_PollPolicyDeclared_ExposesPollOnlyForThatType(t *testing.T) {
	node, tool := pollableTestRegistrations()
	env := newTestEnvWithOptions(t, testEnvOptions{
		ExtraNodeRegistrations: []registry.NodeRegistration{node},
		ExtraToolRegistrations: []registry.ToolRegistration{tool},
	})

	resp, body := env.doJSON(t, http.MethodGet, "/api/node-types", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/node-types status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	items := catalogItemsByKey(t, body, "type")

	pollable, ok := items[pollableTestNodeType]
	if !ok {
		t.Fatalf("GET /api/node-types does not list %s: %s", pollableTestNodeType, body)
	}
	if got, want := string(pollable["poll"]), `{"intervalMs":1500,"maxPolls":4}`; got != want {
		t.Errorf("GET /api/node-types %s.poll = %s, want %s", pollableTestNodeType, got, want)
	}
	imageGeneration, ok := items["image_generation"]
	if !ok {
		t.Fatalf("GET /api/node-types does not list image_generation: %s", body)
	}
	if got, want := string(imageGeneration["poll"]), `{"intervalMs":2000,"maxPolls":30}`; got != want {
		t.Errorf("GET /api/node-types image_generation.poll = %s, want %s", got, want)
	}
	for _, typ := range []string{testAsyncNodeType, "text_input"} {
		item, ok := items[typ]
		if !ok {
			t.Fatalf("GET /api/node-types does not list %s: %s", typ, body)
		}
		if raw, present := item["poll"]; present {
			t.Errorf("GET /api/node-types %s.poll = %s, want the key absent for an undeclared policy", typ, raw)
		}
	}
}

func TestAPI_ToolsCatalog_PollPolicyDeclared_ExposesPollOnlyForThatTool(t *testing.T) {
	node, tool := pollableTestRegistrations()
	env := newTestEnvWithOptions(t, testEnvOptions{
		ExtraNodeRegistrations: []registry.NodeRegistration{node},
		ExtraToolRegistrations: []registry.ToolRegistration{tool},
	})

	resp, body := env.doJSON(t, http.MethodGet, "/api/tools", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/tools status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	items := catalogItemsByKey(t, body, "name")

	pollable, ok := items[pollableTestToolName]
	if !ok {
		t.Fatalf("GET /api/tools does not list %s: %s", pollableTestToolName, body)
	}
	if got, want := string(pollable["poll"]), `{"intervalMs":2500,"maxPolls":6}`; got != want {
		t.Errorf("GET /api/tools %s.poll = %s, want %s", pollableTestToolName, got, want)
	}
	for _, name := range []string{"lookup", "remote_lookup"} {
		item, ok := items[name]
		if !ok {
			t.Fatalf("GET /api/tools does not list %s: %s", name, body)
		}
		if raw, present := item["poll"]; present {
			t.Errorf("GET /api/tools %s.poll = %s, want the key absent for an undeclared policy", name, raw)
		}
	}
}
