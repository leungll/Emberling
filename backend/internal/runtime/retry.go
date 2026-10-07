package runtime

import (
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// MVP retry backoff constants. The retry and SideEffectPolicy contracts name FIXED and
// EXPONENTIAL backoff but do not specify
// numeric base/cap values; these are an MVP default choice, reported as a design decision
// rather than a documented requirement.
const (
	defaultBackoffBase = time.Second
	maxBackoffDuration = 60 * time.Second
)

// RetryInput is everything DecideRetry needs to decide whether a just-failed Attempt may
// produce another one.
type RetryInput struct {
	Policy     domain.ExecutionPolicy
	SideEffect domain.SideEffectPolicy
	// AttemptNo is the attempt number that just failed (1-indexed); the computed delay,
	// when Retry is true, is for attempt AttemptNo+1.
	AttemptNo int
	// HasIdempotencyKey reports whether a stable Provider idempotency key is already
	// bound to this NodeRun, required before an EXTERNAL+KEYED call may be retried.
	HasIdempotencyKey bool
	// ResultUncertain reports whether the failed Attempt's outcome at the Provider is
	// unknown (e.g. a timeout mid-dispatch), the case EXTERNAL+UNKNOWN must not retry.
	ResultUncertain bool
}

// RetryDecision is DecideRetry's pure answer.
type RetryDecision struct {
	Retry  bool
	Reason string
	// NextDelay is a duration, not an absolute time: DecideRetry never calls a Clock
	// itself. The caller, who owns the Clock, adds NextDelay to "now" to get the
	// persisted NextAttemptAt.
	NextDelay time.Duration
}

// DecideRetry applies ExecutionPolicy and SideEffectPolicy to decide whether a failed
// Attempt may be retried.
// Emberling does not promise a universal at-least-once/exactly-once guarantee for
// external calls; DecideRetry is the one place that boundary is enforced.
func DecideRetry(in RetryInput) RetryDecision {
	if in.AttemptNo >= in.Policy.MaxAttempts {
		return RetryDecision{Retry: false, Reason: "max attempts exhausted"}
	}

	switch in.SideEffect.Kind {
	case domain.SideEffectExternal:
		switch in.SideEffect.Idempotency {
		case domain.IdempotencyKeyed:
			if !in.HasIdempotencyKey {
				return RetryDecision{Retry: false, Reason: "external call requires an idempotency key but none is bound"}
			}
		case domain.IdempotencyUnknown:
			if in.ResultUncertain {
				return RetryDecision{Retry: false, Reason: "external call result is uncertain and idempotency is unknown"}
			}
		case domain.IdempotencySafe:
			// Declared safe to repeat: no idempotency precondition applies.
		}
	case domain.SideEffectNone:
		// No external effect can be duplicated, so idempotency imposes no precondition.
	}

	return RetryDecision{Retry: true, NextDelay: backoffDelay(in.Policy.Backoff, in.AttemptNo)}
}

// backoffDelay computes the delay before the next attempt: base for FIXED, base *
// 2^(attemptNo-1) for EXPONENTIAL, both capped at maxBackoffDuration.
func backoffDelay(kind domain.BackoffKind, attemptNo int) time.Duration {
	d := defaultBackoffBase
	if kind == domain.BackoffExponential {
		for i := 1; i < attemptNo; i++ {
			d *= 2
			if d >= maxBackoffDuration {
				d = maxBackoffDuration
				break
			}
		}
	}
	if d > maxBackoffDuration {
		d = maxBackoffDuration
	}
	return d
}

// AttemptDeadline returns the absolute deadline for one Attempt given now (from the
// caller's Clock) and the node's configured timeout. A non-positive TimeoutMs means no
// deadline, reported as the zero time.Time.
func AttemptDeadline(now time.Time, policy domain.ExecutionPolicy) time.Time {
	if policy.TimeoutMs <= 0 {
		return time.Time{}
	}
	return now.Add(time.Duration(policy.TimeoutMs) * time.Millisecond)
}
