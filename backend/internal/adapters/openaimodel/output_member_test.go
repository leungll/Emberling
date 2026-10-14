package openaimodel_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/adapters/openaimodel"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
)

// photoOutputSchema is an object-form outputSchema inside the strict output subset.
const photoOutputSchema = `{
  "type": "object",
  "properties": {
    "examples": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "photoAssetId": {"type": "string"},
          "assetRef": {"type": "string"}
        },
        "required": ["photoAssetId", "assetRef"],
        "additionalProperties": false
      }
    },
    "settingsDigest": {"type": "string", "description": "One line."}
  },
  "required": ["examples", "settingsDigest"],
  "additionalProperties": false
}`

// plainStringMember is the output member of every text-form request: the answer text
// itself, with no hint that it is JSON-encoded.
const plainStringMember = `{"type": ["string", "null"]}`

// sentRequest is the part of the request body these tests read.
type sentRequest struct {
	ResponseFormat struct {
		JSONSchema struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"json_schema"`
	} `json:"response_format"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

// sendWithOutputSchema sends one request whose FinalOutputSchema is schema and returns
// what the fake Provider received.
func sendWithOutputSchema(t *testing.T, schema json.RawMessage) sentRequest {
	t.Helper()
	content := `{"kind":"FINAL","tool":null,"arguments":null,"output":null,"statePatch":null}`
	server, seen := fakeProvider(t, http.StatusOK, completion(t, content, ""))
	provider := newProvider(t, server.URL, server.Client())
	request := agentRequest()
	request.FinalOutputSchema = schema

	if _, err := provider.Generate(context.Background(), request); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	var body sentRequest
	if err := json.Unmarshal(seen.body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	return body
}

// schemaProperties returns the strict Decision schema's properties and required list.
func schemaProperties(t *testing.T, schema json.RawMessage) (map[string]json.RawMessage, []string) {
	t.Helper()
	var decoded struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(schema, &decoded); err != nil {
		t.Fatalf("strict Decision schema is not an object: %v", err)
	}
	return decoded.Properties, decoded.Required
}

// envelopeWithoutOutput is the strict Decision schema every request carries apart from
// its output member.
const envelopeWithoutOutput = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "kind": {"type": "string", "enum": ["TOOL_CALL", "FINAL"]},
    "tool": {"type": ["string", "null"]},
    "arguments": {"type": ["string", "null"], "description": "JSON-encoded value, or null when it does not apply"},
    "statePatch": {"type": ["string", "null"], "description": "JSON-encoded value, or null when it does not apply"}
  },
  "required": ["kind", "arguments", "output", "statePatch", "tool"]
}`

// assertEnvelopeUnchanged checks every member except output against the fixed envelope.
func assertEnvelopeUnchanged(t *testing.T, schema json.RawMessage) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(schema, &got); err != nil {
		t.Fatal(err)
	}
	properties, _ := got["properties"].(map[string]any)
	delete(properties, "output")
	if diff := cmp.Diff(decode(t, []byte(envelopeWithoutOutput)), any(got)); diff != "" {
		t.Errorf("strict Decision envelope changed (-want +got):\n%s", diff)
	}
}

func systemContent(t *testing.T, body sentRequest) string {
	t.Helper()
	if len(body.Messages) == 0 || body.Messages[0].Role != "system" {
		t.Fatalf("request has no leading system message")
	}
	return body.Messages[0].Content
}

func TestProvider_Generate_TextFormRequest_OutputMemberIsPlainString(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema json.RawMessage
	}{
		{"no outputSchema", nil},
		{"null outputSchema", raw(`null`)},
		{"empty Schema", raw(`{}`)},
		{"enum accepting the empty string", raw(`{"type":"string","enum":["","approved"]}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := sendWithOutputSchema(t, tc.schema)
			schema := body.ResponseFormat.JSONSchema.Schema
			properties, _ := schemaProperties(t, schema)
			if diff := cmp.Diff(decode(t, []byte(plainStringMember)), decode(t, properties["output"])); diff != "" {
				t.Errorf("output member mismatch (-want +got):\n%s", diff)
			}
			assertEnvelopeUnchanged(t, schema)

			system := systemContent(t, body)
			for _, stale := range []string{`\"done\"`, `"output" to a JSON-encoded`} {
				if strings.Contains(system, stale) {
					t.Errorf("system prompt still asks for a JSON-encoded output (%q):\n%s", stale, system)
				}
			}
			if !strings.Contains(system, "plain text") {
				t.Errorf("system prompt does not say a text answer is written as plain text:\n%s", system)
			}
		})
	}
}

