package sandboxrunner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// callbackDestination is where and with which credential a test's callbacks go. The token
// lives here, in the redelivery store when test controls are on, and in the delivery
// header; nowhere else.
type callbackDestination struct {
	URL   string
	Token string
}

// summary is the test counts a SUCCEEDED payload reports.
type summary struct {
	Total  int `json:"total"`
	Failed int `json:"failed"`
}

// succeededPayload is the SUCCEEDED callback payload.
type succeededPayload struct {
	Status       string  `json:"status"`
	Passed       bool    `json:"passed"`
	BaseCommit   string  `json:"baseCommit"`
	PatchDigest  string  `json:"patchDigest"`
	ResultDigest string  `json:"resultDigest"`
	Summary      summary `json:"summary"`
}

// failedPayload is the FAILED callback payload.
type failedPayload struct {
	Status string        `json:"status"`
	Error  failureDetail `json:"error"`
}

type failureDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// canonicalResult is the summary resultDigest is computed over. Its members are in
// alphabetical order and json.Marshal emits no insignificant whitespace, so the same
// result always encodes to the same bytes.
type canonicalResult struct {
	BaseCommit  string  `json:"baseCommit"`
	Passed      bool    `json:"passed"`
	PatchDigest string  `json:"patchDigest"`
	Summary     summary `json:"summary"`
}

// ResultDigest returns "sha256:" and the hex SHA-256 of the canonical result summary.
func ResultDigest(baseCommit, patchDigest string, passed bool, total, failed int) string {
	encoded, err := json.Marshal(canonicalResult{
		BaseCommit: baseCommit, Passed: passed, PatchDigest: patchDigest,
		Summary: summary{Total: total, Failed: failed},
	})
	if err != nil {
		// A struct of strings, a bool and ints always encodes.
		panic("sandboxrunner: encode canonical result: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// buildPayload converts a Backend outcome into the callback payload.
func buildPayload(job Job, outcome Outcome) (json.RawMessage, string) {
	var body any
	status := statusFailed
	if outcome.Verdict {
		status = statusSucceeded
		body = succeededPayload{
			Status:       statusSucceeded,
			Passed:       outcome.Passed,
			BaseCommit:   job.BaseCommit,
			PatchDigest:  job.PatchDigest,
			ResultDigest: ResultDigest(job.BaseCommit, job.PatchDigest, outcome.Passed, outcome.Total, outcome.Failed),
			Summary:      summary{Total: outcome.Total, Failed: outcome.Failed},
		}
	} else {
		body = failedPayload{Status: statusFailed, Error: failureDetail{Code: outcome.FailureCode, Message: outcome.FailureMessage}}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		panic("sandboxrunner: encode callback payload: " + err.Error())
	}
	return encoded, status
}

// plannedDeliveries is how many times a finished test's callback is delivered.
func plannedDeliveries(mock string) int {
	switch mock {
	case mockLost:
		return 0
	case mockDuplicate:
		return 2
	default:
		return 1
	}
}

// schedule starts job in a goroutine the Server owns. It returns false, starting nothing,
// once Shutdown has begun or maxOutstandingTests tests are already outstanding.
func (s *Server) schedule(job Job, destination callbackDestination) bool {
	s.mu.Lock()
	if s.stopped || s.outstanding >= maxOutstandingTests {
		s.mu.Unlock()
		return false
	}
	s.outstanding++
	s.wg.Add(1)
	s.mu.Unlock()
	s.setState(job.TestID, statusAccepted, nil)

	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			s.outstanding--
			s.mu.Unlock()
		}()
		s.runTest(job, destination)
	}()
	return true
}

// runTest runs one test, then delivers its callback the planned number of times. A
// shutdown that cancels the run delivers nothing: the test produced no result.
func (s *Server) runTest(job Job, destination callbackDestination) {
	select {
	case s.runs <- struct{}{}:
	case <-s.ctx.Done():
		return
	}
	s.setState(job.TestID, statusRunning, nil)
	s.recordStarted(job)
	outcome := s.backend.Run(s.ctx, job)
	<-s.runs
	if s.ctx.Err() != nil {
		return
	}

	payload, status := buildPayload(job, outcome)
	s.setState(job.TestID, status, payload)
	s.recordCompleted(job, status, outcome)

	deliveries := plannedDeliveries(job.Mock)
	if deliveries == 0 {
		s.recordLost(job.TestID)
		return
	}
	// The result is stored for redelivery before the first attempt, so a runner killed
	// mid-delivery still redelivers it after a restart.
	s.storeForRedelivery(redeliveryEntry{
		TestID: job.TestID, CallbackURL: destination.URL, CallbackToken: destination.Token,
		Payload: payload, Deliveries: deliveries,
	})
	for attempt := 1; attempt <= deliveries; attempt++ {
		s.deliver(s.ctx, job.TestID, destination, payload, attempt)
	}
}

// Shutdown stops accepting tests and waits for every test and callback goroutine. When
// ctx ends first it cancels the running tests, whose Backend stops them, and still waits
// for the goroutines to return, so none outlives the Server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		s.cancel()
		return nil
	case <-ctx.Done():
		s.cancel()
		<-done
		return ctx.Err()
	}
}
