//go:build integration

// This file holds the serving fault variants: one end-to-end test per fault an online
// photo effect service meets every day, each injected into many concurrent end users of
// the same serving template and each judged by the same per-task ledger. The harness is
// in serving_harness_test.go.
package contract

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/reconciler"
)

// harnessWorkers is the size of the work Pool every test Backend starts with.
const harnessWorkers = 4

// TestServing_Burst_TwentyConcurrentUsers_AllCompleteAndLedgerMatchesProviderOneToOne
// submits twenty users at once. Every Run completes, the Provider created exactly one
// task per Run, every task is bound to exactly one image NodeRun, and the Provider's
// in-flight peak stayed within its limit.
func TestServing_Burst_TwentyConcurrentUsers_AllCompleteAndLedgerMatchesProviderOneToOne(t *testing.T) {
	fx := newServingFixture(t, true)
	env := fx.startBackend(t, nil, nil)
	workflowID, version := saveServingDefinition(t, env, servingDefinition(t))
	photo := servingPhoto(t, env)
	base := fx.lastSeq(t)

	runIDs := env.servingUsersSubmit(t, workflowID, version, photo, servingUsers)
	env.waitAllCompleted(t, runIDs)

	ledger := readServingLedger(t, env, fx, runIDs, base)
	ledger.assertOneToOne(t)
	if got := ledger.CallbackCompleted + ledger.PollCompleted; got != servingUsers {
		t.Errorf("completions by callback or poll = %d, want %d", got, servingUsers)
	}
	if ledger.ReplayedDispatches != 0 || ledger.Takeovers != 0 {
		t.Errorf("replayed dispatches = %d, takeovers = %d, want none without a fault", ledger.ReplayedDispatches, ledger.Takeovers)
	}
	if ledger.InFlightPeak < 1 {
		t.Errorf("Provider in-flight peak = %d, want at least one task in flight", ledger.InFlightPeak)
	}
}

// TestServing_CrashWhileDispatchesHeld_RestartCompletesAllWithoutDuplicateTasks stops the
// Backend while its image dispatches are held at the Provider before acceptance. The
// stopped process commits nothing more; the Provider records the held requests as
// abandoned and creates no task for them. The restarted Backend times the expired
// Attempts out, retries them under the same idempotency key and runs every Run that had
// not reached the Provider yet, so all Runs complete with one task each.
func TestServing_CrashWhileDispatchesHeld_RestartCompletesAllWithoutDuplicateTasks(t *testing.T) {
	fx := newServingFixture(t, true)
	first := fx.startBackend(t, nil, nil)
	workflowID, version := saveServingDefinition(t, first, servingDefinition(t))
	photo := servingPhoto(t, first)
	base := fx.lastSeq(t)

	fx.control(t, http.MethodPost, "/control/pause", map[string]any{"kinds": []string{"task"}})
	runIDs := first.servingUsersSubmit(t, workflowID, version, photo, servingUsers)
	// Every worker of the Pool is now inside a held dispatch; the other Runs wait as
	// persisted READY work the in-process queue alone knew how to reach.
	fx.waitHeld(t, harnessWorkers)

	first.stop()
	fx.waitRecord(t, base, harnessWorkers, "abandoned dispatches", func(l providerRecordLine) bool {
		return l.Kind == "task" && l.Event == "abandoned"
	})
	fx.control(t, http.MethodPost, "/control/release", nil)

	clock := &takeoverClock{}
	second := fx.startBackend(t, first, clock)

	// The stopped process committed nothing after the crash: the held Attempts are still
	// STARTED, with no Callback Binding.
	started := 0
	for _, runID := range runIDs {
		nodeRun := second.nodeRunOf(t, runID, servingImageNode)
		if nodeRun == nil {
			continue
		}
		nodeRunID, _ := nodeRun["id"].(string)
		detail, _ := second.nodeRunDetail(t, runID, nodeRunID)
		for _, attempt := range attemptsOf(t, detail) {
			if status, _ := attempt["status"].(string); status == "STARTED" {
				started++
				if bindingOf(attempt) != nil {
					t.Errorf("run %s: a held Attempt carries a Callback Binding: %v", runID, attempt)
				}
			}
		}
	}
	if started != harnessWorkers {
		t.Fatalf("STARTED image Attempts after the crash = %d, want the %d held dispatches", started, harnessWorkers)
	}

	// The held Attempts' deadline passes, then each pass waits out one retry backoff.
	clock.advance(2 * time.Minute)
	second.reconcileUntilTerminal(t, clock, 2*time.Second, runIDs)
	second.waitAllCompleted(t, runIDs)

	ledger := readServingLedger(t, second, fx, runIDs, base)
	ledger.assertOneToOne(t)
	retried := 0
	for _, runID := range runIDs {
		_, detail := second.imageNodeRun(t, runID)
		if attempts := attemptsOf(t, detail); len(attempts) == 2 {
			retried++
			if status, _ := attempts[0]["status"].(string); status != "FAILED" {
				t.Errorf("run %s: held Attempt status = %q, want FAILED", runID, status)
			}
		}
	}
	if retried != harnessWorkers {
		t.Errorf("image NodeRuns with a retried Attempt = %d, want the %d held ones", retried, harnessWorkers)
	}
	if ledger.ReplayedDispatches != 0 {
		t.Errorf("replayed dispatches = %d, want 0 (an abandoned request claims no idempotency key)", ledger.ReplayedDispatches)
	}
}

