package imagegeneration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mocktask"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// stubResolver is a minimal ModelResolver backed by a fixed model.
type stubResolver struct {
	model registry.ModelRegistration
	ok    bool
}

func (s stubResolver) Get(string) (registry.ModelRegistration, registry.ModelProvider, bool) {
	if !s.ok {
		return registry.ModelRegistration{}, nil, false
	}
	return s.model, nil, true
}

func imageModel() registry.ModelRegistration {
	return registry.ModelRegistration{ModelMetadata: domain.ModelMetadata{
		ID:           "image-model-v1",
		DisplayName:  "Image Model v1",
		Capabilities: []string{domain.ModelCapabilityImageGeneration},
		ConfigSchema: json.RawMessage(`{"type": "object"}`),
	}}
}

// recordingDispatcher records the one Dispatch call a test makes and returns a fixed task.
type recordingDispatcher struct {
	prompt         string
	reference      *domain.ImageRef
	options        map[string]any
	callback       registry.CallbackContext
	idempotencyKey string
	calls          int
	task           registry.ExternalTask
	err            error
}

func (d *recordingDispatcher) Dispatch(_ context.Context, prompt string, reference *domain.ImageRef, options map[string]any, callback registry.CallbackContext, idempotencyKey string) (registry.ExternalTask, error) {
	d.calls++
	d.prompt = prompt
	d.reference = reference
	d.options = options
	d.callback = callback
	d.idempotencyKey = idempotencyKey
	return d.task, d.err
}

func validConfig() map[string]any {
	return map[string]any{"modelId": "image-model-v1", "width": float64(1024)}
}

func promptInput(callback *registry.CallbackContext, idempotencyKey string) registry.NodeInput {
	return registry.NodeInput{
		RunID:          "run_1",
		NodeRunID:      "nr_1",
		AttemptNo:      1,
		Ports:          map[string]json.RawMessage{"prompt": json.RawMessage(`"a small ember creature"`)},
		Callback:       callback,
		IdempotencyKey: idempotencyKey,
	}
}

// TestImageGenerationNode_Metadata_MatchesInterfaceSpec pins the registered metadata to
// the Node Metadata Contract: an ASYNC, EXTERNAL + KEYED node with one required
// `prompt` text input and one required `image` output.
func TestImageGenerationNode_Metadata_MatchesInterfaceSpec(t *testing.T) {
	meta := Registration(stubResolver{model: imageModel(), ok: true}, &recordingDispatcher{}).Metadata

	if meta.Type != "image_generation" {
		t.Fatalf("Type = %q, want %q", meta.Type, "image_generation")
	}
	if meta.DisplayName != "Image Generation" {
		t.Fatalf("DisplayName = %q, want %q", meta.DisplayName, "Image Generation")
	}
	if meta.Category != domain.NodeCategoryPromptAndModel {
		t.Fatalf("Category = %q, want %q", meta.Category, domain.NodeCategoryPromptAndModel)
	}
	if meta.ExecutionKind != domain.NodeExecutionAsync {
		t.Fatalf("ExecutionKind = %q, want %q", meta.ExecutionKind, domain.NodeExecutionAsync)
	}
	wantSideEffect := domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed}
	if meta.SideEffect != wantSideEffect {
		t.Fatalf("SideEffect = %+v, want %+v", meta.SideEffect, wantSideEffect)
	}
	wantInputs := []domain.PortMetadata{
		{Name: "prompt", DataType: domain.PortTypeText, Required: true},
		{Name: "reference", DataType: domain.PortTypeImage, Required: false},
	}
	if len(meta.Inputs) != len(wantInputs) || meta.Inputs[0] != wantInputs[0] || meta.Inputs[1] != wantInputs[1] {
		t.Fatalf("Inputs = %+v, want %+v", meta.Inputs, wantInputs)
	}
	wantOutput := domain.PortMetadata{Name: "image", DataType: domain.PortTypeImage, Required: true}
	if len(meta.Outputs) != 1 || meta.Outputs[0] != wantOutput {
		t.Fatalf("Outputs = %+v, want [%+v]", meta.Outputs, wantOutput)
	}

	var schema map[string]any
	if err := json.Unmarshal(meta.ConfigSchema, &schema); err != nil {
		t.Fatalf("ConfigSchema is not valid JSON: %v", err)
	}
	want := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"modelId": map[string]any{"type": "string"},
			"width":   map[string]any{"type": "integer", "minimum": float64(256)},
		},
		"required": []any{"modelId", "width"},
	}
	got, _ := json.Marshal(schema)
	wantJSON, _ := json.Marshal(want)
	if string(got) != string(wantJSON) {
		t.Fatalf("ConfigSchema = %s, want %s", got, wantJSON)
	}
}

