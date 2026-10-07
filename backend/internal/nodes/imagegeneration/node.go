// Package imagegeneration implements the built-in Image Generation Node. It is the
// first ASYNC Node: Execute only dispatches one external task and returns its identity,
// and OnCallback normalises the Provider's callback payload into the node's `image` output
// once the Runtime restores the original Attempt.
//
// The `image` port carries a domain.ImageRef and nothing else: the Provider's own
// response object is validated and re-encoded here, so a Provider-private field or a
// temporarily signed URL cannot travel downstream inside a NodeRun output, Run.output, an
// Event or Trace.
//
// It owns no NodeRun state, no retry, no timeout and no Event of its own, and it never
// generates a callback identity: the Runtime supplies the callback URL, the one-time
// plaintext token and the idempotency key, and this node forwards them unchanged.
package imagegeneration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const nodeType = "image_generation"

// promptPort, referencePort and imagePort are the node's three fixed handles.
// referencePort is optional: an AIGC Run may or may not start from a Reference Image.
const (
	promptPort    = "prompt"
	referencePort = "reference"
	imagePort     = "image"
)

// Callback payload statuses. The Provider reports one of them; anything else is a payload
// this node cannot interpret.
const (
	statusSucceeded = "SUCCEEDED"
	statusFailed    = "FAILED"
)

// defaultFailureCode is used when the Provider reports FAILED without naming a code, so a
// persisted ExecutionError always carries one.
const defaultFailureCode = "PROVIDER_TASK_FAILED"

// configSchema is the Image Generation config contract, reproduced exactly: a Model ID
// and the requested image width.
const configSchema = `{
  "type": "object",
  "properties": {
    "modelId": {"type": "string"},
    "width": {"type": "integer", "minimum": 256}
  },
  "required": ["modelId", "width"]
}`

// ModelResolver resolves a Model ID to its registration and serving ModelProvider.
// *registry.ModelRegistry satisfies it; this node depends only on the narrow shape it
// calls. Image Generation resolves a Model ID rather than a Provider because its config
// contract renders `modelId` with a MODEL_SELECTOR filtered by the image_generation
// capability.
type ModelResolver interface {
	Get(modelID string) (registry.ModelRegistration, registry.ModelProvider, bool)
}

// TaskDispatcher is the one Provider interaction this node needs: accept one image
// generation task and answer with the external task identity. An Adapter implements it.
//
// The dependency is expressed with registry types and plain values rather than an
// Adapter-owned request struct because a node package may import only registry and domain
// (test/architecture), so node and Adapter share no type of their own.
type TaskDispatcher interface {
	// Dispatch forwards prompt, the optional Reference Image and the frozen node config
	// to the Provider together with the Attempt-scoped callback context and the
	// Runtime's idempotency key. A nil reference means the Run supplied none. An empty
	// idempotencyKey means the Provider receives no key at all.
	//
	// The reference is the validated, credential-free domain.ImageRef the `reference`
	// port carried. An Adapter forwards it as-is: resolving it to Asset content or to a
	// signed URL is not a Provider interaction this contract permits.
	Dispatch(ctx context.Context, prompt string, reference *domain.ImageRef, options map[string]any, callback registry.CallbackContext, idempotencyKey string) (registry.ExternalTask, error)
}

// Registration returns the Image Generation NodeRegistration bound to resolver and
// dispatcher. Both are consulted per call, never cached across calls, so a Registry update
// between Definition save and Run creation is observed correctly.
func Registration(resolver ModelResolver, dispatcher TaskDispatcher) registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          nodeType,
			DisplayName:   "Image Generation",
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionAsync,
			Inputs: []domain.PortMetadata{
				{Name: promptPort, DataType: domain.PortTypeText, Required: true},
				{Name: referencePort, DataType: domain.PortTypeImage, Required: false},
			},
			Outputs:      []domain.PortMetadata{{Name: imagePort, DataType: domain.PortTypeImage, Required: true}},
			ConfigSchema: json.RawMessage(configSchema),
			UISchema: domain.NodeUISchema{Fields: []domain.UIField{
				{Path: "modelId", Order: 10, Group: domain.UIGroupModel, Widget: domain.UIWidgetModelSelector, Capability: domain.ModelCapabilityImageGeneration},
				{Path: "width", Order: 20, Group: domain.UIGroupModelParameters, Widget: domain.UIWidgetDefault},
			}},
			// The dispatch carries the Runtime's stable idempotency key, so the Provider
			// - not Emberling - decides that a replayed dispatch is the same task
			// (EXTERNAL + KEYED).
			SideEffect: domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed},
		},
		Binding: registry.ExecutorBinding{Executor: Executor{resolver: resolver, dispatcher: dispatcher}},
	}
}

// Executor implements registry.AsyncNodeExecutor for the Image Generation Node.
type Executor struct {
	resolver   ModelResolver
	dispatcher TaskDispatcher
}

// ValidateSemantics checks what ConfigSchema cannot express: the referenced model exists
// and declares the image_generation capability.
func (e Executor) ValidateSemantics(_ context.Context, config map[string]any) error {
	modelID, _ := config["modelId"].(string)
	if modelID == "" {
		return fmt.Errorf("image_generation: modelId is required")
	}
	model, _, ok := e.resolver.Get(modelID)
	if !ok {
		return fmt.Errorf("image_generation: model %q is not registered", modelID)
	}
	if !model.HasCapability(domain.ModelCapabilityImageGeneration) {
		return fmt.Errorf("image_generation: model %q lacks capability %q", modelID, domain.ModelCapabilityImageGeneration)
	}
	return nil
}