func TestProvider_Generate_ObjectFormRequest_OutputMemberCarriesOutputSchema(t *testing.T) {
	body := sendWithOutputSchema(t, raw(photoOutputSchema))
	schema := body.ResponseFormat.JSONSchema.Schema

	properties, required := schemaProperties(t, schema)
	want := decode(t, []byte(photoOutputSchema)).(map[string]any)
	want["type"] = []any{"object", "null"}
	if diff := cmp.Diff(any(want), decode(t, properties["output"])); diff != "" {
		t.Errorf("output member mismatch (-want +got):\n%s", diff)
	}
	if !slices.Contains(required, "output") {
		t.Errorf("required = %v, want output listed", required)
	}
	assertEnvelopeUnchanged(t, schema)
}

func TestProvider_Generate_ObjectFormRequest_LeavesNextTextFormRequestPlain(t *testing.T) {
	// The output member is derived per request: an object-form request must not leak
	// into the strict form of a later text-form request served by the same Provider.
	content := `{"kind":"FINAL","tool":null,"arguments":null,"output":null,"statePatch":null}`
	server, seen := fakeProvider(t, http.StatusOK, completion(t, content, ""))
	provider := newProvider(t, server.URL, server.Client())

	objectRequest := agentRequest()
	objectRequest.FinalOutputSchema = raw(photoOutputSchema)
	if _, err := provider.Generate(context.Background(), objectRequest); err != nil {
		t.Fatalf("Generate(object form) error = %v", err)
	}
	if _, err := provider.Generate(context.Background(), agentRequest()); err != nil {
		t.Fatalf("Generate(text form) error = %v", err)
	}
	var body sentRequest
	if err := json.Unmarshal(seen.body, &body); err != nil {
		t.Fatal(err)
	}
	properties, _ := schemaProperties(t, body.ResponseFormat.JSONSchema.Schema)
	if diff := cmp.Diff(decode(t, []byte(plainStringMember)), decode(t, properties["output"])); diff != "" {
		t.Errorf("second request's output member mismatch (-want +got):\n%s", diff)
	}
}

func TestProvider_Generate_TextGenerationRequest_OutputMemberIsPlainString(t *testing.T) {
	body := sendWithOutputSchema(t, raw(`{"type":"string"}`))
	properties, _ := schemaProperties(t, body.ResponseFormat.JSONSchema.Schema)
	if diff := cmp.Diff(decode(t, []byte(plainStringMember)), decode(t, properties["output"])); diff != "" {
		t.Errorf("output member mismatch (-want +got):\n%s", diff)
	}
}

// finalAnswer normalises one FINAL answer whose output member is output, for a request
// with the given FinalOutputSchema.
func finalAnswer(t *testing.T, schema json.RawMessage, output string) registry.ModelDecision {
	t.Helper()
	content := `{"kind":"FINAL","tool":null,"arguments":null,"output":` + output + `,"statePatch":null}`
	server, _ := fakeProvider(t, http.StatusOK, completion(t, content, ""))
	provider := newProvider(t, server.URL, server.Client())
	request := agentRequest()
	request.FinalOutputSchema = schema

	response, err := provider.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	return response.Decision
}