// TestRegistration_Validate_Passes proves the registration satisfies the Registry's own
// startup checks, including "an ASYNC Executor must implement AsyncNodeExecutor".
func TestRegistration_Validate_Passes(t *testing.T) {
	reg := Registration(stubResolver{model: imageModel(), ok: true}, &recordingDispatcher{})
	if err := registry.NewNodeRegistry().Register(reg); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
}

func TestImageGenerationExecutor_ValidateSemantics_UnknownModelFails(t *testing.T) {
	e := Executor{resolver: stubResolver{ok: false}, dispatcher: &recordingDispatcher{}}
	if err := e.ValidateSemantics(context.Background(), validConfig()); err == nil {
		t.Fatal("ValidateSemantics() error = nil, want error for unregistered model")
	}
}

func TestImageGenerationExecutor_ValidateSemantics_MissingCapabilityFails(t *testing.T) {
	model := imageModel()
	model.Capabilities = []string{domain.ModelCapabilityTextGeneration}
	e := Executor{resolver: stubResolver{model: model, ok: true}, dispatcher: &recordingDispatcher{}}
	if err := e.ValidateSemantics(context.Background(), validConfig()); err == nil {
		t.Fatal("ValidateSemantics() error = nil, want error for a model without image_generation capability")
	}
}

// TestImageGenerationNode_Execute_WithoutCallbackContext_Fails: an ASYNC dispatch has no
// meaning without the Attempt-scoped callback URL and token the Runtime issues,
// so Execute must fail loudly instead of dispatching a task nobody can report back on.
func TestImageGenerationNode_Execute_WithoutCallbackContext_Fails(t *testing.T) {
	dispatcher := &recordingDispatcher{task: registry.ExternalTask{ProviderID: "p", ExternalTaskID: "t"}}
	e := Executor{resolver: stubResolver{model: imageModel(), ok: true}, dispatcher: dispatcher}

	result, err := e.Execute(context.Background(), promptInput(nil, "nr_1"), validConfig())
	if err == nil {
		t.Fatalf("Execute() error = nil, want error; result = %+v", result)
	}
	if dispatcher.calls != 0 {
		t.Fatalf("dispatcher calls = %d, want 0 (no external call without a callback context)", dispatcher.calls)
	}
	if result.Kind != "" {
		t.Fatalf("Execute() result kind = %q, want the zero value (never a sync result)", result.Kind)
	}
}

// TestImageGenerationNode_Execute_ForwardsIdempotencyKeyAndReturnsDispatched drives the
// node through the real mocktask Adapter against an httptest Provider, so the assertion
// covers the wire body an external Provider actually receives.
func TestImageGenerationNode_Execute_ForwardsIdempotencyKeyAndReturnsDispatched(t *testing.T) {
	var body map[string]any
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode dispatch body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"externalTaskId":"provider_task_789"}`))
	}))
	t.Cleanup(server.Close)

	e := Executor{
		resolver:   stubResolver{model: imageModel(), ok: true},
		dispatcher: mocktask.New(server.URL, server.Client()),
	}
	callback := registry.CallbackContext{URL: "http://emberling.test/callbacks/attempt_1", Token: "plaintext-callback-token"}

	result, err := e.Execute(context.Background(), promptInput(&callback, "nr_1"), validConfig())
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if path != "/v1/tasks" {
		t.Fatalf("dispatch path = %q, want %q", path, "/v1/tasks")
	}
	if got := body["idempotencyKey"]; got != "nr_1" {
		t.Fatalf("idempotencyKey = %v, want %q (forwarded verbatim)", got, "nr_1")
	}
	if got := body["callbackUrl"]; got != callback.URL {
		t.Fatalf("callbackUrl = %v, want %q", got, callback.URL)
	}
	if got := body["callbackToken"]; got != callback.Token {
		t.Fatalf("callbackToken = %v, want the plaintext token the Runtime issued", got)
	}
	if result.Kind != registry.NodeResultDispatched {
		t.Fatalf("result.Kind = %q, want %q", result.Kind, registry.NodeResultDispatched)
	}
	if result.Output != nil {
		t.Fatalf("result.Output = %+v, want nil for a DISPATCHED result", result.Output)
	}
	if result.ExternalTask == nil || result.ExternalTask.ExternalTaskID != "provider_task_789" {
		t.Fatalf("result.ExternalTask = %+v, want the Provider's task id", result.ExternalTask)
	}
	if result.ExternalTask.ProviderID == "" {
		t.Fatal("result.ExternalTask.ProviderID is empty, want the stable Provider identifier")
	}
}

// TestImageGenerationNode_Execute_EmptyIdempotencyKeyIsNotInvented: an empty key means the
// Provider gets no key at all; the Adapter must never generate one of its own.
func TestImageGenerationNode_Execute_EmptyIdempotencyKeyIsNotInvented(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode dispatch body: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"externalTaskId":"provider_task_789"}`))
	}))
	t.Cleanup(server.Close)

	e := Executor{resolver: stubResolver{model: imageModel(), ok: true}, dispatcher: mocktask.New(server.URL, server.Client())}
	callback := registry.CallbackContext{URL: "http://emberling.test/callbacks/attempt_1", Token: "plaintext-callback-token"}
	if _, err := e.Execute(context.Background(), promptInput(&callback, ""), validConfig()); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if got, present := body["idempotencyKey"]; present {
		t.Fatalf("idempotencyKey = %v, want the field to be absent", got)
	}
}

