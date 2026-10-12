package sandboxrunner

import (
	"context"
	"net/http"
	"time"
)

// startTask resumes the Task, which AX creates suspended, and waits until its supervisor
// is ready. It returns false with the outcome to report when the Task could not be
// started or did not become ready. The resume is never sent twice: like a create, it may
// have taken effect even when its outcome is unknown, and the caller's deletion removes
// whatever exists.
func (b *AXBackend) startTask(ctx context.Context, name string, deadline time.Time) (Outcome, bool) {
	readyDeadline := b.cfg.Clock.Now().Add(b.cfg.ReadyTimeout)
	resumeCtx, cancel := context.WithTimeout(ctx, max(earlier(deadline, readyDeadline).Sub(b.cfg.Clock.Now()), 0))
	resumed, err := b.cfg.Control.Resume(resumeCtx, b.cfg.Atespace, name)
	cancel()
	switch {
	case ctx.Err() != nil:
		return interrupted(), false
	case err != nil:
		if outcome, expired := b.readyExpired(deadline, readyDeadline); expired {
			return outcome, false
		}
		return sandboxError("AX task resume was not confirmed"), false
	case !resumed:
		return sandboxError("AX refused to resume the task"), false
	}
	return b.waitReady(ctx, name, deadline, readyDeadline)
}

// waitReady decides readiness after the resume. AX writes the Task's Ready condition only
// once, while resuming, so a Task that reports ready is ready at once; otherwise the
// supervisor's readiness endpoint is asked through the router once per interval until it
// answers 200. Each round first reads the Task state, and a Task that failed, was
// suspended or vanished after the resume is never queried: a query through the router
// would wake a suspended sandbox behind AX's back.
func (b *AXBackend) waitReady(ctx context.Context, name string, deadline, readyDeadline time.Time) (Outcome, bool) {
	failures := 0
	for {
		state, outcome, ok := b.readState(ctx, name, deadline)
		if !ok {
			return outcome, false
		}
		switch state {
		case AXTaskReady:
			return Outcome{}, true
		case AXTaskFailed:
			return sandboxError("AX task failed to start"), false
		case AXTaskSuspended, AXTaskMissing:
			return sandboxError("AX task was suspended or disappeared before it was ready"), false
		case AXTaskPending:
		default:
			return sandboxError("AX reported an unknown task state"), false
		}

		switch b.queryReady(ctx, name, earlier(deadline, readyDeadline)) {
		case readyAnswered:
			return Outcome{}, true
		case readyNotYet:
			failures = 0
		case readyQueryFailed:
			failures++
			if failures >= maxRouterQueryFailures {
				return sandboxError("sandbox readiness could not be read through the AX router"), false
			}
		}
		if b.cfg.Clock.Sleep(ctx, axPollInterval) != nil {
			return interrupted(), false
		}
		if outcome, expired := b.readyExpired(deadline, readyDeadline); expired {
			return outcome, false
		}
	}
}

// readyAnswer is what one readiness query found out.
type readyAnswer int

const (
	// readyAnswered is a 200: the supervisor wrote the tree and started the test command.
	readyAnswered readyAnswer = iota
	// readyNotYet is a 503, the supervisor's own answer while it is not ready. It is an
	// answer, not a failure, so a supervisor that never becomes ready, as one that
	// refused its Workspace, ends at the ready timeout.
	readyNotYet
	// readyQueryFailed is a transport failure, a timeout or any other status, such as the
	// router's 404 for an actor it cannot reach yet; it is retried in the next round.
	readyQueryFailed
)

// queryReady performs one readiness query, bounded by axRouterQueryTimeout and until.
func (b *AXBackend) queryReady(ctx context.Context, name string, until time.Time) readyAnswer {
	queryCtx, cancel := context.WithTimeout(ctx, min(axRouterQueryTimeout, max(until.Sub(b.cfg.Clock.Now()), 0)))
	defer cancel()
	status, err := b.cfg.Results.ReadReady(queryCtx, b.cfg.Atespace, name)
	switch {
	case err != nil:
		return readyQueryFailed
	case status == http.StatusOK:
		return readyAnswered
	case status == http.StatusServiceUnavailable:
		return readyNotYet
	default:
		return readyQueryFailed
	}
}

// readyExpired reports the outcome once the ready wait is over: whichever of the two
// deadlines passed first decides between a test timeout and a sandbox that never became
// ready.
func (b *AXBackend) readyExpired(deadline, readyDeadline time.Time) (Outcome, bool) {
	now := b.cfg.Clock.Now()
	switch {
	case !now.Before(deadline) && !readyDeadline.Before(deadline):
		return b.timeout(), true
	case !now.Before(readyDeadline):
		return sandboxError("AX task was not ready within " + b.cfg.ReadyTimeout.String()), true
	case !now.Before(deadline):
		return b.timeout(), true
	}
	return Outcome{}, false
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
