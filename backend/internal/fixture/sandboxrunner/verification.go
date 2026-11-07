package sandboxrunner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
)

// A verification is the runner's second job kind: the full check of one workspace patch
// against a registered repository. The runner exports the base commit from that
// repository, applies the patch, and runs the base commit's whole test suite, a
// vulnerability scan against the pinned snapshot and a comparison of the complete
// dependency list with the snapshot's affected versions. The three checks are independent
// and all three must hold for the patch to pass; a passing test suite alone never does.
//
// The dispatch, status and callback protocol is the one tests use: POST accepts with an
// id, GET answers the status and the finished payload, and the callback carries the same
// payload once, with the token in its header. The result binds the patch digest and the
// base commit, and carries the bounded log of every step so a caller can keep it.

// verificationsPath is the dispatch route; a status query appends /{verificationId}.
const verificationsPath = "/v1/verifications"

// maxRepositoryIDBytes bounds the repository identifier of a request.
const maxRepositoryIDBytes = 128

// repositoryIDPattern is the shape a repository identifier must have, the same rule the
// Runtime applies to its registered repositories.
var repositoryIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// MaxVerificationLogBytes bounds the log a verification result carries. A longer log is
// cut and the cut is marked both in the text and in the payload, so a reader can tell a
// short run from a truncated one.
const MaxVerificationLogBytes = 512 << 10

// logTruncationMarker ends a truncated log. It names the bytes left out.
const logTruncationMarker = "\n[log truncated by the sandbox runner: %d bytes omitted]\n"

// maxSummaryEntries bounds the advisory and dependency lists a summary carries.
const maxSummaryEntries = 32

// Verification mock modes beside the shared pass, error, duplicate and lost modes.
const (
	mockTestsFail              = "tests-fail"
	mockScanVulnerable         = "scan-vulnerable"
	mockDependenciesVulnerable = "dependencies-vulnerable"
	mockLogOverflow            = "log-overflow"
)

// Failure codes a verification reports besides the shared codes of a test run.
const (
	codeUnknownRepository = "UNKNOWN_REPOSITORY"
	codeModuleUnavailable = "MODULE_UNAVAILABLE"
)

// verificationRequest is the POST /v1/verifications body. Unknown members are rejected,
// so a request can never select an image, a mount or a command.
type verificationRequest struct {
	CallbackURL   string `json:"callbackUrl"`
	CallbackToken string `json:"callbackToken"`
	RepositoryID  string `json:"repositoryId"`
	BaseCommit    string `json:"baseCommit"`
	Patch         string `json:"patch"`
	Mock          string `json:"mock,omitempty"`
}

type verificationAcceptedResponse struct {
	VerificationID string `json:"verificationId"`
}

type verificationStatusResponse struct {
	VerificationID string          `json:"verificationId"`
	Status         string          `json:"status"`
	Result         json.RawMessage `json:"result,omitempty"`
}

// VerificationJob is one accepted verification as a VerificationBackend sees it. It
// carries no callback credential. An empty Patch verifies the base commit itself.
type VerificationJob struct {
	VerificationID string
	RepositoryID   string
	BaseCommit     string
	Patch          string // LF-normalized
	PatchDigest    string
	Mock           string
}

// VerificationOutcome is what a VerificationBackend reports for one job. Verdict is true
// when every step ran far enough to decide the three checks; otherwise FailureCode and
// FailureMessage say why no verdict exists, such as a module the offline cache lacks.
type VerificationOutcome struct {
	Verdict           bool
	TestsPassed       bool
	ScanClean         bool
	DependenciesClean bool
	Tests             testCounts
	// ScanFindings are the advisory ids the scan reported, deduplicated.
	ScanFindings []string
	// ScanRan is false when the scanner could not run to a result, for example because
	// the patched module does not build; the scan then counts as not clean.
	ScanRan bool
	// Scanner names the scanner that produced ScanFindings.
	Scanner string
	// VulnerableDependencies lists the build-list modules at an affected version.
	VulnerableDependencies []string
	// SnapshotDigest identifies the vulnerability database snapshot used.
	SnapshotDigest string
	// Log is the combined, unbounded output of every step; the Server bounds it.
	Log            []byte
	FailureCode    string
	FailureMessage string
}

