package registry

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// stubToolExecutor is a minimal ToolExecutor used to test Registry rules in isolation.
type stubToolExecutor struct{}

func (stubToolExecutor) Execute(ctx context.Context, action ToolAction) (ToolExecutionResult, error) {
	return ToolExecutionResult{Kind: ToolResultCompleted, Result: &ToolResult{}}, nil
}

type stubAsyncToolExecutor struct {
	stubToolExecutor
}

func (stubAsyncToolExecutor) OnCallback(ctx context.Context, state ToolAsyncState, payload []byte) (ToolResult, error) {
	return ToolResult{}, nil
}

func validSyncToolRegistration() ToolRegistration {
	return ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          "lookup",
			Description:   "Read a deterministic record",
			InputSchema:   json.RawMessage(`{"type":"object"}`),
			OutputSchema:  json.RawMessage(`{"type":"object"}`),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			ExecutionKind: domain.ToolExecutionSync,
		},
		Executor: stubToolExecutor{},
	}
}

func validAsyncToolRegistration() ToolRegistration {
	reg := validSyncToolRegistration()
	reg.Metadata.Name = "delayed_task"
	reg.Metadata.ExecutionKind = domain.ToolExecutionAsync
	reg.Metadata.SideEffect = domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed}
	reg.Executor = stubAsyncToolExecutor{}
	return reg
}

func TestToolRegistry_Register_ValidRegistrationSucceeds(t *testing.T) {
	r := NewToolRegistry()
	if err := r.Register(validSyncToolRegistration()); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
	if err := r.Register(validAsyncToolRegistration()); err != nil {
		t.Fatalf("Register() async error = %v, want nil", err)
	}
}

func TestToolRegistry_Register_EmptyNameRejected(t *testing.T) {
	reg := validSyncToolRegistration()
	reg.Metadata.Name = ""
	if err := NewToolRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want error for empty name")
	}
}

func TestToolRegistry_Register_DuplicateNameRejected(t *testing.T) {
	r := NewToolRegistry()
	if err := r.Register(validSyncToolRegistration()); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}
	if err := r.Register(validSyncToolRegistration()); err == nil {
		t.Fatal("second Register() error = nil, want duplicate name error")
	}
}

func TestToolRegistry_Register_UnparsableInputSchemaRejected(t *testing.T) {
	reg := validSyncToolRegistration()
	reg.Metadata.InputSchema = json.RawMessage(`{"type": 123}`)
	if err := NewToolRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want inputSchema compile error")
	}
}

func TestToolRegistry_Register_UnparsableOutputSchemaRejected(t *testing.T) {
	reg := validSyncToolRegistration()
	reg.Metadata.OutputSchema = json.RawMessage(`{"type": 123}`)
	if err := NewToolRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want outputSchema compile error")
	}
}

func TestToolRegistry_Register_MissingExecutorRejected(t *testing.T) {
	reg := validSyncToolRegistration()
	reg.Executor = nil
	if err := NewToolRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want missing executor error")
	}
}

func TestToolRegistry_Register_AsyncWithoutAsyncExecutorRejected(t *testing.T) {
	reg := validAsyncToolRegistration()
	reg.Executor = stubToolExecutor{} // sync-only executor
	if err := NewToolRegistry().Register(reg); err == nil {
		t.Fatal("Register() error = nil, want ASYNC executor requirement error")
	}
}

func TestToolRegistry_Get_UnknownNameReturnsFalse(t *testing.T) {
	if _, ok := NewToolRegistry().Get("does-not-exist"); ok {
		t.Fatal("Get() ok = true, want false")
	}
}

func TestToolRegistry_ListMetadata_SortedByNameAndHidesExecutor(t *testing.T) {
	r := NewToolRegistry()
	if err := r.Register(validAsyncToolRegistration()); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := r.Register(validSyncToolRegistration()); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	list := r.ListMetadata()
	if len(list) != 2 || list[0].Name != "delayed_task" || list[1].Name != "lookup" {
		t.Fatalf("ListMetadata() = %v, want sorted by name", list)
	}
}