// TestServing_CrashWhileWaitingForCallbacks_CallbacksAndPollsCompleteAll stops the
// Backend while every image task waits for its callback. After the restart half of the
// callbacks arrive and complete their Attempts; Provider polling completes the other
// half; the late callbacks of the polled tasks are answered as duplicates. No Attempt is
// redone and no task is created twice.
func TestServing_CrashWhileWaitingForCallbacks_CallbacksAndPollsCompleteAll(t *testing.T) {
	fx := newServingFixture(t, false)
	first := fx.startBackend(t, nil, nil)
	workflowID, version := saveServingDefinition(t, first, servingDefinition(t))
	photo := servingPhoto(t, first)
	base := fx.lastSeq(t)

	runIDs := first.servingUsersSubmit(t, workflowID, version, photo, servingUsers)
	first.waitAllImagesWaiting(t, runIDs)
	tasks := make([]string, len(runIDs))
	for i, runID := range runIDs {
		tasks[i] = first.boundTaskOf(t, runID)
	}

	first.stop()
	clock := &takeoverClock{}
	second := fx.startBackend(t, first, clock)

	half := len(runIDs) / 2
	fx.callbacks.releaseTasks(tasks[:half]...)
	second.waitAllCompleted(t, runIDs[:half])

	second.reconcileUntilTerminal(t, clock, 3*time.Second, runIDs[half:])
	second.waitAllCompleted(t, runIDs)

	fx.callbacks.releaseAll()
	fx.waitRecord(t, base, len(runIDs), "answered callback deliveries", func(l providerRecordLine) bool {
		return l.Kind == "task" && l.Event == "callback" && l.Status == http.StatusOK
	})

	ledger := readServingLedger(t, second, fx, runIDs, base)
	ledger.assertOneToOne(t)
	if ledger.CallbackCompleted != half || ledger.PollCompleted != len(runIDs)-half {
		t.Errorf("callback-completed = %d, poll-completed = %d, want %d and %d",
			ledger.CallbackCompleted, ledger.PollCompleted, half, len(runIDs)-half)
	}
	for _, runID := range runIDs {
		_, detail := second.imageNodeRun(t, runID)
		if attempts := attemptsOf(t, detail); len(attempts) != 1 {
			t.Errorf("run %s: image Attempts = %d, want 1 (a resume never redoes the dispatch)", runID, len(attempts))
		}
	}
	if got := ledger.ImageEvents["NODE_CALLBACK_RECEIVED"]; got != half {
		t.Errorf("NODE_CALLBACK_RECEIVED Events = %d, want %d (a late duplicate writes none)", got, half)
	}
}