// Passed is the overall verdict: all three checks hold.
func (o VerificationOutcome) Passed() bool {
	return o.TestsPassed && o.ScanClean && o.DependenciesClean
}

// testCounts are the test totals the `go test -json` output yields.
type testCounts struct {
	Total  int `json:"total"`
	Failed int `json:"failed"`
}

// VerificationBackend runs one verification. Verify must honor ctx: the Server cancels it
// on shutdown.
type VerificationBackend interface {
	// ValidateMock reports whether this backend accepts the request's mock mode.
	ValidateMock(mock string) error
	Verify(ctx context.Context, job VerificationJob) VerificationOutcome
}

// WithVerification serves the verification routes on top of the test routes, running
// each verification on backend. Without it the routes do not exist.
func WithVerification(backend VerificationBackend) Option {
	return func(s *Server) { s.verifier = backend }
}

// verificationSummary is the bounded summary a SUCCEEDED verification payload carries.
type verificationSummary struct {
	Tests                  testCounts `json:"tests"`
	ScanRan                bool       `json:"scanRan"`
	Scanner                string     `json:"scanner"`
	ScanFindings           []string   `json:"scanFindings"`
	VulnerableDependencies []string   `json:"vulnerableDependencies"`
	SnapshotDigest         string     `json:"snapshotDigest"`
}

// verificationSucceededPayload is the SUCCEEDED callback payload of a verification.
type verificationSucceededPayload struct {
	Status            string              `json:"status"`
	Passed            bool                `json:"passed"`
	TestsPassed       bool                `json:"testsPassed"`
	ScanClean         bool                `json:"scanClean"`
	DependenciesClean bool                `json:"dependenciesClean"`
	BaseCommit        string              `json:"baseCommit"`
	PatchDigest       string              `json:"patchDigest"`
	ResultDigest      string              `json:"resultDigest"`
	Summary           verificationSummary `json:"summary"`
	Log               string              `json:"log"`
	LogTruncated      bool                `json:"logTruncated"`
}

// canonicalVerificationResult is what the verification resultDigest is computed over: the
// verdicts, the bindings and the summary, in alphabetical member order. The log is not
// part of it; the log is kept beside the result under its own content digest.
type canonicalVerificationResult struct {
	BaseCommit        string              `json:"baseCommit"`
	DependenciesClean bool                `json:"dependenciesClean"`
	Passed            bool                `json:"passed"`
	PatchDigest       string              `json:"patchDigest"`
	ScanClean         bool                `json:"scanClean"`
	Summary           verificationSummary `json:"summary"`
	TestsPassed       bool                `json:"testsPassed"`
}

