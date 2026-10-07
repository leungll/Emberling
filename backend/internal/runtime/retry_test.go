package runtime

import (
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// TestDecideRetry_ExternalUnknownUncertain_NoRetry asserts the EXTERNAL+UNKNOWN
// SideEffectPolicy row: when a Provider's side effect kind is EXTERNAL, its
// Idempotency is UNKNOWN and the failed Attempt's outcome is uncertain, DecideRetry must
// refuse to retry -- retrying could duplicate an external effect whose result is unknown.
func TestDecideRetry_ExternalUnknownUncertain_NoRetry(t *testing.T) {
	in := RetryInput{
		Policy:          domain.ExecutionPolicy{MaxAttempts: 5, Backoff: domain.BackoffFixed},
		SideEffect:      domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		AttemptNo:       1,
		ResultUncertain: true,
	}

	got := DecideRetry(in)
	if got.Retry {
		t.Errorf("DecideRetry() = %+v, want Retry=false", got)
	}
}

// TestDecideRetry_ExternalKeyedWithoutKey_NoRetry asserts the EXTERNAL+KEYED row: an
// idempotency-keyed external call may only be retried once a stable idempotency key is
// bound to the NodeRun.
func TestDecideRetry_ExternalKeyedWithoutKey_NoRetry(t *testing.T) {
	in := RetryInput{
		Policy:            domain.ExecutionPolicy{MaxAttempts: 5, Backoff: domain.BackoffFixed},
		SideEffect:        domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed},
		AttemptNo:         1,
		HasIdempotencyKey: false,
	}

	got := DecideRetry(in)
	if got.Retry {
		t.Errorf("DecideRetry() = %+v, want Retry=false", got)
	}
}

// TestDecideRetry_ExternalKeyedWithKey_Retries is the positive control: once an
// idempotency key is bound, an EXTERNAL+KEYED attempt may retry.
func TestDecideRetry_ExternalKeyedWithKey_Retries(t *testing.T) {
	in := RetryInput{
		Policy:            domain.ExecutionPolicy{MaxAttempts: 5, Backoff: domain.BackoffFixed},
		SideEffect:        domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed},
		AttemptNo:         1,
		HasIdempotencyKey: true,
	}

	got := DecideRetry(in)
	if !got.Retry {
		t.Errorf("DecideRetry() = %+v, want Retry=true", got)
	}
}

// TestDecideRetry_NoneSafe_AlwaysRetryable asserts the NONE+SAFE row: a side-effect-free,
// safe-to-repeat Node retries without any idempotency precondition.
func TestDecideRetry_NoneSafe_AlwaysRetryable(t *testing.T) {
	in := RetryInput{
		Policy:     domain.ExecutionPolicy{MaxAttempts: 3, Backoff: domain.BackoffFixed},
		SideEffect: domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		AttemptNo:  1,
	}

	got := DecideRetry(in)
	if !got.Retry {
		t.Errorf("DecideRetry() = %+v, want Retry=true", got)
	}
}

// TestDecideRetry_MaxAttemptsExhausted_NoRetry asserts DecideRetry refuses once
// AttemptNo has reached the policy's MaxAttempts, regardless of SideEffectPolicy.
func TestDecideRetry_MaxAttemptsExhausted_NoRetry(t *testing.T) {
	in := RetryInput{
		Policy:     domain.ExecutionPolicy{MaxAttempts: 3, Backoff: domain.BackoffFixed},
		SideEffect: domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		AttemptNo:  3,
	}

	got := DecideRetry(in)
	if got.Retry {
		t.Errorf("DecideRetry() = %+v, want Retry=false once AttemptNo reaches MaxAttempts", got)
	}
}

