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
	describedTestNodeType  = "test_described_echo"
	untitledTestToolName   = "test_untitled_lookup"
	titledTestToolName     = "test_titled_lookup"
	describedTestNodeText  = "Echoes its input for the display-text catalogue contract test."
	describedTestPortTitle = "Echo input"
	describedTestPortText  = "The text the echo repeats."
)

// displayTextTestToolExecutor lets the catalogue expose a Tool with and without a display
// title. It is never called.
type displayTextTestToolExecutor struct{}

func (displayTextTestToolExecutor) Execute(context.Context, registry.ToolAction) (registry.ToolExecutionResult, error) {
	return registry.ToolExecutionResult{}, nil
}

// displayTextTestRegistrations returns a Node whose metadata carries every display text
// field, a Tool with a display title and a Tool without one.
func displayTextTestRegistrations() (registry.NodeRegistration, []registry.ToolRegistration) {
	node := testAsyncNodeRegistration(nil)
	node.Metadata.Type = describedTestNodeType
	node.Metadata.DisplayName = "Test Described Echo"
	node.Metadata.Description = describedTestNodeText
	node.Metadata.Inputs = []domain.PortMetadata{{
		Name: "in", DataType: domain.PortTypeText, Required: true,
		Title: describedTestPortTitle, Description: describedTestPortText,
	}}
	node.Metadata.Outputs = []domain.PortMetadata{{
		Name: "out", DataType: domain.PortTypeText, Required: true,
		Description: "The echoed text.",
	}}

	tool := func(name, title string) registry.ToolRegistration {
		return registry.ToolRegistration{
			Metadata: domain.ToolMetadata{
				Name:          name,
				Title:         title,
				Description:   "Lookup used only by the display-text catalogue contract test",
				InputSchema:   json.RawMessage(`{"type":"object"}`),
				OutputSchema:  json.RawMessage(`{"type":"object"}`),
				SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
				ExecutionKind: domain.ToolExecutionSync,
			},
			Executor: displayTextTestToolExecutor{},
		}
	}
	return node, []registry.ToolRegistration{tool(titledTestToolName, "Titled lookup"), tool(untitledTestToolName, "")}
}

// stringMember decodes one string member of a catalogue item and fails when it is absent.
func stringMember(t *testing.T, item map[string]json.RawMessage, key string) string {
	t.Helper()
	raw, present := item[key]
	if !present {
		t.Fatalf("catalogue item has no %q member: %v", key, item)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("catalogue item %q is not a string: %v", key, err)
	}
	return value
}

// portsByName decodes a catalogue item's port list keyed by port name.
func portsByName(t *testing.T, item map[string]json.RawMessage, direction string) map[string]map[string]json.RawMessage {
	t.Helper()
	ports := decodeBody[[]map[string]json.RawMessage](t, item[direction])
	byName := make(map[string]map[string]json.RawMessage, len(ports))
	for _, port := range ports {
		byName[stringMember(t, port, "name")] = port
	}
	return byName
}