// VerificationResultDigest returns "sha256:" and the hex SHA-256 of the canonical
// verification result.
func VerificationResultDigest(baseCommit, patchDigest string, outcome VerificationOutcome) string {
	encoded, err := json.Marshal(canonicalVerificationResult{
		BaseCommit: baseCommit, DependenciesClean: outcome.DependenciesClean, Passed: outcome.Passed(),
		PatchDigest: patchDigest, ScanClean: outcome.ScanClean, Summary: summarize(outcome), TestsPassed: outcome.TestsPassed,
	})
	if err != nil {
		// A struct of strings, bools, ints and string slices always encodes.
		panic("sandboxrunner: encode canonical verification result: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// WorkspacePatchDigest is the digest the Runtime identifies a workspace patch by: the
// lowercase hex SHA-256 of the patch text after every CRLF is folded to LF, with nothing
// else changed. The runner reports it so a verification result binds to the same value the
// Runtime recorded for the patch; the empty patch digests the empty byte string.
func WorkspacePatchDigest(patch string) string {
	sum := sha256.Sum256([]byte(strings.ReplaceAll(patch, "\r\n", "\n")))
	return hex.EncodeToString(sum[:])
}

// summarize builds the bounded summary of an outcome. Lists over maxSummaryEntries are cut;
// the digest is computed over the cut lists, so what is reported is what is signed.
func summarize(outcome VerificationOutcome) verificationSummary {
	return verificationSummary{
		Tests:                  outcome.Tests,
		ScanRan:                outcome.ScanRan,
		Scanner:                outcome.Scanner,
		ScanFindings:           boundedList(outcome.ScanFindings),
		VulnerableDependencies: boundedList(outcome.VulnerableDependencies),
		SnapshotDigest:         outcome.SnapshotDigest,
	}
}

func boundedList(items []string) []string {
	out := make([]string, 0, min(len(items), maxSummaryEntries))
	for _, item := range items {
		if len(out) == maxSummaryEntries {
			break
		}
		out = append(out, truncateSummary(item))
	}
	return out
}

// boundLog cuts log to MaxVerificationLogBytes on a UTF-8 boundary and appends the
// truncation marker, reporting whether it cut anything. Invalid UTF-8 is replaced so the
// log is always text.
func boundLog(log []byte) (string, bool) {
	text := strings.ToValidUTF8(string(log), "�")
	if len(text) <= MaxVerificationLogBytes {
		return text, false
	}
	cut := MaxVerificationLogBytes
	for cut > 0 && text[cut]&0xC0 == 0x80 {
		cut--
	}
	return text[:cut] + fmt.Sprintf(logTruncationMarker, len(text)-cut), true
}

// buildVerificationPayload converts a backend outcome into the callback payload.
func buildVerificationPayload(job VerificationJob, outcome VerificationOutcome) (json.RawMessage, string) {
	var body any
	status := statusFailed
	if outcome.Verdict {
		status = statusSucceeded
		log, truncated := boundLog(outcome.Log)
		body = verificationSucceededPayload{
			Status:            statusSucceeded,
			Passed:            outcome.Passed(),
			TestsPassed:       outcome.TestsPassed,
			ScanClean:         outcome.ScanClean,
			DependenciesClean: outcome.DependenciesClean,
			BaseCommit:        job.BaseCommit,
			PatchDigest:       job.PatchDigest,
			ResultDigest:      VerificationResultDigest(job.BaseCommit, job.PatchDigest, outcome),
			Summary:           summarize(outcome),
			Log:               log,
			LogTruncated:      truncated,
		}
	} else {
		body = failedPayload{Status: statusFailed, Error: failureDetail{Code: outcome.FailureCode, Message: outcome.FailureMessage}}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		panic("sandboxrunner: encode verification payload: " + err.Error())
	}
	return encoded, status
}

// handleCreateVerification validates one dispatch, records and possibly holds it, then
// accepts it and starts the verification in a goroutine the Server owns.
func (s *Server) handleCreateVerification(w http.ResponseWriter, r *http.Request) {
	arrival := verificationArrival{}
	var req verificationRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		s.rejectVerification(w, arrival, http.StatusBadRequest, "request body must be one JSON object with only callbackUrl, callbackToken, repositoryId, baseCommit, patch and mock")
		return
	}
	arrival.RepositoryID = truncateSummary(req.RepositoryID)
	arrival.BaseCommit = truncateSummary(req.BaseCommit)
	arrival.CallbackTarget = truncateSummary(callbackTarget(req.CallbackURL))
	arrival.Mock = truncateSummary(req.Mock)
	if message := validateVerificationRequest(req); message != "" {
		s.rejectVerification(w, arrival, http.StatusBadRequest, message)
		return
	}
	if err := s.verifier.ValidateMock(req.Mock); err != nil {
		s.rejectVerification(w, arrival, http.StatusBadRequest, err.Error())
		return
	}
	verificationID, err := s.newVerificationID()
	if err != nil {
		s.rejectVerification(w, arrival, http.StatusInternalServerError, "verificationId could not be generated")
		return
	}
	patch := strings.ReplaceAll(req.Patch, "\r\n", "\n")
	arrival.VerificationID = verificationID
	arrival.PatchDigest = WorkspacePatchDigest(req.Patch)

	if err := s.admit(r.Context(), kindVerification, arrival, verificationRef{VerificationID: verificationID}); err != nil {
		s.respondHeldError(w, verificationID, err)
		return
	}
	job := VerificationJob{
		VerificationID: verificationID, RepositoryID: req.RepositoryID, BaseCommit: req.BaseCommit,
		Patch: patch, PatchDigest: arrival.PatchDigest, Mock: req.Mock,
	}
	destination := callbackDestination{URL: req.CallbackURL, Token: req.CallbackToken}
	if !s.scheduleVerification(job, destination) {
		s.recordVerificationResponse(verificationID, http.StatusServiceUnavailable)
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "sandbox runner is not accepting verifications"})
		return
	}
	s.recordVerificationResponse(verificationID, http.StatusAccepted)
	writeJSON(w, http.StatusAccepted, verificationAcceptedResponse{VerificationID: verificationID})
}

