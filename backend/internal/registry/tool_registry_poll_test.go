package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// stubPollableAsyncToolExecutor additionally implements PollableAsyncToolExecutor.
type stubPollableAsyncToolExecutor struct {
	stubAsyncToolExecutor
}

func (stubPollableAsyncToolExecutor) Poll(ctx context.Context, state ToolAsyncState) (PollResult, error) {
	return PollResult{}, nil
}

func TestToolRegistry_PollDeclaredWithPollableExecutor_Registers(t *testing.T) {
	reg := validAsyncToolRegistration()
	reg.Metadata.Poll = &domain.PollPolicy{IntervalMs: 1000, MaxPolls: 5}
	reg.Executor = stubPollableAsyncToolExecutor{}

	r := NewToolRegistry()
	if err := r.Register(reg); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
	got, ok := r.Get(reg.Metadata.Name)
	if !ok {
		t.Fatalf("Get(%q) not found", reg.Metadata.Name)
	}
	if got.Metadata.Poll == nil || *got.Metadata.Poll != *reg.Metadata.Poll {
		t.Fatalf("resolved Poll = %+v, want %+v", got.Metadata.Poll, reg.Metadata.Poll)
	}
}

func TestToolRegistry_PollDeclaredWithoutPollableExecutor_RegistrationFails(t *testing.T) {
	reg := validAsyncToolRegistration()
	reg.Metadata.Poll = &domain.PollPolicy{IntervalMs: 1000, MaxPolls: 5}

	err := NewToolRegistry().Register(reg)
	const want = `tool "delayed_task": poll declared but executor does not implement PollableAsyncToolExecutor`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Register() error = %v, want it to contain %q", err, want)
	}
}

func TestToolRegistry_PollableExecutorWithoutPollPolicy_RegistrationFails(t *testing.T) {
	reg := validAsyncToolRegistration()
	reg.Executor = stubPollableAsyncToolExecutor{}

	err := NewToolRegistry().Register(reg)
	const want = `tool "delayed_task": executor implements PollableAsyncToolExecutor but metadata declares no poll policy`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Register() error = %v, want it to contain %q", err, want)
	}
}
