package service

import (
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
)

func TestPostReconcileBackoff_QueriesSoFar_DoublesFromPollIntervalUpToCap(t *testing.T) {
	cases := []struct {
		name    string
		base    time.Duration
		queries int
		want    time.Duration
	}{
		{"first query waits the poll interval", 5 * time.Second, 0, 5 * time.Second},
		{"each query doubles", 5 * time.Second, 3, 40 * time.Second},
		{"missing interval falls back", 0, 1, 2 * postReconcileFallbackInterval},
		{"long schedule stops at the cap", 5 * time.Second, 10, postReconcileMaxInterval},
		{"huge query count stays bounded", time.Second, 1 << 30, postReconcileMaxInterval},
	}
	for _, tc := range cases {
		if got := postReconcileBackoff(tc.base, tc.queries); got != tc.want {
			t.Errorf("%s: backoff = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestReleaseUnknownSince_AttemptFacts_OnlyProductionNotKnowingStartsTheGrace(t *testing.T) {
	unknown := time.Date(2026, 11, 2, 12, 1, 0, 0, time.UTC)
	completed := unknown.Add(time.Minute)
	yes, no := true, false
	cases := []struct {
		name    string
		attempt domain.ToolAttempt
		want    *time.Time
	}{
		{"production lost the operation", domain.ToolAttempt{Status: domain.ToolAttemptFailed, OutcomeUnknownSince: &unknown, Uncertain: &yes, CompletedAt: &completed}, &unknown},
		{"stopped while production still had it", domain.ToolAttempt{Status: domain.ToolAttemptFailed, Uncertain: &yes, CompletedAt: &completed}, nil},
		{"definite refusal", domain.ToolAttempt{Status: domain.ToolAttemptFailed, Uncertain: &no, CompletedAt: &completed}, nil},
		{"still dispatched and known", domain.ToolAttempt{Status: domain.ToolAttemptDispatched}, nil},
	}
	for _, tc := range cases {
		got := releaseUnknownSince(tc.attempt)
		if (got == nil) != (tc.want == nil) || (got != nil && !got.Equal(*tc.want)) {
			t.Errorf("%s: unknown since = %v, want %v", tc.name, got, tc.want)
		}
	}
}