// validateVerificationRequest returns a fixed message naming the first invalid member, or
// "". It never echoes a member's value. An empty patch is allowed: it verifies the base
// commit itself.
func validateVerificationRequest(req verificationRequest) string {
	switch {
	case strings.TrimSpace(req.CallbackURL) == "":
		return "callbackUrl is required"
	case len(req.CallbackURL) > maxCallbackBytes || !validCallbackURL(req.CallbackURL):
		return "callbackUrl must be an absolute http or https URL"
	case strings.TrimSpace(req.CallbackToken) == "":
		return "callbackToken is required"
	case len(req.CallbackToken) > maxCallbackBytes:
		return "callbackToken is too long"
	case req.RepositoryID == "":
		return "repositoryId is required"
	case len(req.RepositoryID) > maxRepositoryIDBytes || !repositoryIDPattern.MatchString(req.RepositoryID):
		return "repositoryId must be a lowercase identifier of letters, digits and hyphens"
	case strings.TrimSpace(req.BaseCommit) == "":
		return "baseCommit is required"
	case len(req.BaseCommit) > maxBaseCommitBytes:
		return "baseCommit is too long"
	case len(req.Patch) > maxPatchBytes:
		return "patch is too large"
	}
	return ""
}

// handleVerificationStatus answers GET /v1/verifications/{verificationId}. A test id is
// unknown here: the two job kinds have separate routes.
func (s *Server) handleVerificationStatus(w http.ResponseWriter, r *http.Request) {
	verificationID := chi.URLParam(r, "verificationId")
	s.mu.Lock()
	state, ok := s.tests[verificationID]
	var response verificationStatusResponse
	if ok && state.kind == kindVerification {
		response = verificationStatusResponse{VerificationID: verificationID, Status: state.status, Result: state.result}
	} else {
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		s.recordVerificationPoll(verificationID, http.StatusNotFound, "")
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "unknown verificationId"})
		return
	}
	s.recordVerificationPoll(verificationID, http.StatusOK, response.Status)
	writeJSON(w, http.StatusOK, response)
}

// scheduleVerification starts job in a goroutine the Server owns, under the same bounds
// as a test.
func (s *Server) scheduleVerification(job VerificationJob, destination callbackDestination) bool {
	s.mu.Lock()
	if s.stopped || s.outstanding >= maxOutstandingTests {
		s.mu.Unlock()
		return false
	}
	s.outstanding++
	s.wg.Add(1)
	s.mu.Unlock()
	s.setKindState(job.VerificationID, kindVerification, statusAccepted, nil)

	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			s.outstanding--
			s.mu.Unlock()
		}()
		s.runVerification(job, destination)
	}()
	return true
}

// runVerification runs one verification, then delivers its callback the planned number of
// times. A shutdown that cancels the run delivers nothing.
func (s *Server) runVerification(job VerificationJob, destination callbackDestination) {
	select {
	case s.runs <- struct{}{}:
	case <-s.ctx.Done():
		return
	}
	s.setKindState(job.VerificationID, kindVerification, statusRunning, nil)
	s.appendLine(eventStarted, kindVerification, verificationRef{VerificationID: job.VerificationID})
	outcome := s.verifier.Verify(s.ctx, job)
	<-s.runs
	if s.ctx.Err() != nil {
		return
	}

	payload, status := buildVerificationPayload(job, outcome)
	s.setKindState(job.VerificationID, kindVerification, status, payload)
	s.recordVerificationCompleted(job, status, outcome)

	deliveries := plannedDeliveries(job.Mock)
	if deliveries == 0 {
		s.appendLine(eventLost, kindCallback, verificationRef{VerificationID: job.VerificationID})
		return
	}
	s.storeForRedelivery(redeliveryEntry{
		TestID: job.VerificationID, Kind: kindVerification, CallbackURL: destination.URL, CallbackToken: destination.Token,
		Payload: payload, Deliveries: deliveries,
	})
	for attempt := 1; attempt <= deliveries; attempt++ {
		s.deliver(s.ctx, job.VerificationID, destination, payload, attempt)
	}
}

func (s *Server) newVerificationID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "verify_" + hex.EncodeToString(b[:]), nil
}