// TestServing_DuplicateCallbacks_EachRunCompletesOnceWithoutExtraEvents has the Provider
// deliver every callback twice at once, then delivers each one a third time after the
// Runs completed. Each Run completes once, each image NodeRun records one callback, and
// the late deliveries add no Event.
func TestServing_DuplicateCallbacks_EachRunCompletesOnceWithoutExtraEvents(t *testing.T) {
	fx := newServingFixture(t, true)
	env := fx.startBackend(t, nil, nil)
	workflowID, version := saveServingDefinition(t, env, servingTemplateWithDirective(t, "mock:duplicate"))
	photo := servingPhoto(t, env)
	base := fx.lastSeq(t)

	runIDs := env.servingUsersSubmit(t, workflowID, version, photo, servingUsers)
	env.waitAllCompleted(t, runIDs)
	fx.waitRecord(t, base, 2*servingUsers, "callback deliveries", func(l providerRecordLine) bool {
		return l.Kind == "task" && l.Event == "callback"
	})

	eventsBefore := map[string]int{}
	for _, runID := range runIDs {
		eventsBefore[runID] = len(env.listEvents(t, runID))
	}
	for _, runID := range runIDs {
		task := env.boundTaskOf(t, runID)
		answer := fx.control(t, http.MethodPost, "/control/tasks/"+task+"/callback", nil)
		if status, _ := answer["status"].(float64); int(status) != http.StatusOK {
			t.Errorf("late redelivery of %s answered %v, want 200", task, answer)
		}
	}
	for _, runID := range runIDs {
		events := env.listEvents(t, runID)
		if len(events) != eventsBefore[runID] {
			t.Errorf("run %s Events = %d after the late redelivery, want %d", runID, len(events), eventsBefore[runID])
		}
		if got := len(eventsOfType(events, "RUN_COMPLETED")); got != 1 {
			t.Errorf("run %s RUN_COMPLETED Events = %d, want 1", runID, got)
		}
		assertContiguousSeq(t, events)
	}

	ledger := readServingLedger(t, env, fx, runIDs, base)
	ledger.assertOneToOne(t)
	if got := ledger.ImageEvents["NODE_CALLBACK_RECEIVED"]; got != servingUsers {
		t.Errorf("NODE_CALLBACK_RECEIVED Events = %d, want one per Run (%d)", got, servingUsers)
	}
	for task, deliveries := range ledger.CallbackDeliveries {
		if deliveries != 3 {
			t.Errorf("task %s callback deliveries = %d, want 3 (two at once, one late)", task, deliveries)
		}
	}
}