// TestImageGenerationNode_OnCallback_SucceededPayload_ProducesNormalisedImageRef: the
// `image` port carries the canonical encoding of the parsed domain.ImageRef, not the
// Provider's bytes. Re-encoding is what guarantees a downstream node sees one shape and
// that nothing the Provider added alongside the reference can ride along.
func TestImageGenerationNode_OnCallback_SucceededPayload_ProducesNormalisedImageRef(t *testing.T) {
	e := Executor{resolver: stubResolver{model: imageModel(), ok: true}, dispatcher: &recordingDispatcher{}}
	cases := map[string]struct{ image, want string }{
		"external": {
			image: `{"mediaType":"image/png","width":1024,"uri":"http://provider.test/v1/images/abc.png","source":"EXTERNAL"}`,
			want:  `{"source":"EXTERNAL","uri":"http://provider.test/v1/images/abc.png","mediaType":"image/png","width":1024}`,
		},
		"asset": {
			image: `{"source":"ASSET","asset":{"assetId":"asset_123","mediaType":"image/png","sizeBytes":102400,"sha256":"abc"}}`,
			want:  `{"source":"ASSET","asset":{"assetId":"asset_123","mediaType":"image/png","sizeBytes":102400,"sha256":"abc"}}`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			payload := []byte(`{"status":"SUCCEEDED","image":` + tc.image + `}`)
			output, err := e.OnCallback(context.Background(), asyncState(), payload)
			if err != nil {
				t.Fatalf("OnCallback() error = %v, want nil", err)
			}
			raw, ok := output.Ports["image"]
			if !ok {
				t.Fatalf("output ports = %v, want an %q port", output.Ports, "image")
			}
			if string(raw) != tc.want {
				t.Fatalf("image port = %s, want the canonical ImageRef encoding %s", raw, tc.want)
			}
			if _, err := domain.ParseImageRef(raw); err != nil {
				t.Fatalf("image port is not a valid ImageRef: %v", err)
			}
			if len(output.Ports) != 1 {
				t.Fatalf("output ports = %v, want exactly one port", output.Ports)
			}
		})
	}
}