// TestDecideRetry_ZeroValuePolicy_NoRetry asserts a totally zero-value RetryInput (an
// ExecutionPolicy that was never populated, MaxAttempts: 0) fails safe: AttemptNo(0) >=
// MaxAttempts(0) trips the exhausted-attempts guard before any SideEffectPolicy check
// runs, so DecideRetry refuses to retry and reports a clear, non-empty Reason rather than
// panicking or defaulting to Retry=true.
func TestDecideRetry_ZeroValuePolicy_NoRetry(t *testing.T) {
	got := DecideRetry(RetryInput{})

	if got.Retry {
		t.Errorf("DecideRetry(RetryInput{}) = %+v, want Retry=false", got)
	}
	if got.Reason == "" {
		t.Error("DecideRetry(RetryInput{}) Reason is empty, want a clear reason")
	}
}

// TestDecideRetry_ExponentialBackoff_Doubles asserts EXPONENTIAL backoff doubles the
// delay per attempt (base * 2^(AttemptNo-1)) and FIXED does not grow.
func TestDecideRetry_ExponentialBackoff_Doubles(t *testing.T) {
	base := domain.ExecutionPolicy{MaxAttempts: 10, Backoff: domain.BackoffExponential}

	d1 := DecideRetry(RetryInput{Policy: base, SideEffect: domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe}, AttemptNo: 1}).NextDelay
	d2 := DecideRetry(RetryInput{Policy: base, SideEffect: domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe}, AttemptNo: 2}).NextDelay
	d3 := DecideRetry(RetryInput{Policy: base, SideEffect: domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe}, AttemptNo: 3}).NextDelay

	if d2 != 2*d1 {
		t.Errorf("delay at attempt 2 = %v, want 2x attempt 1 delay (%v)", d2, 2*d1)
	}
	if d3 != 4*d1 {
		t.Errorf("delay at attempt 3 = %v, want 4x attempt 1 delay (%v)", d3, 4*d1)
	}

	fixed := domain.ExecutionPolicy{MaxAttempts: 10, Backoff: domain.BackoffFixed}
	f1 := DecideRetry(RetryInput{Policy: fixed, SideEffect: domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe}, AttemptNo: 1}).NextDelay
	f3 := DecideRetry(RetryInput{Policy: fixed, SideEffect: domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe}, AttemptNo: 3}).NextDelay
	if f1 != f3 {
		t.Errorf("FIXED backoff delay changed across attempts: attempt1=%v attempt3=%v", f1, f3)
	}
}

// TestDecideRetry_ExponentialBackoff_CapsAtMaximum asserts exponential growth is capped,
// not unbounded, once it would exceed the MVP maxBackoffDuration constant.
func TestDecideRetry_ExponentialBackoff_CapsAtMaximum(t *testing.T) {
	policy := domain.ExecutionPolicy{MaxAttempts: 20, Backoff: domain.BackoffExponential}
	got := DecideRetry(RetryInput{
		Policy:     policy,
		SideEffect: domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		AttemptNo:  10,
	})
	if got.NextDelay > maxBackoffDuration {
		t.Errorf("NextDelay = %v, want capped at %v", got.NextDelay, maxBackoffDuration)
	}
}

// TestAttemptDeadline_PositiveTimeout_AddsDuration asserts AttemptDeadline adds the
// policy's configured timeout to the given now, and never calls a Clock itself.
func TestAttemptDeadline_PositiveTimeout_AddsDuration(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := domain.ExecutionPolicy{TimeoutMs: 5000}

	got := AttemptDeadline(now, policy)
	want := now.Add(5 * time.Second)
	if !got.Equal(want) {
		t.Errorf("AttemptDeadline() = %v, want %v", got, want)
	}
}

// TestAttemptDeadline_NonPositiveTimeout_IsZero asserts a non-positive TimeoutMs means no
// deadline, reported as the zero time.Time rather than now itself (which would look like
// an already-expired deadline).
func TestAttemptDeadline_NonPositiveTimeout_IsZero(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := domain.ExecutionPolicy{TimeoutMs: 0}

	got := AttemptDeadline(now, policy)
	if !got.IsZero() {
		t.Errorf("AttemptDeadline() = %v, want zero time.Time", got)
	}
}
