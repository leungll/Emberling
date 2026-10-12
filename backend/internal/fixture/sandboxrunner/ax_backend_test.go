package sandboxrunner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/fixture/mockax"
	"github.com/leungll/Emberling/backend/internal/fixture/mockcontrol"
)

const (
	axTestAtespace = "emberling-sandbox"
	axTestImage    = "registry.local/sandboxsupervisor@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	axTestID       = "test_0123456789abcdef"
	axTestTask     = "emberling-sbx-0123456789abcdef"
)

// stepClock is a virtual clock: Sleep returns at once and moves Now forward, so a test
// walks through any number of polling rounds and deadlines without real time passing.
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func newStepClock() *stepClock { return &stepClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return nil
}

func (c *stepClock) elapsed() time.Duration {
	return c.Now().Sub(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
}

// axHarness is an ax Backend wired to the AX stand-in, a virtual clock and a record.
type axHarness struct {
	backend    *AXBackend
	fake       *mockax.Fake
	clock      *stepClock
	recordPath string
}

type axHarnessOptions struct {
	fake         mockax.Config
	testTimeout  time.Duration
	readyTimeout time.Duration
}

func newAXHarness(t *testing.T, opts axHarnessOptions) *axHarness {
	t.Helper()
	h := &axHarness{clock: newStepClock(), recordPath: filepath.Join(t.TempDir(), "record.jsonl")}
	record, err := mockcontrol.OpenRecord(h.recordPath)
	if err != nil {
		t.Fatalf("open record: %v", err)
	}
	t.Cleanup(func() { _ = record.Close() })
	opts.fake.Digest = WorkspaceDigest
	if opts.fake.Record == nil {
		opts.fake.Record = record
	}
	h.fake = mockax.New(opts.fake)
	t.Cleanup(h.fake.StopHolding)
	if opts.testTimeout == 0 {
		opts.testTimeout = 2 * time.Minute
	}
	if opts.readyTimeout == 0 {
		opts.readyTimeout = 30 * time.Second
	}
	h.backend, err = NewAXBackend(AXBackendConfig{
		Atespace: axTestAtespace, Image: axTestImage,
		TestTimeout: opts.testTimeout, ReadyTimeout: opts.readyTimeout,
		Control: h.fake, Results: h.fake, Clock: h.clock, Record: record,
	})
	if err != nil {
		t.Fatalf("new ax backend: %v", err)
	}
	return h
}

// axJob is a job whose patch applies to the fixture.
func axJob(patch string) Job {
	return Job{TestID: axTestID, BaseCommit: FixtureBaseCommit, Patch: patch, PatchDigest: PatchDigest(patch)}
}

// digestOf is the digest of the fixture tree after patch.
func digestOf(t *testing.T, patch string) string {
	t.Helper()
	files, err := patchedFixture(t, patch)
	if err != nil {
		t.Fatalf("apply %q: %v", patch, err)
	}
	return WorkspaceDigest(files)
}

// recordLines returns the record lines of one event and kind, decoded.
func (h *axHarness) recordLines(t *testing.T, event, kind string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(h.recordPath)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	var out []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("decode record line %q: %v", scanner.Text(), err)
		}
		if line["event"] == event && line["kind"] == kind {
			out = append(out, line)
		}
	}
	return out
}

func (h *axHarness) resultQueries(t *testing.T) int {
	return len(h.recordLines(t, mockcontrol.EventArrived, mockax.KindResult))
}

func (h *axHarness) arrivals(t *testing.T, kind string) int {
	return len(h.recordLines(t, mockcontrol.EventArrived, kind))
}