// TestImageGenerationNode_OnCallback_ImageIsNotAnImageRef_ReturnsPlainError: a Provider
// object that is not an ImageRef -- the legacy `{"url":...}` shape, a private field
// alongside the reference, a branch that contradicts its own `source` -- must not reach the
// `image` port. It is a payload this node cannot interpret, so it stays a plain error and
// the NodeRun stays WAITING_CALLBACK.
func TestImageGenerationNode_OnCallback_ImageIsNotAnImageRef_ReturnsPlainError(t *testing.T) {
	e := Executor{resolver: stubResolver{model: imageModel(), ok: true}, dispatcher: &recordingDispatcher{}}
	cases := map[string]string{
		"legacy url shape":      `{"url":"http://provider.test/v1/images/abc.png","mediaType":"image/png","width":1024}`,
		"no source":             `{"uri":"http://provider.test/v1/images/abc.png","mediaType":"image/png"}`,
		"unknown source":        `{"source":"URL","uri":"http://provider.test/a.png","mediaType":"image/png"}`,
		"provider private key":  `{"source":"EXTERNAL","uri":"http://provider.test/a.png","mediaType":"image/png","providerJobId":"job_1"}`,
		"external without uri":  `{"source":"EXTERNAL","mediaType":"image/png"}`,
		"asset branch mismatch": `{"source":"ASSET","uri":"http://provider.test/a.png","mediaType":"image/png"}`,
		"incomplete asset":      `{"source":"ASSET","asset":{"assetId":"asset_123"}}`,
		"relative uri":          `{"source":"EXTERNAL","uri":"/v1/images/abc.png","mediaType":"image/png"}`,
	}
	for name, image := range cases {
		t.Run(name, func(t *testing.T) {
			payload := []byte(`{"status":"SUCCEEDED","image":` + image + `}`)
			output, err := e.OnCallback(context.Background(), asyncState(), payload)
			if err == nil {
				t.Fatalf("OnCallback() error = nil, want a plain error; output = %+v", output)
			}
			var failure *registry.ProviderFailure
			if errors.As(err, &failure) {
				t.Fatalf("OnCallback() error = %v, want a plain error, not a ProviderFailure", err)
			}
			if strings.Contains(err.Error(), "provider.test") {
				t.Errorf("OnCallback() error echoes the Provider payload back: %v", err)
			}
		})
	}
}

func TestImageGenerationNode_OnCallback_FailedPayload_ReturnsProviderFailure(t *testing.T) {
	e := Executor{resolver: stubResolver{model: imageModel(), ok: true}, dispatcher: &recordingDispatcher{}}
	payload := []byte(`{"status":"FAILED","error":{"code":"PROVIDER_TASK_FAILED","message":"mock provider: task failed by scenario"}}`)

	output, err := e.OnCallback(context.Background(), asyncState(), payload)
	if err == nil {
		t.Fatalf("OnCallback() error = nil, want a ProviderFailure; output = %+v", output)
	}
	var failure *registry.ProviderFailure
	if !errors.As(err, &failure) {
		t.Fatalf("OnCallback() error = %v (%T), want errors.As *registry.ProviderFailure", err, err)
	}
	if failure.Err.Code != "PROVIDER_TASK_FAILED" {
		t.Fatalf("ProviderFailure code = %q, want %q", failure.Err.Code, "PROVIDER_TASK_FAILED")
	}
	if failure.Err.Message == "" {
		t.Fatal("ProviderFailure message is empty, want the Provider's reported message")
	}
}