func TestProvider_Generate_TextFormAnswer_StringBecomesJSONString(t *testing.T) {
	decision := finalAnswer(t, nil, `"done"`)
	want := registry.ModelDecision{Kind: registry.DecisionFinal, Output: raw(`"done"`)}
	if diff := cmp.Diff(want, decision); diff != "" {
		t.Errorf("Decision mismatch (-want +got):\n%s", diff)
	}
	if err := runtime.ValidateFinalOutput(decision.Output, nil); err != nil {
		t.Errorf("ValidateFinalOutput() error = %v, want the plain answer accepted as text", err)
	}
}

func TestProvider_Generate_TextFormAnswer_NeverUnwrapsEncodedText(t *testing.T) {
	decision := finalAnswer(t, nil, `"\"done\""`)
	text, err := runtime.FinalOutputText(decision.Output)
	if err != nil {
		t.Fatalf("FinalOutputText() error = %v", err)
	}
	if text != `"done"` {
		t.Errorf("text = %q, want the quoted text kept exactly as the model wrote it", text)
	}
}

func TestProvider_Generate_ObjectFormAnswer_ObjectBecomesOutput(t *testing.T) {
	object := `{"examples":[{"photoAssetId":"ast_1","assetRef":"art_9"}],"settingsDigest":"warm, 35mm"}`
	decision := finalAnswer(t, raw(photoOutputSchema), object)
	if diff := cmp.Diff(decode(t, []byte(object)), decode(t, decision.Output)); diff != "" {
		t.Errorf("Output mismatch (-want +got):\n%s", diff)
	}
	if err := runtime.ValidateFinalOutput(decision.Output, raw(photoOutputSchema)); err != nil {
		t.Errorf("ValidateFinalOutput() error = %v, want the object accepted", err)
	}
}

func TestProvider_Generate_ObjectFormAnswerWithStringOutput_PassesToRuntime(t *testing.T) {
	// A Provider that did not enforce the strict form answers with the object encoded as
	// a string. The Adapter neither rejects nor unwraps it; the Runtime rejects it as an
	// invalid action, so the Decision is committed and visible in Trace.
	encoded := `"{\"examples\":[],\"settingsDigest\":\"x\"}"`
	decision := finalAnswer(t, raw(photoOutputSchema), encoded)
	if diff := cmp.Diff(raw(encoded), decision.Output); diff != "" {
		t.Errorf("Output mismatch (-want +got):\n%s", diff)
	}
	err := runtime.ValidateFinalOutput(decision.Output, raw(photoOutputSchema))
	if _, ok := runtime.AsInvalidActionError(err); !ok {
		t.Fatalf("ValidateFinalOutput() error = %v, want an invalid action", err)
	}
}

func TestProvider_Generate_TextFormAnswerWithObjectOutput_PassesToRuntime(t *testing.T) {
	decision := finalAnswer(t, nil, `{"answer":"Paris"}`)
	err := runtime.ValidateFinalOutput(decision.Output, nil)
	if _, ok := runtime.AsInvalidActionError(err); !ok {
		t.Fatalf("ValidateFinalOutput() error = %v, want an invalid action", err)
	}
}

// stopped answers one request with a choice that carries the given message members and
// finish reasons, and returns the Adapter error.
func stopped(t *testing.T, choice string) *openaimodel.Error {
	t.Helper()
	server, _ := fakeProvider(t, http.StatusOK, `{"id":"chatcmpl-stop","choices":[`+choice+`]}`)
	provider := newProvider(t, server.URL, server.Client())
	request := agentRequest()
	request.FinalOutputSchema = raw(photoOutputSchema)

	_, err := provider.Generate(context.Background(), request)
	var modelErr *openaimodel.Error
	if !errors.As(err, &modelErr) {
		t.Fatalf("Generate() error = %v, want *openaimodel.Error", err)
	}
	if modelErr.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", modelErr.StatusCode)
	}
	return modelErr
}