// Execute dispatches one external task and returns DISPATCHED. It never returns a
// COMPLETED result: this node cannot know the outcome within one call, and the Runtime
// persists the Callback Binding from the ExternalTask returned here before anything else
// can advance.
func (e Executor) Execute(ctx context.Context, input registry.NodeInput, config map[string]any) (registry.NodeResult, error) {
	if input.Callback == nil {
		// Without the Attempt-scoped callback URL and token there is nothing to hand the
		// Provider, and a dispatched task could never be reported back. Failing here
		// keeps the external side effect from happening at all.
		return registry.NodeResult{}, fmt.Errorf("image_generation: asynchronous dispatch requires a callback context")
	}

	modelID, _ := config["modelId"].(string)
	if _, _, ok := e.resolver.Get(modelID); !ok {
		return registry.NodeResult{}, fmt.Errorf("image_generation: model %q is not registered", modelID)
	}

	promptRaw, present := input.Port(promptPort)
	if !present {
		return registry.NodeResult{}, fmt.Errorf("image_generation: required input port %q is missing", promptPort)
	}
	var promptText string
	if err := json.Unmarshal(promptRaw, &promptText); err != nil {
		return registry.NodeResult{}, fmt.Errorf("image_generation: input port %q is not a JSON string: %w", promptPort, err)
	}

	reference, err := referenceImage(input)
	if err != nil {
		// Returned before the dispatch, so a value this node cannot vouch for never
		// becomes an external side effect.
		return registry.NodeResult{}, err
	}

	task, err := e.dispatcher.Dispatch(ctx, promptText, reference, config, *input.Callback, input.IdempotencyKey)
	if err != nil {
		// Wrapped, not replaced: the Adapter's error carries the uncertainty
		// classification the Execution Service reads with errors.As.
		return registry.NodeResult{}, fmt.Errorf("image_generation: dispatch: %w", err)
	}
	if task.ProviderID == "" || task.ExternalTaskID == "" {
		return registry.NodeResult{}, fmt.Errorf("image_generation: dispatch returned an incomplete external task identity")
	}

	return registry.NodeResult{Kind: registry.NodeResultDispatched, ExternalTask: &task}, nil
}

// referenceImage reads the optional `reference` port. An unconnected port and a connected
// port carrying JSON null are the same absence: an Image Input whose optional Run input
// key was not supplied publishes an explicit null rather than a substituted image, and
// this node must not turn that into a Reference Image the Provider never received.
//
// A present value is parsed through domain.ParseImageRef, so only a valid, credential-free
// ImageRef reaches the Adapter. The error never quotes the port value: it reaches Trace.
func referenceImage(input registry.NodeInput) (*domain.ImageRef, error) {
	raw, present := input.Port(referencePort)
	if !present || len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	reference, err := domain.ParseImageRef(raw)
	if err != nil {
		return nil, fmt.Errorf("image_generation: input port %q does not carry a valid ImageRef: %w", referencePort, err)
	}
	return &reference, nil
}

// callbackPayload is the part of the Provider's callback payload this node interprets.
// `status` is the discriminator; `image` carries the Provider's image reference, which is
// normalised into a domain.ImageRef before it reaches the output port, and `error`
// describes a reported failure.
type callbackPayload struct {
	Status string          `json:"status"`
	Image  json.RawMessage `json:"image"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// OnCallback converts one Provider callback payload into the node's output.
//
// A reported task failure becomes a *registry.ProviderFailure, which fails the Attempt. A
// payload this node cannot interpret stays a plain error, which leaves the NodeRun
// WAITING_CALLBACK until its Attempt deadline decides the outcome: a malformed
// delivery is not evidence that the external task failed.
func (e Executor) OnCallback(_ context.Context, state registry.NodeAsyncState, payload []byte) (registry.NodeOutput, error) {
	var body callbackPayload
	if err := json.Unmarshal(payload, &body); err != nil {
		return registry.NodeOutput{}, fmt.Errorf("image_generation: callback payload for external task %s is not a JSON object: %w", state.ExternalTask.ExternalTaskID, err)
	}

	switch body.Status {
	case statusSucceeded:
		// The Provider's object is normalised, never republished: only a valid ImageRef
		// may reach an `image` port. A Provider-private member, a signed URL or
		// a reference shape this node does not recognise fails the payload instead.
		reference, err := domain.ParseImageRef(body.Image)
		if err != nil {
			// err names the offending rule and never contains the payload, so the message
			// is safe for Trace.
			return registry.NodeOutput{}, fmt.Errorf("image_generation: callback payload for external task %s reports %s without a valid %q reference: %w", state.ExternalTask.ExternalTaskID, statusSucceeded, imagePort, err)
		}
		// The canonical encoding of the parsed reference is what the port carries, so a
		// downstream node reads one shape whatever the Provider sent.
		encoded, err := json.Marshal(reference)
		if err != nil {
			return registry.NodeOutput{}, fmt.Errorf("image_generation: callback payload for external task %s: encode %q reference: %w", state.ExternalTask.ExternalTaskID, imagePort, err)
		}
		return registry.NodeOutput{Ports: map[string]json.RawMessage{imagePort: encoded}}, nil

	case statusFailed:
		code := defaultFailureCode
		message := "provider reported the external task failed"
		if body.Error != nil {
			if body.Error.Code != "" {
				code = body.Error.Code
			}
			if body.Error.Message != "" {
				message = body.Error.Message
			}
		}
		return registry.NodeOutput{}, &registry.ProviderFailure{Err: domain.ExecutionError{Code: code, Message: message}}

	default:
		return registry.NodeOutput{}, fmt.Errorf("image_generation: callback payload for external task %s carries status %q, want %s or %s", state.ExternalTask.ExternalTaskID, body.Status, statusSucceeded, statusFailed)
	}
}
