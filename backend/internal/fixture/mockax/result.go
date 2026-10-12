package mockax

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// runningBody and workspaceInvalidBody are the supervisor's two fixed result forms.
const (
	runningBody          = `{"state":"running"}`
	workspaceInvalidBody = `{"state":"failed","reason":"WORKSPACE_INVALID"}`
)

// doneResult is the supervisor's finished result form, member for member.
type doneResult struct {
	State           string `json:"state"`
	ExitCode        int    `json:"exitCode"`
	WorkspaceDigest string `json:"workspaceDigest"`
	Output          string `json:"output"`
	OutputTruncated bool   `json:"outputTruncated"`
}

// ReadReady answers one readiness query for the named Task as the sandbox supervisor
// would through the AX router. A Task that does not exist or was never resumed answers
// 404, as the router does for an actor it cannot reach; the fake does not model the
// router waking a suspended actor.
func (f *Fake) ReadReady(ctx context.Context, atespace, taskName string) (int, error) {
	if err := f.admit(ctx, KindReady, callRef{Atespace: atespace, Name: taskName}); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	res, ok := f.resources[atespace+"/"+taskName]
	if !ok || !res.resumed {
		return http.StatusNotFound, nil
	}
	res.readyQueries++
	switch scenario := res.scenario; {
	case scenario.ResumeRefused || scenario.Startup == StartupFailed || scenario.Startup == StartupNeverReady:
		return http.StatusServiceUnavailable, nil
	case scenario.Startup == StartupReadyLater && res.readyQueries <= len(scenario.ReadyAnswers):
		status := scenario.ReadyAnswers[res.readyQueries-1]
		if status == 0 {
			return 0, errors.New("mockax: router connection reset")
		}
		if status == http.StatusOK {
			res.readyReported = true
		}
		return status, nil
	}
	res.readyReported = true
	return http.StatusOK, nil
}

// ReadResult answers one result query for the named Task as the sandbox supervisor would
// through the AX router: failures first, then "running" rounds, then the finished result.
// A Task that does not exist answers 404, as the router does for an unknown actor.
func (f *Fake) ReadResult(ctx context.Context, atespace, taskName string) (int, io.ReadCloser, error) {
	if err := f.admit(ctx, KindResult, callRef{Atespace: atespace, Name: taskName}); err != nil {
		return 0, nil, err
	}
	f.mu.Lock()
	res, ok := f.resources[atespace+"/"+taskName]
	if !ok {
		f.mu.Unlock()
		return http.StatusNotFound, io.NopCloser(bytes.NewReader(nil)), nil
	}
	res.queries++
	query, scenario, digest := res.queries, res.scenario, res.digest
	f.mu.Unlock()

	if query <= scenario.QueryFailures {
		if scenario.QueryFailureStatus == 0 {
			return 0, nil, errors.New("mockax: router connection reset")
		}
		return scenario.QueryFailureStatus, io.NopCloser(bytes.NewReader(nil)), nil
	}
	answered := query - scenario.QueryFailures
	switch {
	case scenario.WorkspaceInvalid:
		return ok200(workspaceInvalidBody)
	case scenario.RunningRounds == Forever || answered <= scenario.RunningRounds:
		return ok200(runningBody)
	case scenario.Body != nil:
		return http.StatusOK, io.NopCloser(bytes.NewReader(scenario.Body)), nil
	}
	if scenario.WorkspaceDigest != "" {
		digest = scenario.WorkspaceDigest
	}
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(doneResult{
		State: "done", ExitCode: scenario.ExitCode, WorkspaceDigest: digest,
		Output: scenario.Output, OutputTruncated: scenario.OutputTruncated,
	}); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, io.NopCloser(&body), nil
}

func ok200(body string) (int, io.ReadCloser, error) {
	return http.StatusOK, io.NopCloser(bytes.NewReader([]byte(body))), nil
}