// TestServing_VersionSwitchMidFlight_InFlightRunsKeepTheirVersionNewRunsBindTheNewOne
// saves a new version of the serving template while the first users' images are in
// flight. Those Runs finish on the version they were bound to, with that version's
// image width; users who submit after the save get the new version and its width.
func TestServing_VersionSwitchMidFlight_InFlightRunsKeepTheirVersionNewRunsBindTheNewOne(t *testing.T) {
	fx := newServingFixture(t, false)
	env := fx.startBackend(t, nil, nil)
	workflowID, v1 := saveServingDefinition(t, env, servingDefinition(t))
	photo := servingPhoto(t, env)
	base := fx.lastSeq(t)

	half := servingUsers / 2
	inFlight := env.servingUsersSubmit(t, workflowID, v1, photo, half)
	env.waitAllImagesWaiting(t, inFlight)

	next := servingDefinition(t)
	setServingNodeConfig(t, next, servingImageNode, "width", float64(768))
	next["baseVersion"] = v1
	resp, body := env.doJSON(t, http.MethodPut, "/api/definitions/"+workflowID, next)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/definitions/%s status = %d, body=%s", workflowID, resp.StatusCode, body)
	}
	resp, body = env.doJSON(t, http.MethodGet, "/api/definitions/"+workflowID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/definitions/%s status = %d, body=%s", workflowID, resp.StatusCode, body)
	}
	latest, _ := decodeBody[map[string]any](t, body)["version"].(float64)
	v2 := int(latest)
	if v2 != v1+1 {
		t.Fatalf("latest version after the save = %d, want %d", v2, v1+1)
	}

	newRuns := env.servingUsersSubmit(t, workflowID, v2, photo, servingUsers-half)
	env.waitAllImagesWaiting(t, newRuns)
	fx.callbacks.releaseAll()
	all := append(append([]string{}, inFlight...), newRuns...)
	env.waitAllCompleted(t, all)

	check := func(runIDs []string, version, width int) {
		t.Helper()
		for _, runID := range runIDs {
			run, _ := env.runSnapshot(t, runID)["run"].(map[string]any)
			if got, _ := run["definitionVersion"].(float64); int(got) != version {
				t.Errorf("run %s definitionVersion = %v, want %d", runID, run["definitionVersion"], version)
			}
			output, _ := run["output"].(map[string]any)
			image, _ := output["image"].(map[string]any)
			if got, _ := image["width"].(float64); int(got) != width {
				t.Errorf("run %s output image width = %v, want %d (the bound version's config)", runID, image["width"], width)
			}
		}
	}
	check(inFlight, v1, 1024)
	check(newRuns, v2, 768)

	readServingLedger(t, env, fx, all, base).assertOneToOne(t)
}

// TestServing_ClientRetrySameKey_ConcurrentResubmissionsMergeIntoOneRunPerUser has each
// user's client send the same submission four times at once under one Idempotency-Key,
// as a gateway retry would. Each user gets one Run: one 201, every other answer a 200
// replay of that Run, and the merged submissions are counted.
func TestServing_ClientRetrySameKey_ConcurrentResubmissionsMergeIntoOneRunPerUser(t *testing.T) {
	const users, sends = 5, 4
	fx := newServingFixture(t, true)
	env := fx.startBackend(t, nil, nil)
	workflowID, version := saveServingDefinition(t, env, servingDefinition(t))
	photo := servingPhoto(t, env)
	base := fx.lastSeq(t)

	var submissions []servingSubmission
	for send := 0; send < sends; send++ {
		for user := 1; user <= users; user++ {
			submissions = append(submissions, servingSubmission{
				WorkflowID: workflowID, Version: version, Photo: photo,
				Request:        fmt.Sprintf("Portrait of user %d.", user),
				IdempotencyKey: fmt.Sprintf("serving-user-%d", user),
			})
		}
	}
	results := env.submitConcurrently(t, submissions)

	runByKey := map[string]string{}
	created := map[string]int{}
	merged := 0
	for i, r := range results {
		key := submissions[i].IdempotencyKey
		if previous, ok := runByKey[key]; ok && previous != r.RunID {
			t.Errorf("key %s answered with Runs %s and %s, want one", key, previous, r.RunID)
		}
		runByKey[key] = r.RunID
		switch r.Status {
		case http.StatusCreated:
			created[key]++
			if r.Replayed {
				t.Errorf("key %s: a 201 carries Idempotent-Replayed", key)
			}
		case http.StatusOK:
			merged++
			if !r.Replayed {
				t.Errorf("key %s: a 200 replay lacks Idempotent-Replayed: true", key)
			}
		}
	}
	for key, n := range created {
		if n != 1 {
			t.Errorf("key %s created %d Runs, want 1", key, n)
		}
	}
	if len(created) != users || merged != users*(sends-1) {
		t.Errorf("Runs created = %d, merged submissions = %d, want %d and %d", len(created), merged, users, users*(sends-1))
	}
	t.Logf("client retry: %d submissions merged into %d Runs (%d merged)", len(submissions), len(created), merged)

	runIDs := make([]string, 0, users)
	for _, runID := range runByKey {
		runIDs = append(runIDs, runID)
	}
	env.waitAllCompleted(t, runIDs)
	if got := runCountOfWorkflow(t, env, workflowID); got != users {
		t.Errorf("Runs of the workflow = %d, want %d", got, users)
	}
	readServingLedger(t, env, fx, runIDs, base).assertOneToOne(t)
}

