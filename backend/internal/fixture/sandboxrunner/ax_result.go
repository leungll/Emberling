package sandboxrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

// maxResultBytes bounds one supervisor result body: the output, which the supervisor
// trims so its JSON encoding fits maxTestOutputBytes, plus a fixed allowance for the other
// members. A longer body is malformed, not truncated.
const maxResultBytes = maxTestOutputBytes + 4096

// Supervisor result states.
const (
	resultRunning = "running"
	resultDone    = "done"
	resultFailed  = "failed"
)

// reasonWorkspaceInvalid is the only reason a failed result may carry: the supervisor
// refused the Workspace and never started the test command.
const reasonWorkspaceInvalid = "WORKSPACE_INVALID"

var workspaceDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// supervisorResult is the result body as it arrives. Pointer members tell an absent
// member apart from a zero value.
type supervisorResult struct {
	State           string  `json:"state"`
	ExitCode        *int    `json:"exitCode"`
	WorkspaceDigest *string `json:"workspaceDigest"`
	Output          *string `json:"output"`
	OutputTruncated *bool   `json:"outputTruncated"`
	Reason          *string `json:"reason"`
}

// axResult is one validated answer of the result endpoint, or a malformed one.
type axResult struct {
	state     string
	malformed string
	exitCode  int
	digest    string
	output    string
}

// queryResult performs one result query, bounded by axResultQueryTimeout and the test
// deadline. transient is true for a transport failure, a non-200 answer or a body that
// could not be read, which the caller retries in the next round.
func (b *AXBackend) queryResult(ctx context.Context, name string, deadline time.Time) (axResult, bool) {
	limit := min(axResultQueryTimeout, max(deadline.Sub(b.cfg.Clock.Now()), 0))
	queryCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	status, body, err := b.cfg.Results.ReadResult(queryCtx, b.cfg.Atespace, name)
	if err != nil {
		return axResult{}, true
	}
	defer body.Close()
	if status != http.StatusOK {
		return axResult{}, true
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxResultBytes+1))
	if err != nil {
		return axResult{}, true
	}
	return parseSupervisorResult(raw), false
}

// parseSupervisorResult validates a result body against the supervisor's three forms:
// running with nothing else, failed with the workspace-invalid reason only, and done with
// exactly the exit code, the digest, the output and the truncation flag.
func parseSupervisorResult(raw []byte) axResult {
	if len(raw) > maxResultBytes {
		return axResult{malformed: "sandbox result exceeds its size limit"}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var body supervisorResult
	if err := decoder.Decode(&body); err != nil {
		return axResult{malformed: "sandbox result is not a valid result object"}
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return axResult{malformed: "sandbox result has data after the result object"}
	}
	doneMembers := body.ExitCode != nil || body.WorkspaceDigest != nil || body.Output != nil || body.OutputTruncated != nil
	switch body.State {
	case resultRunning:
		if doneMembers || body.Reason != nil {
			return axResult{malformed: "running sandbox result carries other members"}
		}
		return axResult{state: resultRunning}
	case resultFailed:
		if doneMembers || body.Reason == nil || *body.Reason != reasonWorkspaceInvalid {
			return axResult{malformed: "failed sandbox result is not a workspace refusal"}
		}
		return axResult{state: resultFailed}
	case resultDone:
		if body.Reason != nil || body.ExitCode == nil || body.WorkspaceDigest == nil || body.Output == nil ||
			body.OutputTruncated == nil || !workspaceDigestPattern.MatchString(*body.WorkspaceDigest) {
			return axResult{malformed: "finished sandbox result is incomplete"}
		}
		return axResult{state: resultDone, exitCode: *body.ExitCode, digest: *body.WorkspaceDigest, output: *body.Output}
	default:
		return axResult{malformed: "sandbox result has an unknown state"}
	}
}

// outcome maps a final result. A done result counts only when the supervisor wrote
// exactly the tree the runner built; then it follows the docker Backend: exit 0 or 1 is a
// verdict counted from the go test -json output, which is untrusted data, and any other
// exit code produced none. A truncated output is counted as far as it arrived.
func (r axResult) outcome(wantDigest string) Outcome {
	switch {
	case r.malformed != "":
		return sandboxError(r.malformed)
	case r.state == resultFailed:
		return sandboxError("sandbox supervisor refused the workspace")
	case r.digest != wantDigest:
		return sandboxError("sandbox wrote a different file tree than the runner built")
	}
	switch r.exitCode {
	case 0, 1:
		total, failed := countTests([]byte(r.output))
		return Outcome{Verdict: true, Passed: r.exitCode == 0 && failed == 0, Total: total, Failed: failed}
	default:
		return sandboxError("sandbox exited with status " + strconv.Itoa(r.exitCode))
	}
}