// arrivalKinds returns the kind of every call that reached the stand-in, in order.
func (h *axHarness) arrivalKinds(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(h.recordPath)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	var kinds []string
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		var line struct {
			Event string `json:"event"`
			Kind  string `json:"kind"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("decode record line %q: %v", scanner.Text(), err)
		}
		if line.Event == mockcontrol.EventArrived {
			kinds = append(kinds, line.Kind)
		}
	}
	return kinds
}

// controlStandIn sends one barrier control request to the stand-in.
func (h *axHarness) controlStandIn(t *testing.T, action, body string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.fake.ControlHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/control/"+action, strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status = %d, body %s", action, rec.Code, rec.Body)
	}
}

// assertCleanedUp checks the run deleted its derived Task and left nothing behind.
func (h *axHarness) assertCleanedUp(t *testing.T) {
	t.Helper()
	if deleted := h.fake.Deleted(); !slices.Equal(deleted, []string{axTestTask}) {
		t.Fatalf("deleted = %v, want exactly %s", deleted, axTestTask)
	}
	if names, _ := h.fake.List(context.Background(), axTestAtespace, ""); len(names) != 0 {
		t.Fatalf("resources left = %v, want none", names)
	}
}

func assertFailure(t *testing.T, outcome Outcome, code string) {
	t.Helper()
	if outcome.Verdict || outcome.FailureCode != code {
		t.Fatalf("outcome = %+v, want no verdict and %s", outcome, code)
	}
}

func TestAXBackend_TaskName_DerivedFromTestIDAsRFC1123Label(t *testing.T) {
	name, err := axTaskName(axTestID)
	if err != nil || name != axTestTask {
		t.Fatalf("axTaskName(%q) = %q, %v, want %q", axTestID, name, err, axTestTask)
	}
	label := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	if len(name) != 30 || !label.MatchString(name) || !ValidAXName(name) {
		t.Fatalf("task name %q is not a 30-character lowercase RFC 1123 label", name)
	}
	for _, bad := range []string{"", "0123456789abcdef", "test_0123456789ABCDEF", "test_0123", "test_0123456789abcdef0", "test_0123456789abcdeg", "test-0123456789abcdef"} {
		if got, err := axTaskName(bad); err == nil {
			t.Fatalf("axTaskName(%q) = %q, want an error", bad, got)
		}
	}

	h := newAXHarness(t, axHarnessOptions{})
	job := axJob(touchPatch)
	job.TestID = "test_NOT-HEX"
	assertFailure(t, h.backend.Run(context.Background(), job), codeSandboxError)
	if applied := h.fake.Applied(); len(applied) != 0 {
		t.Fatalf("applied = %d documents, want none for an unusable test id", len(applied))
	}
}

func TestAXBackend_PatchNotApplying_ReportsPatchRejectedWithoutCreatingResources(t *testing.T) {
	h := newAXHarness(t, axHarnessOptions{})
	patch := strings.Replace(touchPatch, "Greeting returns", "Greeting yields", 1)
	assertFailure(t, h.backend.Run(context.Background(), axJob(patch)), codePatchRejected)
	if len(h.fake.Applied()) != 0 || len(h.fake.Deleted()) != 0 {
		t.Fatalf("applied %d, deleted %v, want no AX call", len(h.fake.Applied()), h.fake.Deleted())
	}
}

func TestAXBackend_UnknownBaseCommit_RunsNothing(t *testing.T) {
	h := newAXHarness(t, axHarnessOptions{})
	job := axJob(touchPatch)
	job.BaseCommit = "main"
	assertFailure(t, h.backend.Run(context.Background(), job), codeUnknownBaseCommit)
	if len(h.fake.Applied()) != 0 || len(h.fake.Deleted()) != 0 {
		t.Fatalf("applied %d, deleted %v, want no AX call", len(h.fake.Applied()), h.fake.Deleted())
	}
}

func TestAXBackend_ExitZeroOrOne_ReportsVerdictWithParsedCounts(t *testing.T) {
	cases := []struct {
		name     string
		scenario mockax.Scenario
		want     Outcome
	}{
		{"exit zero", mockax.Scenario{ExitCode: 0, Output: mockax.GoTestOutput(3, 0)}, Outcome{Verdict: true, Passed: true, Total: 3}},
		{"exit one", mockax.Scenario{ExitCode: 1, Output: mockax.GoTestOutput(2, 1)}, Outcome{Verdict: true, Passed: false, Total: 3, Failed: 1}},
		{"exit zero with a failure line", mockax.Scenario{ExitCode: 0, Output: mockax.GoTestOutput(1, 1)}, Outcome{Verdict: true, Passed: false, Total: 2, Failed: 1}},
		{"truncated output counts what arrived", mockax.Scenario{ExitCode: 1, Output: mockax.GoTestOutput(4, 0), OutputTruncated: true, RunningRounds: 2}, Outcome{Verdict: true, Passed: false, Total: 4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{Scenarios: map[string]mockax.Scenario{digestOf(t, touchPatch): tc.scenario}}})
			if got := h.backend.Run(context.Background(), axJob(touchPatch)); got != tc.want {
				t.Fatalf("outcome = %+v, want %+v", got, tc.want)
			}
			h.assertCleanedUp(t)
		})
	}
}

func TestAXBackend_OtherExitCode_ReportsSandboxError(t *testing.T) {
	for _, exitCode := range []int{2, 91, 125, -1} {
		h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{Default: &mockax.Scenario{ExitCode: exitCode, Output: mockax.GoTestOutput(1, 0)}}})
		assertFailure(t, h.backend.Run(context.Background(), axJob(touchPatch)), codeSandboxError)
		h.assertCleanedUp(t)
	}
}

func TestAXBackend_StartupFailure_DeletesResourcesAndReportsSandboxError(t *testing.T) {
	h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{Default: &mockax.Scenario{Startup: mockax.StartupFailed}}})
	assertFailure(t, h.backend.Run(context.Background(), axJob(touchPatch)), codeSandboxError)
	h.assertCleanedUp(t)
	if n := h.resultQueries(t); n != 0 {
		t.Fatalf("result queries = %d, want none for a Task that never started", n)
	}
}

func TestAXBackend_ResumeFails_DeletesResourcesAndReportsSandboxError(t *testing.T) {
	cases := map[string]mockax.Scenario{
		"resume refused":             {ResumeRefused: true},
		"task failed after a resume": {Startup: mockax.StartupFailed},
	}
	for name, scenario := range cases {
		t.Run(name, func(t *testing.T) {
			h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{Default: &scenario}})
			assertFailure(t, h.backend.Run(context.Background(), axJob(touchPatch)), codeSandboxError)
			h.assertCleanedUp(t)
			if resumed := h.fake.Resumed(); !slices.Equal(resumed, []string{axTestTask}) {
				t.Fatalf("resumes = %v, want exactly one for %s", resumed, axTestTask)
			}
			if ready, results := h.arrivals(t, mockax.KindReady), h.resultQueries(t); ready != 0 || results != 0 {
				t.Fatalf("readiness queries %d, result queries %d, want none for a Task that did not start", ready, results)
			}
		})
	}
}

func TestAXBackend_ResumeOutcomeUnknown_DeletesDerivedNameWithoutSecondResume(t *testing.T) {
	check := func(t *testing.T, h *axHarness, outcome Outcome) {
		t.Helper()
		assertFailure(t, outcome, codeSandboxError)
		if applied, resumed := h.fake.Applied(), h.fake.Resumed(); len(applied) != 1 || len(resumed) > 1 {
			t.Fatalf("applied %d, resumed %v, want one create and at most the one resume", len(applied), resumed)
		}
		if n := h.arrivals(t, mockax.KindResume); n != 1 {
			t.Fatalf("resume calls = %d, want one and no retry", n)
		}
		if state, ready := h.arrivals(t, mockax.KindState), h.arrivals(t, mockax.KindReady); state != 0 || ready != 0 {
			t.Fatalf("state reads %d, readiness queries %d, want none after an unconfirmed resume", state, ready)
		}
		h.assertCleanedUp(t)
	}

	t.Run("resume took effect but was not confirmed", func(t *testing.T) {
		h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{ResumeUncertain: true}})
		check(t, h, h.backend.Run(context.Background(), axJob(touchPatch)))
	})

	t.Run("resume command never exited", func(t *testing.T) {
		held := make(chan string, 1)
		h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{HeldNotify: held}})
		h.controlStandIn(t, "pause", `{"kinds":["resume"]}`)
		done := make(chan Outcome, 1)
		go func() { done <- h.backend.Run(context.Background(), axJob(touchPatch)) }()
		if kind := <-held; kind != mockax.KindResume {
			t.Fatalf("held %q, want the resume", kind)
		}
		// Dropping the held call ends it without an answer, as a command that never exits
		// on its own does.
		h.fake.StopHolding()
		check(t, h, <-done)
	})
}

func TestAXBackend_NewTaskSuspendedBeforeResume_IsNotAFailure(t *testing.T) {
	held := make(chan string, 1)
	h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{HeldNotify: held}})
	h.controlStandIn(t, "pause", `{"kinds":["resume"]}`)
	done := make(chan Outcome, 1)
	go func() { done <- h.backend.Run(context.Background(), axJob(touchPatch)) }()
	if kind := <-held; kind != mockax.KindResume {
		t.Fatalf("held %q, want the resume", kind)
	}
	if kinds := h.arrivalKinds(t); !slices.Equal(kinds, []string{mockax.KindApply, mockax.KindResume}) {
		t.Fatalf("calls before the resume completed = %v, want only the create and the resume", kinds)
	}
	if state, err := h.fake.TaskState(context.Background(), axTestAtespace, axTestTask); err != nil || state != mockax.TaskSuspended {
		t.Fatalf("new task state = %q, %v, want suspended until resumed", state, err)
	}
	h.controlStandIn(t, "release", "")
	if got := <-done; !got.Verdict || !got.Passed {
		t.Fatalf("outcome = %+v, want the passing verdict", got)
	}
	h.assertCleanedUp(t)
}

func TestAXBackend_ReadyAfterResume_SkipsReadyzPolling(t *testing.T) {
	h := newAXHarness(t, axHarnessOptions{})
	if got := h.backend.Run(context.Background(), axJob(touchPatch)); !got.Verdict || !got.Passed {
		t.Fatalf("outcome = %+v, want the passing verdict", got)
	}
	want := []string{mockax.KindApply, mockax.KindResume, mockax.KindState, mockax.KindState, mockax.KindResult, mockax.KindDelete}
	if kinds := h.arrivalKinds(t); !slices.Equal(kinds, want) {
		t.Fatalf("calls = %v, want %v: one state read decides readiness, no readiness query", kinds, want)
	}
	h.assertCleanedUp(t)
}

func TestAXBackend_NotReadyAfterResume_PollsReadyzUntil200(t *testing.T) {
	t.Run("not ready, unreachable and reset answers until 200", func(t *testing.T) {
		scenario := mockax.Scenario{
			Startup: mockax.StartupReadyLater, ReadyAnswers: []int{http.StatusServiceUnavailable, http.StatusNotFound, 0, http.StatusServiceUnavailable},
			Output: mockax.GoTestOutput(4, 0),
		}
		h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{Default: &scenario}})
		if got := h.backend.Run(context.Background(), axJob(touchPatch)); got != (Outcome{Verdict: true, Passed: true, Total: 4}) {
			t.Fatalf("outcome = %+v, want the passing verdict of 4 tests", got)
		}
		if n := h.arrivals(t, mockax.KindReady); n != 5 {
			t.Fatalf("readiness queries = %d, want 5: four unready answers, then 200", n)
		}
		if elapsed := h.clock.elapsed(); elapsed != 4*time.Second {
			t.Fatalf("finished after %s, want one readiness query per second from 0s to 4s", elapsed)
		}
		h.assertCleanedUp(t)
	})

	t.Run("three failed readiness queries in a row", func(t *testing.T) {
		scenario := mockax.Scenario{Startup: mockax.StartupReadyLater, ReadyAnswers: []int{http.StatusNotFound, 0, http.StatusBadGateway}}
		h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{Default: &scenario}})
		assertFailure(t, h.backend.Run(context.Background(), axJob(touchPatch)), codeSandboxError)
		if ready, results := h.arrivals(t, mockax.KindReady), h.resultQueries(t); ready != 3 || results != 0 {
			t.Fatalf("readiness queries %d, result queries %d, want 3 and none", ready, results)
		}
		h.assertCleanedUp(t)
	})

	t.Run("suspended while waiting for readiness", func(t *testing.T) {
		h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{Default: &mockax.Scenario{Startup: mockax.StartupNeverReady}}})
		control := &suspendingControl{AXTaskControl: h.fake, suspendFrom: 2}
		backend, err := NewAXBackend(AXBackendConfig{
			Atespace: axTestAtespace, Image: axTestImage, TestTimeout: time.Minute, ReadyTimeout: time.Minute,
			Control: control, Results: h.fake, Clock: h.clock,
		})
		if err != nil {
			t.Fatalf("new backend: %v", err)
		}
		assertFailure(t, backend.Run(context.Background(), axJob(touchPatch)), codeSandboxError)
		if n := h.arrivals(t, mockax.KindReady); n != 1 {
			t.Fatalf("readiness queries = %d, want only the one before the Task was seen suspended", n)
		}
		h.assertCleanedUp(t)
	})
}

// suspendingControl reports the Task suspended from its suspendFrom-th state read on.
type suspendingControl struct {
	AXTaskControl
	mu          sync.Mutex
	reads       int
	suspendFrom int
}

func (c *suspendingControl) TaskState(ctx context.Context, atespace, name string) (string, error) {
	c.mu.Lock()
	c.reads++
	reads := c.reads
	c.mu.Unlock()
	state, err := c.AXTaskControl.TaskState(ctx, atespace, name)
	if err == nil && reads >= c.suspendFrom {
		return AXTaskSuspended, nil
	}
	return state, err
}

func TestAXBackend_NeverReady_DeletesAfterReadyTimeout(t *testing.T) {
	h := newAXHarness(t, axHarnessOptions{
		fake:         mockax.Config{Default: &mockax.Scenario{Startup: mockax.StartupNeverReady}},
		readyTimeout: 5 * time.Second, testTimeout: time.Minute,
	})
	assertFailure(t, h.backend.Run(context.Background(), axJob(touchPatch)), codeSandboxError)
	h.assertCleanedUp(t)
	if elapsed := h.clock.elapsed(); elapsed != 5*time.Second {
		t.Fatalf("gave up after %s, want the 5s ready timeout", elapsed)
	}
	if reads, ready := h.arrivals(t, mockax.KindState), h.arrivals(t, mockax.KindReady); reads != 5 || ready != 5 {
		t.Fatalf("state reads %d, readiness queries %d, want one of each per second from 0s to 4s", reads, ready)
	}
	if n := h.resultQueries(t); n != 0 {
		t.Fatalf("result queries = %d, want none before the Task is ready", n)
	}
}

func TestAXBackend_TestTimeout_DeletesTaskAndReportsTimeout(t *testing.T) {
	cases := map[string]mockax.Scenario{
		"result never finishes":                                   {RunningRounds: mockax.Forever},
		"never ready, ready timeout longer than the test timeout": {Startup: mockax.StartupNeverReady},
	}
	for name, scenario := range cases {
		t.Run(name, func(t *testing.T) {
			h := newAXHarness(t, axHarnessOptions{
				fake:        mockax.Config{Default: &scenario},
				testTimeout: 10 * time.Second, readyTimeout: time.Minute,
			})
			assertFailure(t, h.backend.Run(context.Background(), axJob(touchPatch)), codeTimeout)
			h.assertCleanedUp(t)
			if elapsed := h.clock.elapsed(); elapsed != 10*time.Second {
				t.Fatalf("gave up after %s, want the 10s test timeout", elapsed)
			}
		})
	}
}

func TestAXBackend_CreateOutcomeUnknown_DeletesDerivedNameWithoutSecondCreate(t *testing.T) {
	h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{ApplyUncertain: true}})
	assertFailure(t, h.backend.Run(context.Background(), axJob(touchPatch)), codeSandboxError)
	if applied := h.fake.Applied(); len(applied) != 1 {
		t.Fatalf("applied = %d times, want one create and no retry", len(applied))
	}
	h.assertCleanedUp(t)
	if resumes, reads := h.arrivals(t, mockax.KindResume), h.arrivals(t, mockax.KindState); resumes != 0 || reads != 0 {
		t.Fatalf("resumes %d, state reads %d, want none after an uncertain create", resumes, reads)
	}
}

func TestAXBackend_DeleteFails_StillReportsOutcomeAndRecordsFailure(t *testing.T) {
	h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{DeleteFails: true}})
	outcome := h.backend.Run(context.Background(), axJob(touchPatch))
	if !outcome.Verdict || !outcome.Passed || outcome.Total != 12 {
		t.Fatalf("outcome = %+v, want the passing verdict despite the failed delete", outcome)
	}
	if deleted := h.fake.Deleted(); !slices.Equal(deleted, []string{axTestTask}) {
		t.Fatalf("delete calls = %v, want one for %s", deleted, axTestTask)
	}
	failures := h.recordLines(t, eventCleanupFailed, kindTest)
	if len(failures) != 1 || failures[0]["testId"] != axTestID || failures[0]["task"] != axTestTask {
		t.Fatalf("cleanup failure lines = %v, want one naming %s and %s", failures, axTestID, axTestTask)
	}
}

func TestAXBackend_ContextCancelled_DeletesTask(t *testing.T) {
	held := make(chan string, 1)
	h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{HeldNotify: held}})
	pause := httptest.NewRecorder()
	h.fake.ControlHandler().ServeHTTP(pause, httptest.NewRequest(http.MethodPost, "/control/pause", strings.NewReader(`{"kinds":["result"]}`)))
	if pause.Code != http.StatusOK {
		t.Fatalf("pause status = %d, body %s", pause.Code, pause.Body)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Outcome, 1)
	go func() { done <- h.backend.Run(ctx, axJob(touchPatch)) }()
	if kind := <-held; kind != mockax.KindResult {
		t.Fatalf("held %q, want the result query", kind)
	}
	cancel()
	outcome := <-done
	if outcome.Verdict || outcome.FailureCode == codeTimeout {
		t.Fatalf("outcome = %+v, want an interrupted run that is not a test timeout", outcome)
	}
	h.assertCleanedUp(t)
}

func TestAXBackend_OversizedOrMalformedResult_ReportsSandboxError(t *testing.T) {
	digest := digestOf(t, touchPatch)
	oversized := `{"state":"done","exitCode":0,"workspaceDigest":"` + digest + `","output":"` +
		strings.Repeat("a", maxTestOutputBytes+4096) + `","outputTruncated":true}`
	bodies := map[string]string{
		"oversized":            oversized,
		"not json":             "not json",
		"unknown member":       `{"state":"running","progress":1}`,
		"unknown state":        `{"state":"paused"}`,
		"done missing members": `{"state":"done","exitCode":0}`,
		"done with reason":     `{"state":"done","exitCode":0,"workspaceDigest":"` + digest + `","output":"","outputTruncated":false,"reason":"WORKSPACE_INVALID"}`,
		"bad digest format":    `{"state":"done","exitCode":0,"workspaceDigest":"sha256:ABC","output":"","outputTruncated":false}`,
		"running with output":  `{"state":"running","output":"partial"}`,
		"failed other reason":  `{"state":"failed","reason":"OOM"}`,
		"trailing data":        `{"state":"running"} {}`,
		"null exit code":       `{"state":"done","exitCode":null,"workspaceDigest":"` + digest + `","output":"","outputTruncated":false}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{Default: &mockax.Scenario{Body: []byte(body)}}})
			assertFailure(t, h.backend.Run(context.Background(), axJob(touchPatch)), codeSandboxError)
			h.assertCleanedUp(t)
		})
	}
}

func TestAXBackend_WorkspaceDigestMismatch_ReportsSandboxError(t *testing.T) {
	other := "sha256:" + strings.Repeat("0", 64)
	h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{Default: &mockax.Scenario{ExitCode: 0, Output: mockax.GoTestOutput(12, 0), WorkspaceDigest: other}}})
	assertFailure(t, h.backend.Run(context.Background(), axJob(touchPatch)), codeSandboxError)
	h.assertCleanedUp(t)
}

func TestAXBackend_SupervisorReportsWorkspaceInvalid_ReportsSandboxError(t *testing.T) {
	h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{Default: &mockax.Scenario{WorkspaceInvalid: true}}})
	assertFailure(t, h.backend.Run(context.Background(), axJob(touchPatch)), codeSandboxError)
	h.assertCleanedUp(t)
}

func TestAXBackend_ResultQueryFailsThreeRoundsInARow_DeletesResourcesAndReportsSandboxError(t *testing.T) {
	for name, status := range map[string]int{"transport error": 0, "bad gateway": http.StatusBadGateway} {
		t.Run(name, func(t *testing.T) {
			h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{Default: &mockax.Scenario{QueryFailures: 3, QueryFailureStatus: status, Output: mockax.GoTestOutput(1, 0)}}})
			assertFailure(t, h.backend.Run(context.Background(), axJob(touchPatch)), codeSandboxError)
			h.assertCleanedUp(t)
			if n := h.resultQueries(t); n != 3 {
				t.Fatalf("result queries = %d, want 3 before giving up", n)
			}
		})
	}
}

func TestAXBackend_ResultQueryFailsOnceThenSucceeds_ReportsVerdict(t *testing.T) {
	cases := map[string]mockax.Scenario{
		"one failure": {QueryFailures: 1, QueryFailureStatus: http.StatusServiceUnavailable, Output: mockax.GoTestOutput(5, 0)},
		"two failures, one running round, then done": {QueryFailures: 2, RunningRounds: 1, Output: mockax.GoTestOutput(5, 0)},
	}
	for name, scenario := range cases {
		t.Run(name, func(t *testing.T) {
			h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{Default: &scenario}})
			if got := h.backend.Run(context.Background(), axJob(touchPatch)); got != (Outcome{Verdict: true, Passed: true, Total: 5}) {
				t.Fatalf("outcome = %+v, want a passing verdict of 5 tests", got)
			}
			h.assertCleanedUp(t)
		})
	}
	// A success in between resets the count: fail twice, succeed with "running", fail
	// twice more, then finish.
	h := newAXHarness(t, axHarnessOptions{})
	reader := &flakyReader{inner: h.fake, failOn: map[int]bool{1: true, 2: true, 4: true, 5: true}, runningOn: map[int]bool{3: true}}
	backend, err := NewAXBackend(AXBackendConfig{
		Atespace: axTestAtespace, Image: axTestImage, TestTimeout: time.Minute, ReadyTimeout: time.Minute,
		Control: h.fake, Results: reader, Clock: h.clock,
	})
	if err != nil {
		t.Fatalf("new backend: %v", err)
	}
	if got := backend.Run(context.Background(), axJob(touchPatch)); !got.Verdict || !got.Passed {
		t.Fatalf("outcome = %+v, want the verdict after non-consecutive failures", got)
	}
}

// flakyReader fails or answers "running" on chosen query numbers and otherwise asks the
// stand-in.
type flakyReader struct {
	inner     AXResultReader
	mu        sync.Mutex
	query     int
	failOn    map[int]bool
	runningOn map[int]bool
}

func (r *flakyReader) ReadReady(ctx context.Context, atespace, task string) (int, error) {
	return r.inner.ReadReady(ctx, atespace, task)
}

func (r *flakyReader) ReadResult(ctx context.Context, atespace, task string) (int, io.ReadCloser, error) {
	r.mu.Lock()
	r.query++
	query := r.query
	r.mu.Unlock()
	switch {
	case r.failOn[query]:
		return http.StatusBadGateway, io.NopCloser(strings.NewReader("")), nil
	case r.runningOn[query]:
		return http.StatusOK, io.NopCloser(strings.NewReader(`{"state":"running"}`)), nil
	}
	return r.inner.ReadResult(ctx, atespace, task)
}

func TestAXBackend_OversizedOrNonUTF8Workspace_ReportsSandboxErrorWithoutCreatingResources(t *testing.T) {
	big := strings.Repeat("x", 64<<10)
	oversized := "--- /dev/null\n+++ b/big.txt\n@@ -0,0 +1 @@\n+" + big + "\n"
	nonUTF8Content := "--- /dev/null\n+++ b/bad.txt\n@@ -0,0 +1 @@\n+\xff\xfe\n"
	nonUTF8Path := "--- /dev/null\n+++ b/bad\xff.txt\n@@ -0,0 +1 @@\n+ok\n"
	for name, patch := range map[string]string{"over 64 KiB": oversized, "non-UTF-8 content": nonUTF8Content, "non-UTF-8 path": nonUTF8Path} {
		t.Run(name, func(t *testing.T) {
			if _, err := patchedFixture(t, patch); err != nil {
				t.Fatalf("precondition: patch does not apply: %v", err)
			}
			h := newAXHarness(t, axHarnessOptions{})
			assertFailure(t, h.backend.Run(context.Background(), axJob(patch)), codeSandboxError)
			if len(h.fake.Applied()) != 0 || len(h.fake.Deleted()) != 0 {
				t.Fatalf("applied %d, deleted %v, want no AX call", len(h.fake.Applied()), h.fake.Deleted())
			}
		})
	}
	// Exactly at the limit is still accepted.
	files, err := fixtureFiles()
	if err != nil {
		t.Fatalf("fixture files: %v", err)
	}
	used := 0
	for _, content := range files {
		used += len(content)
	}
	fill := strings.Repeat("y", maxWorkspaceContentBytes-used-1)
	atLimit := "--- /dev/null\n+++ b/fill.txt\n@@ -0,0 +1 @@\n+" + fill + "\n"
	h := newAXHarness(t, axHarnessOptions{})
	if got := h.backend.Run(context.Background(), axJob(atLimit)); !got.Verdict {
		t.Fatalf("outcome = %+v, want a workspace of exactly 64 KiB to run", got)
	}
}

// axDocument is a Workspace or Task document as the backend submits it.
type axDocument struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name     string `json:"name"`
		Atespace string `json:"atespace"`
	} `json:"metadata"`
	Spec json.RawMessage `json:"spec"`
}

func splitDocuments(t *testing.T, stream []byte) (axDocument, axDocument) {
	t.Helper()
	parts := strings.Split(strings.TrimSuffix(string(stream), "\n"), "\n---\n")
	if len(parts) != 2 {
		t.Fatalf("document stream has %d documents, want a Workspace and a Task", len(parts))
	}
	var workspace, task axDocument
	if err := json.Unmarshal([]byte(parts[0]), &workspace); err != nil || workspace.Kind != "Workspace" {
		t.Fatalf("first document %s is not a Workspace: %v", parts[0], err)
	}
	if err := json.Unmarshal([]byte(parts[1]), &task); err != nil || task.Kind != "Task" {
		t.Fatalf("second document %s is not a Task: %v", parts[1], err)
	}
	return workspace, task
}

func TestAXBackend_WorkspaceSpec_BindsOneWorkspaceAtWorkWithSortedRelativePaths(t *testing.T) {
	h := newAXHarness(t, axHarnessOptions{})
	if got := h.backend.Run(context.Background(), axJob(touchPatch)); !got.Verdict {
		t.Fatalf("outcome = %+v, want a verdict", got)
	}
	applied := h.fake.Applied()
	if len(applied) != 1 {
		t.Fatalf("applied = %d, want 1", len(applied))
	}
	workspace, task := splitDocuments(t, applied[0])
	for _, doc := range []axDocument{workspace, task} {
		if doc.APIVersion != "ax.io/v1alpha1" || doc.Metadata.Name != axTestTask || doc.Metadata.Atespace != axTestAtespace {
			t.Fatalf("document %+v, want ax.io/v1alpha1 named %s in %s", doc, axTestTask, axTestAtespace)
		}
	}

	var workspaceSpec struct {
		Files []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"files"`
	}
	if err := json.Unmarshal(workspace.Spec, &workspaceSpec); err != nil {
		t.Fatalf("decode workspace spec: %v", err)
	}
	want, err := patchedFixture(t, touchPatch)
	if err != nil {
		t.Fatalf("patched fixture: %v", err)
	}
	var paths []string
	for _, file := range workspaceSpec.Files {
		paths = append(paths, file.Path)
		if strings.HasPrefix(file.Path, "/") || string(want[file.Path]) != file.Content {
			t.Fatalf("file %q content %q, want a relative path holding the patched content", file.Path, file.Content)
		}
	}
	if !slices.Equal(paths, []string{"go.mod", "greet.go", "greet_test.go"}) {
		t.Fatalf("workspace paths = %v, want the patched tree in byte order", paths)
	}

	var taskSpec struct {
		Image      string `json:"image"`
		Workspaces []struct {
			Name string `json:"name"`
			Path string `json:"path"`
			Goal string `json:"goal"`
		} `json:"workspaces"`
		Env   []any `json:"env"`
		Debug *bool `json:"debug"`
	}
	if err := json.Unmarshal(task.Spec, &taskSpec); err != nil {
		t.Fatalf("decode task spec: %v", err)
	}
	if taskSpec.Image != axTestImage || len(taskSpec.Workspaces) != 1 || taskSpec.Workspaces[0].Name != axTestTask ||
		taskSpec.Workspaces[0].Path != "/work" || taskSpec.Workspaces[0].Goal != "" ||
		taskSpec.Env == nil || len(taskSpec.Env) != 0 || taskSpec.Debug == nil || *taskSpec.Debug {
		t.Fatalf("task spec = %s, want the pinned image, one Workspace at /work, an empty env list and debug false", task.Spec)
	}
}

func TestAXBackend_TaskSuspendedOrMissing_StopsQueryingAndReportsFailed(t *testing.T) {
	for name, fate := range map[string]mockax.Fate{"suspended": mockax.FateSuspended, "missing": mockax.FateMissing} {
		t.Run(name, func(t *testing.T) {
			h := newAXHarness(t, axHarnessOptions{fake: mockax.Config{Default: &mockax.Scenario{AfterReady: fate, Output: mockax.GoTestOutput(1, 0)}}})
			assertFailure(t, h.backend.Run(context.Background(), axJob(touchPatch)), codeSandboxError)
			if n := h.resultQueries(t); n != 0 {
				t.Fatalf("result queries = %d, want none once the Task is %s", n, name)
			}
			h.assertCleanedUp(t)
		})
	}
}

func TestAXBackend_RemoveOrphans_DeletesOnlyPrefixedTasksInItsAtespace(t *testing.T) {
	h := newAXHarness(t, axHarnessOptions{})
	h.fake.Seed(axTestAtespace, "emberling-sbx-aaaaaaaaaaaaaaaa")
	h.fake.Seed(axTestAtespace, "emberling-sbx-bbbbbbbbbbbbbbbb")
	h.fake.Seed(axTestAtespace, "someone-else")
	h.fake.Seed("other-atespace", "emberling-sbx-cccccccccccccccc")

	removed, err := h.backend.RemoveOrphans(context.Background())
	if err != nil || removed != 2 {
		t.Fatalf("removed = %d, %v, want 2", removed, err)
	}
	if left, _ := h.fake.List(context.Background(), axTestAtespace, ""); !slices.Equal(left, []string{"someone-else"}) {
		t.Fatalf("left in %s = %v, want only the unprefixed resource", axTestAtespace, left)
	}
	if left, _ := h.fake.List(context.Background(), "other-atespace", ""); len(left) != 1 {
		t.Fatalf("left in other-atespace = %v, want it untouched", left)
	}

	failing := newAXHarness(t, axHarnessOptions{fake: mockax.Config{DeleteFails: true}})
	failing.fake.Seed(axTestAtespace, "emberling-sbx-aaaaaaaaaaaaaaaa")
	if _, err := failing.backend.RemoveOrphans(context.Background()); err == nil {
		t.Fatal("RemoveOrphans succeeded although a delete failed")
	}
}

func TestAXBackend_TaskSpec_CarriesNoCallbackTokenURLOrKubeCredential(t *testing.T) {
	const kubeconfig = "/secret/kube/config-never-in-a-task"
	t.Setenv("KUBECONFIG", kubeconfig)
	h := newAXHarness(t, axHarnessOptions{})
	rcv := newReceiver(t)
	r := newRunner(t, h.backend, false)
	status, body := r.post(t, map[string]any{
		"callbackUrl": rcv.url(), "callbackToken": testToken, "baseCommit": FixtureBaseCommit, "patch": touchPatch,
	})
	if status != http.StatusAccepted {
		t.Fatalf("dispatch status = %d, body %v", status, body)
	}
	if got := rcv.next(t); got.Token != testToken || !bytes.Contains(got.Body.Payload, []byte(`"status":"SUCCEEDED"`)) {
		t.Fatalf("callback = %+v, want a SUCCEEDED delivery with the token", got)
	}

	applied := h.fake.Applied()
	if len(applied) != 1 {
		t.Fatalf("applied = %d, want 1", len(applied))
	}
	record, err := os.ReadFile(h.recordPath)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	host := strings.TrimPrefix(rcv.server.URL, "http://")
	for _, surface := range [][]byte{applied[0], record} {
		for _, secret := range []string{testToken, host, "/api/callbacks", "secret-query", kubeconfig, "KUBECONFIG", "kube"} {
			if bytes.Contains(surface, []byte(secret)) {
				t.Fatalf("%s reached AX or the record:\n%s", secret, surface)
			}
		}
	}
}

func TestAXBackend_TaskSpec_UsesFixedEnvAcrossTests(t *testing.T) {
	h := newAXHarness(t, axHarnessOptions{})
	first := axJob(touchPatch)
	second := axJob(breakingPatch)
	second.TestID = "test_fedcba9876543210"
	h.backend.Run(context.Background(), first)
	h.backend.Run(context.Background(), second)
	applied := h.fake.Applied()
	if len(applied) != 2 {
		t.Fatalf("applied = %d, want 2", len(applied))
	}
	_, firstTask := splitDocuments(t, applied[0])
	_, secondTask := splitDocuments(t, applied[1])
	firstSpec := strings.ReplaceAll(string(firstTask.Spec), "emberling-sbx-0123456789abcdef", "NAME")
	secondSpec := strings.ReplaceAll(string(secondTask.Spec), "emberling-sbx-fedcba9876543210", "NAME")
	if firstSpec != secondSpec {
		t.Fatalf("task specs differ beyond the name:\n%s\n%s", firstSpec, secondSpec)
	}
	if !strings.Contains(firstSpec, `"env":[]`) {
		t.Fatalf("task spec %s, want an empty env list", firstSpec)
	}
}

func TestAXBackend_ValidateMock_RejectsEveryMockMode(t *testing.T) {
	h := newAXHarness(t, axHarnessOptions{})
	if err := h.backend.ValidateMock(""); err != nil {
		t.Fatalf("ValidateMock(\"\") = %v, want nil", err)
	}
	for _, mode := range []string{mockPass, mockFail, mockError, mockDuplicate, mockLost, "anything"} {
		if err := h.backend.ValidateMock(mode); err == nil {
			t.Fatalf("ValidateMock(%q) = nil, want a rejection", mode)
		}
	}
}