// TestImageGenerationNode_OnCallback_MalformedPayload_ReturnsPlainError: a payload that
// cannot be interpreted is not a Provider-reported task failure. It must stay a plain
// error, which leaves the NodeRun WAITING_CALLBACK (registry.AsyncNodeExecutor contract).
func TestImageGenerationNode_OnCallback_MalformedPayload_ReturnsPlainError(t *testing.T) {
	e := Executor{resolver: stubResolver{model: imageModel(), ok: true}, dispatcher: &recordingDispatcher{}}
	cases := map[string]string{
		"not json":             `}{`,
		"unknown status":       `{"status":"RUNNING"}`,
		"succeeded no image":   `{"status":"SUCCEEDED"}`,
		"succeeded not object": `{"status":"SUCCEEDED","image":"http://provider.test/abc.png"}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := e.OnCallback(context.Background(), asyncState(), []byte(payload))
			if err == nil {
				t.Fatal("OnCallback() error = nil, want a plain error")
			}
			var failure *registry.ProviderFailure
			if errors.As(err, &failure) {
				t.Fatalf("OnCallback() error = %v, want a plain error, not a ProviderFailure", err)
			}
		})
	}
}

func asyncState() registry.NodeAsyncState {
	return registry.NodeAsyncState{
		RunID:        "run_1",
		NodeRunID:    "nr_1",
		AttemptNo:    1,
		ExternalTask: registry.ExternalTask{ProviderID: mocktask.ProviderID, ExternalTaskID: "provider_task_789"},
		Config:       validConfig(),
	}
}

// assetReferencePort is one `source: ASSET` ImageRef as an image_input Node publishes it.
const assetReferencePort = `{"source":"ASSET","asset":{"assetId":"asset_123","mediaType":"image/png","sizeBytes":102400,"sha256":"abc"}}`

// TestImageGenerationNode_Execute_ReferenceImageRefReachesTheDispatcher pins the optional
// `reference` input port: when an upstream Node publishes an ImageRef on it, the
// parsed reference travels to the Adapter with the dispatch, so the Provider request can
// name the source image.
func TestImageGenerationNode_Execute_ReferenceImageRefReachesTheDispatcher(t *testing.T) {
	dispatcher := &recordingDispatcher{task: registry.ExternalTask{ProviderID: "p", ExternalTaskID: "t"}}
	e := Executor{resolver: stubResolver{model: imageModel(), ok: true}, dispatcher: dispatcher}
	callback := registry.CallbackContext{URL: "http://emberling.test/callbacks/attempt_1", Token: "plaintext-callback-token"}
	input := promptInput(&callback, "nr_1")
	input.Ports["reference"] = json.RawMessage(assetReferencePort)

	if _, err := e.Execute(context.Background(), input, validConfig()); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if dispatcher.reference == nil {
		t.Fatal("dispatcher received no reference, want the ASSET ImageRef from the `reference` port")
	}
	if dispatcher.reference.Source != domain.ImageSourceAsset || dispatcher.reference.Asset == nil {
		t.Fatalf("dispatched reference = %+v, want an ASSET ImageRef", *dispatcher.reference)
	}
	want := domain.AssetRef{AssetID: "asset_123", MediaType: "image/png", SizeBytes: 102400, SHA256: "abc"}
	if *dispatcher.reference.Asset != want {
		t.Fatalf("dispatched reference asset = %+v, want %+v", *dispatcher.reference.Asset, want)
	}
}

// TestImageGenerationNode_Execute_AbsentReferenceDispatchesWithoutOne covers the optional
// half of the port: the Node dispatches exactly as before when nothing is connected, and
// an unconnected optional port carrying JSON null is the same absence.
func TestImageGenerationNode_Execute_AbsentReferenceDispatchesWithoutOne(t *testing.T) {
	for name, ports := range map[string]map[string]json.RawMessage{
		"port missing": nil,
		"port null":    {"reference": json.RawMessage(`null`)},
	} {
		t.Run(name, func(t *testing.T) {
			dispatcher := &recordingDispatcher{task: registry.ExternalTask{ProviderID: "p", ExternalTaskID: "t"}}
			e := Executor{resolver: stubResolver{model: imageModel(), ok: true}, dispatcher: dispatcher}
			callback := registry.CallbackContext{URL: "http://emberling.test/callbacks/attempt_1", Token: "plaintext-callback-token"}
			input := promptInput(&callback, "nr_1")
			for port, value := range ports {
				input.Ports[port] = value
			}

			if _, err := e.Execute(context.Background(), input, validConfig()); err != nil {
				t.Fatalf("Execute() error = %v, want nil", err)
			}
			if dispatcher.reference != nil {
				t.Fatalf("dispatched reference = %+v, want nil", *dispatcher.reference)
			}
		})
	}
}

// TestImageGenerationNode_Execute_ReferenceIsNotAnImageRef_DoesNotDispatch keeps the
// external side effect from happening at all when the connected value is not a reference
// this node can normalise.
func TestImageGenerationNode_Execute_ReferenceIsNotAnImageRef_DoesNotDispatch(t *testing.T) {
	dispatcher := &recordingDispatcher{task: registry.ExternalTask{ProviderID: "p", ExternalTaskID: "t"}}
	e := Executor{resolver: stubResolver{model: imageModel(), ok: true}, dispatcher: dispatcher}
	callback := registry.CallbackContext{URL: "http://emberling.test/callbacks/attempt_1", Token: "plaintext-callback-token"}
	input := promptInput(&callback, "nr_1")
	input.Ports["reference"] = json.RawMessage(`{"source":"ASSET"}`)

	if _, err := e.Execute(context.Background(), input, validConfig()); err == nil {
		t.Fatal("Execute() error = nil, want error for a `reference` value that is not a valid ImageRef")
	}
	if dispatcher.calls != 0 {
		t.Fatalf("dispatcher calls = %d, want 0 (no external call for an invalid reference)", dispatcher.calls)
	}
}
