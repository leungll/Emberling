package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// stubNodeExecutor is a minimal NodeExecutor used to test Registry rules in isolation from
// any real node implementation.
type stubNodeExecutor struct {
	validateErr error
}

func (s *stubNodeExecutor) ValidateSemantics(ctx context.Context, config map[string]any) error {
	return s.validateErr
}

func (s *stubNodeExecutor) Execute(ctx context.Context, input NodeInput, config map[string]any) (NodeResult, error) {
	return CompletedResult(nil), nil
}

// stubAsyncNodeExecutor additionally implements AsyncNodeExecutor.
type stubAsyncNodeExecutor struct {
	stubNodeExecutor
}

func (s *stubAsyncNodeExecutor) OnCallback(ctx context.Context, state NodeAsyncState, payload []byte) (NodeOutput, error) {
	return NodeOutput{}, nil
}

// validSyncNodeRegistration returns a registration that must pass every startup rule so
// each failing test can mutate exactly one fact and prove which rule rejected it.
func validSyncNodeRegistration() NodeRegistration {
	return NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          "text_input",
			DisplayName:   "Text Input",
			Category:      domain.NodeCategoryInput,
			ExecutionKind: domain.NodeExecutionSync,
			Outputs:       []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			ConfigSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"inputKey": {"type": "string", "minLength": 1},
					"label": {"type": "string"},
					"required": {"type": "boolean"},
					"mode": {"type": "string", "enum": ["a", "b"]}
				},
				"required": ["inputKey", "required"],
				"additionalProperties": false
			}`),
			UISchema: domain.NodeUISchema{Fields: []domain.UIField{
				{Path: "inputKey", Order: 10, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
				{Path: "mode", Order: 20, Group: domain.UIGroupBasic, Widget: domain.UIWidgetSelect},
			}},
			SideEffect: domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		Binding: ExecutorBinding{Executor: &stubNodeExecutor{}},
	}
}

func validAsyncNodeRegistration() NodeRegistration {
	reg := validSyncNodeRegistration()
	reg.Metadata.Type = "image_generation"
	reg.Metadata.ExecutionKind = domain.NodeExecutionAsync
	reg.Binding = ExecutorBinding{Executor: &stubAsyncNodeExecutor{}}
	return reg
}

func validManagedAgentRegistration() NodeRegistration {
	reg := validSyncNodeRegistration()
	reg.Metadata.Type = "agent"
	reg.Metadata.ExecutionKind = domain.NodeExecutionManagedAgent
	reg.Binding = ManagedAgentBinding{RuntimeKey: "builtin"}
	return reg
}

func TestNodeRegistry_Register_ValidRegistrationSucceeds(t *testing.T) {
	r := NewNodeRegistry()
	if err := r.Register(validSyncNodeRegistration()); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
	if err := r.Register(validAsyncNodeRegistration()); err != nil {
		t.Fatalf("Register() async error = %v, want nil", err)
	}
	if err := r.Register(validManagedAgentRegistration()); err != nil {
		t.Fatalf("Register() managed agent error = %v, want nil", err)
	}
}

func TestNodeRegistry_Register_EmptyTypeRejected(t *testing.T) {
	reg := validSyncNodeRegistration()
	reg.Metadata.Type = ""
	if err := NewNodeRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want error for empty type")
	}
}

func TestNodeRegistry_Register_DuplicateTypeRejected(t *testing.T) {
	r := NewNodeRegistry()
	if err := r.Register(validSyncNodeRegistration()); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}
	if err := r.Register(validSyncNodeRegistration()); err == nil {
		t.Fatal("second Register() error = nil, want duplicate type error")
	}
}

func TestNodeRegistry_Register_MissingBindingRejected(t *testing.T) {
	reg := validSyncNodeRegistration()
	reg.Binding = nil
	if err := NewNodeRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want error for missing binding")
	}
}

func TestNodeRegistry_Register_SyncWithManagedAgentBindingRejected(t *testing.T) {
	reg := validSyncNodeRegistration()
	reg.Binding = ManagedAgentBinding{RuntimeKey: "builtin"}
	if err := NewNodeRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want binding/executionKind mismatch error")
	}
}

func TestNodeRegistry_Register_ManagedAgentWithExecutorBindingRejected(t *testing.T) {
	reg := validManagedAgentRegistration()
	reg.Binding = ExecutorBinding{Executor: &stubNodeExecutor{}}
	if err := NewNodeRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want binding/executionKind mismatch error")
	}
}

func TestNodeRegistry_Register_AsyncWithoutAsyncExecutorRejected(t *testing.T) {
	reg := validAsyncNodeRegistration()
	reg.Binding = ExecutorBinding{Executor: &stubNodeExecutor{}} // sync-only executor
	if err := NewNodeRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want ASYNC executor requirement error")
	}
}

func TestNodeRegistry_Register_ExecutorBindingWithNilExecutorRejected(t *testing.T) {
	reg := validSyncNodeRegistration()
	reg.Binding = ExecutorBinding{Executor: nil}
	if err := NewNodeRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want missing executor error")
	}
}

func TestNodeRegistry_Register_UnparsableConfigSchemaRejected(t *testing.T) {
	reg := validSyncNodeRegistration()
	reg.Metadata.ConfigSchema = json.RawMessage(`{"type": 123}`)
	if err := NewNodeRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want configSchema compile error")
	}
}

func TestNodeRegistry_Register_UIFieldPathDoesNotResolveRejected(t *testing.T) {
	reg := validSyncNodeRegistration()
	reg.Metadata.UISchema.Fields = append(reg.Metadata.UISchema.Fields, domain.UIField{
		Path: "missing", Order: 30, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault,
	})
	if err := NewNodeRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want unresolved UI path error")
	}
}

func TestNodeRegistry_Register_DuplicateUIFieldPathRejected(t *testing.T) {
	reg := validSyncNodeRegistration()
	reg.Metadata.UISchema.Fields = append(reg.Metadata.UISchema.Fields, domain.UIField{
		Path: "inputKey", Order: 30, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault,
	})
	if err := NewNodeRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want duplicate UI path error")
	}
}

func TestNodeRegistry_Register_SelectWidgetWithoutEnumRejected(t *testing.T) {
	reg := validSyncNodeRegistration()
	reg.Metadata.UISchema.Fields = []domain.UIField{
		{Path: "label", Order: 10, Group: domain.UIGroupBasic, Widget: domain.UIWidgetSelect},
	}
	if err := NewNodeRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want SELECT-without-enum error")
	}
}

func TestNodeRegistry_Register_ModelSelectorWithoutStringPropertyRejected(t *testing.T) {
	reg := validSyncNodeRegistration()
	reg.Metadata.UISchema.Fields = []domain.UIField{
		{Path: "required", Order: 10, Group: domain.UIGroupModel, Widget: domain.UIWidgetModelSelector, Capability: domain.ModelCapabilityTextGeneration},
	}
	if err := NewNodeRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want MODEL_SELECTOR non-string-property error")
	}
}

func TestNodeRegistry_Register_ModelSelectorWithoutCapabilityRejected(t *testing.T) {
	reg := validSyncNodeRegistration()
	reg.Metadata.UISchema.Fields = []domain.UIField{
		{Path: "inputKey", Order: 10, Group: domain.UIGroupModel, Widget: domain.UIWidgetModelSelector},
	}
	if err := NewNodeRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want MODEL_SELECTOR missing-capability error")
	}
}

func TestNodeRegistry_Register_TextAreaWithoutStringPropertyRejected(t *testing.T) {
	reg := validSyncNodeRegistration()
	reg.Metadata.UISchema.Fields = []domain.UIField{
		{Path: "required", Order: 10, Group: domain.UIGroupBasic, Widget: domain.UIWidgetTextArea},
	}
	if err := NewNodeRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want TEXTAREA non-string-property error")
	}
}

func TestNodeRegistry_Register_PromptEditorWithoutStringPropertyRejected(t *testing.T) {
	reg := validSyncNodeRegistration()
	reg.Metadata.UISchema.Fields = []domain.UIField{
		{Path: "required", Order: 10, Group: domain.UIGroupBasic, Widget: domain.UIWidgetPromptEditor},
	}
	if err := NewNodeRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want PROMPT_EDITOR non-string-property error")
	}
}

func TestNodeRegistry_Get_UnknownTypeReturnsFalse(t *testing.T) {
	r := NewNodeRegistry()
	if _, ok := r.Get("does_not_exist"); ok {
		t.Fatal("Get() ok = true, want false for unregistered type")
	}
}

func TestNodeRegistry_Metadata_ReturnsRegisteredMetadata(t *testing.T) {
	r := NewNodeRegistry()
	reg := validSyncNodeRegistration()
	if err := r.Register(reg); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	got, ok := r.NodeMetadata(reg.Metadata.Type)
	if !ok {
		t.Fatal("NodeMetadata() ok = false, want true")
	}
	if got.Type != reg.Metadata.Type {
		t.Fatalf("NodeMetadata().Type = %q, want %q", got.Type, reg.Metadata.Type)
	}
}

func TestNodeRegistry_ListMetadata_SortedByType(t *testing.T) {
	r := NewNodeRegistry()
	if err := r.Register(validAsyncNodeRegistration()); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := r.Register(validSyncNodeRegistration()); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	list := r.ListMetadata()
	if len(list) != 2 {
		t.Fatalf("len(ListMetadata()) = %d, want 2", len(list))
	}
	if list[0].Type != "image_generation" || list[1].Type != "text_input" {
		t.Fatalf("ListMetadata() = %v, want sorted by type", list)
	}
}

func TestNodeRegistry_ValidateSemantics_DispatchesToExecutor(t *testing.T) {
	r := NewNodeRegistry()
	wantErr := errors.New("semantic failure")
	reg := validSyncNodeRegistration()
	reg.Binding = ExecutorBinding{Executor: &stubNodeExecutor{validateErr: wantErr}}
	if err := r.Register(reg); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := r.ValidateSemantics(context.Background(), reg.Metadata.Type, map[string]any{}); !errors.Is(err, wantErr) {
		t.Fatalf("ValidateSemantics() error = %v, want %v", err, wantErr)
	}
}

func TestNodeRegistry_ValidateSemantics_ManagedAgentReturnsNil(t *testing.T) {
	r := NewNodeRegistry()
	reg := validManagedAgentRegistration()
	if err := r.Register(reg); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := r.ValidateSemantics(context.Background(), reg.Metadata.Type, map[string]any{}); err != nil {
		t.Fatalf("ValidateSemantics() error = %v, want nil for MANAGED_AGENT in M1", err)
	}
}

func TestNodeRegistry_ValidateSemantics_UnknownTypeReturnsError(t *testing.T) {
	r := NewNodeRegistry()
	if err := r.ValidateSemantics(context.Background(), "does_not_exist", map[string]any{}); err == nil {
		t.Fatal("ValidateSemantics() error = nil, want error for unregistered type")
	}
}

// A Provider-reported failure must survive the wrapping an intermediate layer adds, so the
// resume use case can still tell "the external task failed" from "the payload did not
// parse".
func TestProviderFailure_ErrorsAs_IdentifiesProviderReportedFailure(t *testing.T) {
	err := fmt.Errorf("on_callback image_generation: %w", &ProviderFailure{
		Err: domain.ExecutionError{Code: "PROVIDER_TASK_FAILED", Message: "render rejected"},
	})

	var failure *ProviderFailure
	if !errors.As(err, &failure) {
		t.Fatalf("errors.As(%v) = false, want a wrapped *ProviderFailure", err)
	}
	if failure.Err.Code != "PROVIDER_TASK_FAILED" {
		t.Fatalf("failure.Err.Code = %q, want %q", failure.Err.Code, "PROVIDER_TASK_FAILED")
	}
	if failure.Err.Message != "render rejected" {
		t.Fatalf("failure.Err.Message = %q, want %q", failure.Err.Message, "render rejected")
	}
	if !strings.Contains(err.Error(), "render rejected") {
		t.Fatalf("err.Error() = %q, want it to carry the Provider message", err.Error())
	}
}

// A plain OnCallback error is a parse or transport failure: it leaves the NodeRun
// WAITING_CALLBACK, so it must never be mistaken for a Provider-reported failure.
func TestProviderFailure_PlainError_IsNotProviderFailure(t *testing.T) {
	err := fmt.Errorf("on_callback image_generation: %w", errors.New("payload is not valid JSON"))

	var failure *ProviderFailure
	if errors.As(err, &failure) {
		t.Fatalf("errors.As(%v) = true, want false for a parse error", err)
	}
}

// The idempotency key of an EXTERNAL + KEYED node is supplied by the Execution Service.
// Every other node leaves it empty, and an Adapter must never invent one.
func TestNodeInput_IdempotencyKey_ZeroValueIsEmpty(t *testing.T) {
	var in NodeInput
	if in.IdempotencyKey != "" {
		t.Fatalf("NodeInput{}.IdempotencyKey = %q, want empty", in.IdempotencyKey)
	}
}