// TestServing_ResponseLost_RetryTakesOverOriginalTaskAndItsCallbackCompletesIt has the
// Provider accept each image task and drop its answer. Attempt 1 fails as uncertain, the
// keyed retry is answered with the original task, and that task's callback -- carrying
// Attempt 1's credential -- completes Attempt 2. The Provider created one task per Run.
func TestServing_ResponseLost_RetryTakesOverOriginalTaskAndItsCallbackCompletesIt(t *testing.T) {
	const users = 3
	fx := newServingFixture(t, false)
	clock := &takeoverClock{}
	env := fx.startBackend(t, nil, clock)
	workflowID, version := saveServingDefinition(t, env, servingTemplateWithDirective(t, "mock:noResponse"))
	photo := servingPhoto(t, env)
	base := fx.lastSeq(t)

	runIDs := env.servingUsersSubmit(t, workflowID, version, photo, users)
	for _, runID := range runIDs {
		waitForScheduledRetry(t, env, runID, servingImageNode, servingWait)
	}
	fx.waitRecord(t, base, users, "dropped responses", func(l providerRecordLine) bool {
		return l.Kind == "task" && l.Event == "responseDropped"
	})

	// The backoff elapses; one Reconciler pass dispatches every retry.
	clock.advance(2 * time.Second)
	rec := reconciler.New(reconciler.Config{UoW: env.uow, Executor: env.execution, Clock: clock, BatchLimit: 100})
	if report, err := rec.RunOnce(context.Background()); err != nil || len(report.Errors) != 0 {
		t.Fatalf("reconciler pass: err=%v errors=%v", err, report.Errors)
	}
	env.waitAllImagesWaiting(t, runIDs)

	fx.callbacks.releaseAll()
	env.waitAllCompleted(t, runIDs)

	for _, runID := range runIDs {
		nodeRunID, detail := env.imageNodeRun(t, runID)
		attempts := attemptsOf(t, detail)
		if len(attempts) != 2 {
			t.Fatalf("run %s: image Attempts = %d, want 2", runID, len(attempts))
		}
		if status, _ := attempts[0]["status"].(string); status != "FAILED" {
			t.Errorf("run %s: Attempt 1 status = %q, want FAILED", runID, status)
		}
		if uncertain, _ := attempts[0]["uncertain"].(bool); !uncertain {
			t.Errorf("run %s: Attempt 1 uncertain = %v, want true", runID, attempts[0]["uncertain"])
		}
		if status, _ := attempts[1]["status"].(string); status != "SUCCEEDED" {
			t.Errorf("run %s: Attempt 2 status = %q, want SUCCEEDED", runID, status)
		}
		events := env.eventsForNodeRun(t, runID, nodeRunID)
		received := eventsOfType(events, "NODE_CALLBACK_RECEIVED")
		if len(received) != 1 {
			t.Fatalf("run %s: NODE_CALLBACK_RECEIVED Events = %d, want 1", runID, len(received))
		}
		if attemptNo, _ := payloadOf(t, received[0])["attemptNo"].(float64); attemptNo != 2 {
			t.Errorf("run %s: the original task's callback completed Attempt %v, want 2", runID, attemptNo)
		}
	}

	ledger := readServingLedger(t, env, fx, runIDs, base)
	ledger.assertOneToOne(t)
	if ledger.ReplayedDispatches != users || ledger.Takeovers != users {
		t.Errorf("replayed dispatches = %d, takeovers = %d, want %d each", ledger.ReplayedDispatches, ledger.Takeovers, users)
	}
	if ledger.CallbackCompleted != users {
		t.Errorf("callback-completed = %d, want %d", ledger.CallbackCompleted, users)
	}
}