// messageContent encodes content as the JSON value of message.content.
func messageContent(t *testing.T, content string) string {
	t.Helper()
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

const (
	refusedText   = "model refused to answer"
	truncatedText = "response truncated at the output token limit"
)

func TestProvider_Generate_TruncatedAnswer_ReturnsTruncationError(t *testing.T) {
	complete := `{"kind":"FINAL","tool":null,"arguments":null,"output":{"examples":[],"settingsDigest":"x"},"statePatch":null}`
	partial := `{"kind":"FINAL","tool":null,"arguments":null,"output":{"examples":[{"photoAsse`
	for _, tc := range []struct {
		name    string
		content string
	}{
		// A complete, valid Decision proves the content is not parsed at all: parsing
		// it would have succeeded.
		{"complete content", complete},
		{"partial content", partial},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := stopped(t, `{"index":0,"message":{"role":"assistant","content":`+messageContent(t, tc.content)+`},"finish_reason":"length"}`)
			message := err.Error()
			if !strings.Contains(message, truncatedText) {
				t.Errorf("error = %q, want it to say %q", message, truncatedText)
			}
			for _, misleading := range []string{"schema", "not JSON", refusedText} {
				if strings.Contains(message, misleading) {
					t.Errorf("error = %q, must not report a truncation as %q", message, misleading)
				}
			}
			if strings.Contains(message, "photoAsse") {
				t.Errorf("error = %q repeats the truncated content", message)
			}
		})
	}
}

func TestProvider_Generate_RefusalStopReason_ReturnsRefusalError(t *testing.T) {
	refusal := "I cannot help with that request."
	valid := messageContent(t, `{"kind":"FINAL","tool":null,"arguments":null,"output":{"examples":[],"settingsDigest":"x"},"statePatch":null}`)
	for _, tc := range []struct {
		name   string
		choice string
	}{
		{"refusal message", `{"message":{"content":null,"refusal":` + messageContent(t, refusal) + `},"finish_reason":"stop"}`},
		{"refusal message with content", `{"message":{"content":` + valid + `,"refusal":` + messageContent(t, refusal) + `},"finish_reason":"stop"}`},
		{"content_filter finish reason", `{"message":{"content":` + valid + `},"finish_reason":"content_filter"}`},
		{"content_filter without content", `{"message":{"content":null},"finish_reason":"content_filter"}`},
		{"refusal finish reason", `{"message":{"content":` + messageContent(t, refusal) + `},"finish_reason":"refusal"}`},
		{"refusal native finish reason", `{"message":{"content":` + messageContent(t, refusal) + `},"finish_reason":"stop","native_finish_reason":"refusal"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := stopped(t, tc.choice).Error()
			if !strings.Contains(message, refusedText) {
				t.Errorf("error = %q, want it to say %q", message, refusedText)
			}
			for _, other := range []string{truncatedText, "schema", "not JSON", "cannot help"} {
				if strings.Contains(message, other) {
					t.Errorf("error = %q, a refusal must not read as %q", message, other)
				}
			}
		})
	}
}

func TestProvider_Generate_StopReasons_AreDistinguishableFromEnvelopeMismatch(t *testing.T) {
	refused := stopped(t, `{"message":{"content":null},"finish_reason":"content_filter"}`).Error()
	truncated := stopped(t, `{"message":{"content":"{\"kind\""},"finish_reason":"length"}`).Error()
	mismatch := stopped(t, `{"message":{"content":"{\"kind\":\"FINAL\"}"},"finish_reason":"stop"}`).Error()
	if refused == truncated || refused == mismatch || truncated == mismatch {
		t.Errorf("errors are not distinguishable: refused %q, truncated %q, mismatch %q", refused, truncated, mismatch)
	}
	if strings.Contains(mismatch, refusedText) || strings.Contains(mismatch, truncatedText) {
		t.Errorf("envelope mismatch error = %q, must not read as a refusal or truncation", mismatch)
	}
}