func TestAPI_NodeTypesCatalog_DisplayText_SerialisedAndOmittedWhenEmpty(t *testing.T) {
	node, tools := displayTextTestRegistrations()
	env := newTestEnvWithOptions(t, testEnvOptions{
		ExtraNodeRegistrations: []registry.NodeRegistration{node},
		ExtraToolRegistrations: tools,
	})

	resp, body := env.doJSON(t, http.MethodGet, "/api/node-types", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/node-types status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	items := catalogItemsByKey(t, body, "type")

	described, ok := items[describedTestNodeType]
	if !ok {
		t.Fatalf("GET /api/node-types does not list %s: %s", describedTestNodeType, body)
	}
	if got := stringMember(t, described, "description"); got != describedTestNodeText {
		t.Errorf("%s.description = %q, want %q", describedTestNodeType, got, describedTestNodeText)
	}
	in := portsByName(t, described, "inputs")["in"]
	if got := stringMember(t, in, "title"); got != describedTestPortTitle {
		t.Errorf("%s.inputs[in].title = %q, want %q", describedTestNodeType, got, describedTestPortTitle)
	}
	if got := stringMember(t, in, "description"); got != describedTestPortText {
		t.Errorf("%s.inputs[in].description = %q, want %q", describedTestNodeType, got, describedTestPortText)
	}
	out := portsByName(t, described, "outputs")["out"]
	if raw, present := out["title"]; present {
		t.Errorf("%s.outputs[out].title = %s, want the key absent for an undeclared title", describedTestNodeType, raw)
	}
	if got := stringMember(t, out, "description"); got != "The echoed text." {
		t.Errorf("%s.outputs[out].description = %q, want the declared text", describedTestNodeType, got)
	}

	// A built-in registration carries its own display text through the same wire fields.
	textInput, ok := items["text_input"]
	if !ok {
		t.Fatalf("GET /api/node-types does not list text_input: %s", body)
	}
	if got := stringMember(t, textInput, "description"); got == "" {
		t.Errorf("text_input.description is empty, want the registered sentence")
	}
	if got := stringMember(t, portsByName(t, textInput, "outputs")["text"], "description"); got == "" {
		t.Errorf("text_input.outputs[text].description is empty, want the registered sentence")
	}

	// A registration without display text omits the keys rather than sending empty strings.
	undescribed, ok := items[testAsyncNodeType]
	if !ok {
		t.Fatalf("GET /api/node-types does not list %s: %s", testAsyncNodeType, body)
	}
	if raw, present := undescribed["description"]; present {
		t.Errorf("%s.description = %s, want the key absent", testAsyncNodeType, raw)
	}
	for direction, name := range map[string]string{"inputs": "in", "outputs": "out"} {
		port := portsByName(t, undescribed, direction)[name]
		for _, key := range []string{"title", "description"} {
			if raw, present := port[key]; present {
				t.Errorf("%s.%s[%s].%s = %s, want the key absent", testAsyncNodeType, direction, name, key, raw)
			}
		}
	}
}

func TestAPI_ToolsCatalog_DisplayTitle_SerialisedAndOmittedWhenEmpty(t *testing.T) {
	node, tools := displayTextTestRegistrations()
	env := newTestEnvWithOptions(t, testEnvOptions{
		ExtraNodeRegistrations: []registry.NodeRegistration{node},
		ExtraToolRegistrations: tools,
	})

	resp, body := env.doJSON(t, http.MethodGet, "/api/tools", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/tools status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	items := catalogItemsByKey(t, body, "name")

	titled, ok := items[titledTestToolName]
	if !ok {
		t.Fatalf("GET /api/tools does not list %s: %s", titledTestToolName, body)
	}
	if got := stringMember(t, titled, "title"); got != "Titled lookup" {
		t.Errorf("%s.title = %q, want %q", titledTestToolName, got, "Titled lookup")
	}
	untitled, ok := items[untitledTestToolName]
	if !ok {
		t.Fatalf("GET /api/tools does not list %s: %s", untitledTestToolName, body)
	}
	if raw, present := untitled["title"]; present {
		t.Errorf("%s.title = %s, want the key absent for an undeclared title", untitledTestToolName, raw)
	}
	// The model-facing description is unchanged by the title and still serialised.
	if got := stringMember(t, untitled, "description"); got == "" {
		t.Errorf("%s.description is empty, want the registered description", untitledTestToolName)
	}

	// Every built-in Tool the harness registers carries a display title.
	for _, name := range []string{"lookup", "remote_lookup", "generate_image", "review_asset", "generate_video", "read_material"} {
		item, ok := items[name]
		if !ok {
			t.Fatalf("GET /api/tools does not list %s: %s", name, body)
		}
		if got := stringMember(t, item, "title"); got == "" {
			t.Errorf("%s.title is empty, want the registered display title", name)
		}
	}
}
