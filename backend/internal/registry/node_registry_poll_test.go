package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// stubPollableAsyncNodeExecutor additionally implements PollableAsyncNodeExecutor.
type stubPollableAsyncNodeExecutor struct {
	stubAsyncNodeExecutor
}

func (s *stubPollableAsyncNodeExecutor) Poll(ctx context.Context, state NodeAsyncState) (PollResult, error) {
	return PollResult{}, nil
}

func TestNodeRegistry_PollDeclaredWithPollableExecutor_Registers(t *testing.T) {
	reg := validAsyncNodeRegistration()
	reg.Metadata.Poll = &domain.PollPolicy{IntervalMs: 1000, MaxPolls: 5}
	reg.Binding = ExecutorBinding{Executor: &stubPollableAsyncNodeExecutor{}}

	r := NewNodeRegistry()
	if err := r.Register(reg); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
	got, ok := r.Get(reg.Metadata.Type)
	if !ok {
		t.Fatalf("Get(%q) not found", reg.Metadata.Type)
	}
	if got.Metadata.Poll == nil || *got.Metadata.Poll != *reg.Metadata.Poll {
		t.Fatalf("resolved Poll = %+v, want %+v", got.Metadata.Poll, reg.Metadata.Poll)
	}
}

func TestNodeRegistry_PollDeclaredWithoutPollableExecutor_RegistrationFails(t *testing.T) {
	reg := validAsyncNodeRegistration()
	reg.Metadata.Poll = &domain.PollPolicy{IntervalMs: 1000, MaxPolls: 5}

	err := NewNodeRegistry().Register(reg)
	const want = `node type "image_generation": poll declared but executor does not implement PollableAsyncNodeExecutor`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Register() error = %v, want it to contain %q", err, want)
	}
}

func TestNodeRegistry_PollableExecutorWithoutPollPolicy_RegistrationFails(t *testing.T) {
	reg := validAsyncNodeRegistration()
	reg.Binding = ExecutorBinding{Executor: &stubPollableAsyncNodeExecutor{}}

	err := NewNodeRegistry().Register(reg)
	const want = `node type "image_generation": executor implements PollableAsyncNodeExecutor but metadata declares no poll policy`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Register() error = %v, want it to contain %q", err, want)
	}
}
