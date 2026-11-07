package service

import (
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
)

func TestNodeOrphanError_ClassifiesByRegisteredPolicy(t *testing.T) {
	cases := []struct {
		name          string
		side          domain.SideEffectPolicy
		registered    bool
		wantCode      string
		wantUncertain bool
	}{
		{"no side effect", domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe}, true, "EXECUTION_INTERRUPTED", false},
		{"external and safe", domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencySafe}, true, "EXECUTION_INTERRUPTED", false},
		{"external and unknown", domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown}, true, "RESULT_UNCERTAIN", true},
		{"external and keyed", domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed}, true, "RESULT_UNCERTAIN", true},
		{"no registration left", domain.SideEffectPolicy{}, false, "RESULT_UNCERTAIN", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, uncertain := nodeOrphanError(tc.side, tc.registered)
			if got.Code != tc.wantCode || uncertain != tc.wantUncertain {
				t.Fatalf("nodeOrphanError = %s uncertain=%v, want %s uncertain=%v", got.Code, uncertain, tc.wantCode, tc.wantUncertain)
			}
		})
	}
}

func TestOrphanedNode_OnlyAStartedAttemptOfAnotherRecordedProcessWithNoDeadline(t *testing.T) {
	s := &ExecutionService{deps: Deps{RunnerID: "runner-current"}}
	other, own, empty := "runner-other", "runner-current", ""
	deadline := time.Unix(1_790_000_000, 0).UTC()
	cases := []struct {
		name    string
		attempt domain.NodeAttempt
		want    bool
	}{
		{"another process", domain.NodeAttempt{Status: domain.NodeAttemptStarted, RunnerID: &other}, true},
		{"this process", domain.NodeAttempt{Status: domain.NodeAttemptStarted, RunnerID: &own}, false},
		{"no recorded process", domain.NodeAttempt{Status: domain.NodeAttemptStarted}, false},
		{"empty recorded process", domain.NodeAttempt{Status: domain.NodeAttemptStarted, RunnerID: &empty}, false},
		{"bounded by a deadline", domain.NodeAttempt{Status: domain.NodeAttemptStarted, RunnerID: &other, DeadlineAt: &deadline}, false},
		{"already dispatched", domain.NodeAttempt{Status: domain.NodeAttemptDispatched, RunnerID: &other}, false},
		{"already resolved", domain.NodeAttempt{Status: domain.NodeAttemptFailed, RunnerID: &other}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.orphanedNode(tc.attempt); got != tc.want {
				t.Fatalf("orphanedNode = %v, want %v", got, tc.want)
			}
		})
	}
}
