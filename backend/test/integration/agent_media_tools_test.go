//go:build integration

// run_checks and render_timeline end to end against PostgreSQL and an in-process media
// task service: a render of a timeline that was never checked, or whose newest check did
// not pass, is handed back to the Agent before any Attempt exists and nothing reaches the
// service; facts are decided per timeline digest, so a newer timeline passing never
// unlocks an older one; a render result and a check result committing together write
// each fact once; the canonical timeline a check returns is readable for the Run under
// the id derived from its digest; and a clip check that does not pass leaves a false
// verdict about the clip.
//
// They share the agentHarness of agent_loop_test.go, the fact fixtures of
// agent_action_facts_test.go and the fake task service of agent_external_tool_test.go.
package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/externaltask"
	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/externaltool"
)

const (
	mediaProviderID    = "media-service"
	mediaSourceID      = "src_demo"
	mediaTranscriptSha = "2222222222222222222222222222222222222222222222222222222222222222"
	mediaSourceSha     = "1111111111111111111111111111111111111111111111111111111111111111"
	mediaClipSha       = "3333333333333333333333333333333333333333333333333333333333333333"
)

// canonicalTimelineFor is the timeline as the media service canonicalises the given
// sentence range: sorted keys, no whitespace. Two ranges yield two digests.
func canonicalTimelineFor(from, to string) string {
	return `{"durationMs":42000,"ruleVersion":"v1","segments":[{"endMs":12000,"from":"` + from + `","startMs":3000,"text":"the method works","to":"` + to + `"}],` +
		`"sourceId":"` + mediaSourceID + `","sourceSha256":"` + mediaSourceSha + `","targetDurationMs":null,"transcriptSha256":"` + mediaTranscriptSha + `"}`
}

// timelineDigest is the identity of a canonical timeline: the lowercase hex SHA-256 of
// its text.
func timelineDigest(text string) string {
	return sha256Hex([]byte(text))
}

func timelineCheckCall(from, to string) mockmodel.Scenario {
	return wsCall(externaltool.RunChecksToolName, map[string]any{
		"kind": "TIMELINE",
		"timeline": map[string]any{
			"sourceId": mediaSourceID, "transcriptSha256": mediaTranscriptSha,
			"segments": []map[string]any{{"from": from, "to": to}},
		},
	})
}

func clipCheckCall(clipSha string) mockmodel.Scenario {
	return wsCall(externaltool.RunChecksToolName, map[string]any{"kind": "CLIP", "clipSha256": clipSha})
}

func renderCall(timelineSha string) mockmodel.Scenario {
	return wsCall(externaltool.RenderTimelineToolName, map[string]any{"timelineSha256": timelineSha})
}

// timelineCheckedPayload is the service's CHECK_TIMELINE callback for the canonical
// timeline, with the digest the Tool must find in the content.
func timelineCheckedPayload(timeline string, passed bool) string {
	findings := []map[string]any{}
	if !passed {
		findings = append(findings, map[string]any{"code": "DURATION_OUT_OF_RANGE", "level": "BLOCKING", "message": "42.0 s is outside 30 to 60 s", "segmentIndex": 0})
	}
	payload, _ := json.Marshal(map[string]any{"status": "SUCCEEDED", "result": map[string]any{
		"kind": "TIMELINE", "passed": passed, "ruleVersion": "v1",
		"subjectSha256": timelineDigest(timeline), "timelineSha256": timelineDigest(timeline), "durationMs": 42000,
		"timeline": timeline, "findings": findings,
	}})
	return string(payload)
}

func clipCheckedPayload(clipSha, timelineSha string, passed bool) string {
	payload, _ := json.Marshal(map[string]any{"status": "SUCCEEDED", "result": map[string]any{
		"kind": "CLIP", "passed": passed, "ruleVersion": "v1", "subjectSha256": clipSha, "timelineSha256": timelineSha,
		"findings": []map[string]any{{"code": "BLACK_FRAMES", "level": "BLOCKING", "message": "1.4 s of black frames"}},
	}})
	return string(payload)
}

func renderedPayload(clipSha, timelineSha string) string {
	payload, _ := json.Marshal(map[string]any{"status": "SUCCEEDED", "result": map[string]any{
		"clipSha256": clipSha, "clipUrl": "http://media.internal/clips/" + clipSha + ".mp4",
		"sizeBytes": 1048576, "mediaType": "video/mp4", "durationMs": 42000, "timelineSha256": timelineSha,
	}})
	return string(payload)
}

// mediaFixture is one Backend with both video Tools registered over the real externaltask
// client against the fake service, keeping timeline Artifacts in a real content store.
type mediaFixture struct {
	h         *agentHarness
	service   *fakeTaskService
	artifacts *asset.ArtifactStore
}

func newMediaFixture(t *testing.T, opts agentHarnessOptions) *mediaFixture {
	t.Helper()
	fake := newFakeTaskService()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	artifacts := asset.NewArtifactStore(t.TempDir(), 1<<20)
	if err := artifacts.VerifyReadWrite(context.Background()); err != nil {
		t.Fatalf("artifact store: %v", err)
	}
	client := externaltask.New(server.URL, server.Client())
	for _, spec := range externaltool.MediaSpecs(artifacts) {
		spec.ProviderID = mediaProviderID
		reg, err := externaltool.Registration(spec, client)
		if err != nil {
			t.Fatalf("Registration(%s): %v", spec.Metadata.Name, err)
		}
		opts.Tools = append(opts.Tools, reg)
	}
	if opts.Clock == nil {
		opts.Clock = newExecClock(time.Now().UTC().Truncate(time.Millisecond))
	}
	return &mediaFixture{h: newAgentHarness(t, opts), service: fake, artifacts: artifacts}
}

// start scripts the TOOL_CALLs (the model answers FINAL afterwards), saves a Definition
// whose Agent may call both video Tools, creates the Run and executes it up to the first
// call that waits on the service or the loop's end.
func (f *mediaFixture) start(t *testing.T, workflowID string, calls ...mockmodel.Scenario) (domain.Run, service.AdvanceOutcome) {
	t.Helper()
	factScriptToolCalls(f.h, calls...)
	def := f.h.saveDefinition(factDefinition(workflowID, externaltool.RunChecksToolName, externaltool.RenderTimelineToolName))
	run := f.h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := f.h.claimAgentNode(run.ID)
	f.h.execute(outcome)
	return run, outcome
}

// waitingAction returns the Action of Turn turnNo, which must be waiting on the service,
// and the external task id the Tool computed for its first Attempt.
func (f *mediaFixture) waitingAction(t *testing.T, agentRunID string, turnNo int) (domain.AgentAction, string) {
	t.Helper()
	action := wsActionOfTurn(t, f.h, agentRunID, turnNo)
	if action.Status != domain.AgentActionWaitingCallback {
		t.Fatalf("action of turn %d = status %s, want WAITING_CALLBACK", turnNo, action.Status)
	}
	return action, externaltool.TaskID(action.ID, 1)
}

// deliver hands the fake service's callback for taskID to the callback intake with the
// token the Tool sent, then drains the Turns the result continues.
func (f *mediaFixture) deliver(t *testing.T, taskID, payload string) {
	t.Helper()
	task := f.service.task(t, taskID)
	delivered, err := f.h.svc.HandleCallback(f.h.ctx, service.HandleCallback{Token: task.callbackToken, ExternalTaskID: taskID, Payload: json.RawMessage(payload)})
	if err != nil || !delivered.Accepted || delivered.Duplicate {
		t.Fatalf("callback for %s = %+v, %v, want accepted", taskID, delivered, err)
	}
	if err := f.h.drainTurns(f.h.ctx); err != nil {
		t.Fatalf("drain turns after the callback: %v", err)
	}
}

// assertRenderRefused proves the render Action of Turn turnNo was handed back as
// VERIFICATION_MISSING in its claim transaction: FAILED with the code, no Attempt, no
// AGENT_ACTION_STARTED, AGENT_ACTION_FAILED with returnedToAgent and the AUTHORIZATION
// source, a REJECTED tool message naming the missing fact type, and exactly one new Turn.
func assertRenderRefused(t *testing.T, f *mediaFixture, run domain.Run, agentRunID string, turnNo int) {
	t.Helper()
	action := wsActionOfTurn(t, f.h, agentRunID, turnNo)
	if action.Status != domain.AgentActionFailed || action.Error == nil || action.Error.Code != string(domain.ReturnVerificationMissing) {
		t.Fatalf("render action = status %s error %+v, want FAILED with VERIFICATION_MISSING", action.Status, action.Error)
	}
	if attempts := agentToolAttempts(f.h.ctx, t, f.h.uow, action.ID); len(attempts) != 0 {
		t.Errorf("tool attempts of the refused render = %+v, want none", attempts)
	}
	events := listEvents(f.h.ctx, t, f.h.uow, run.ID)
	assertContiguousSeq(t, events)
	if started := agentPayloadsFor(t, events, domain.EventAgentActionStarted, "actionId", action.ID); len(started) != 0 {
		t.Errorf("AGENT_ACTION_STARTED for the refused render = %v, want none", started)
	}
	failed := agentOnlyPayloadFor(t, events, domain.EventAgentActionFailed, "actionId", action.ID)
	if failed["returnedToAgent"] != true || failed["failureSource"] != string(domain.FailureAuthorization) {
		t.Errorf("AGENT_ACTION_FAILED = %v, want returnedToAgent with the AUTHORIZATION source", failed)
	}
	message := wsToolMessage(t, f.h, agentRunID, turnNo, action.ID)
	if message["status"] != "REJECTED" || message["code"] != string(domain.ReturnVerificationMissing) {
		t.Errorf("tool message = %v, want REJECTED VERIFICATION_MISSING", message)
	}
	if text, _ := message["message"].(string); !strings.Contains(text, externaltool.FactTypeMediaChecked) {
		t.Errorf("tool message %q should name the missing fact type", text)
	}
	if !agentTurnNoExists(f.h.ctx, t, f.h.uow, agentRunID, turnNo+1) || agentTurnNoExists(f.h.ctx, t, f.h.uow, agentRunID, turnNo+2) {
		t.Errorf("want exactly one Turn to follow the refusal of turn %d", turnNo)
	}
}

func factsOfType(facts []domain.ExecutionFact, factType string) []domain.ExecutionFact {
	var matching []domain.ExecutionFact
	for _, fact := range facts {
		if fact.FactType == factType {
			matching = append(matching, fact)
		}
	}
	return matching
}

func assertMediaChecked(t *testing.T, fact domain.ExecutionFact, subject, kind string, verdict bool) {
	t.Helper()
	if fact.FactType != externaltool.FactTypeMediaChecked || fact.SubjectRef != subject || fact.Verdict == nil || *fact.Verdict != verdict {
		t.Errorf("fact = %+v, want media_checked about %s with verdict %v", fact, subject, verdict)
	}
	var binding map[string]any
	_ = json.Unmarshal(fact.Binding, &binding)
	if binding["kind"] != kind || binding["ruleVersion"] != "v1" {
		t.Errorf("binding = %v, want kind %s and ruleVersion v1", binding, kind)
	}
}

// Nothing checked the timeline: the render is refused before any Attempt and the service
// receives nothing at all.
func TestMediaTools_RenderNeverChecked_RefusedWithoutAnyCall(t *testing.T) {
	f := newMediaFixture(t, agentHarnessOptions{})
	timelineSha := timelineDigest(canonicalTimelineFor("S12", "S13"))
	run, outcome := f.start(t, "wf-media-render-unchecked", renderCall(timelineSha))
	agentRunID := factAgentRunID(t, f.h, outcome)

	assertRenderRefused(t, f, run, agentRunID, 1)
	if facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID); len(facts) != 0 {
		t.Errorf("facts = %+v, want none", facts)
	}
	if submits, gets := f.service.counts(); submits != 0 || gets != 0 {
		t.Errorf("the media service saw %d submits and %d status queries, want none", submits, gets)
	}
}

// The timeline was checked and did not pass: the fact exists with a false verdict, so the
// render is still refused and the service never receives a render task.
func TestMediaTools_TimelineCheckFailed_RenderRefused(t *testing.T) {
	f := newMediaFixture(t, agentHarnessOptions{})
	timeline := canonicalTimelineFor("S12", "S13")
	timelineSha := timelineDigest(timeline)
	run, outcome := f.start(t, "wf-media-check-failed", timelineCheckCall("S12", "S13"), renderCall(timelineSha))
	agentRunID := factAgentRunID(t, f.h, outcome)
	check, taskID := f.waitingAction(t, agentRunID, 1)

	task := f.service.task(t, taskID)
	wantInput := `{"sourceId":"` + mediaSourceID + `","transcriptSha256":"` + mediaTranscriptSha + `","targetDurationMs":null,"segments":[{"from":"S12","to":"S13"}]}`
	if task.taskType != externaltool.TaskTypeCheckTimeline || string(task.input) != wantInput {
		t.Fatalf("dispatched task = %s %s, want CHECK_TIMELINE with %s", task.taskType, task.input, wantInput)
	}
	if bindings := agentToolBindings(t, f.h, agentOnlyToolAttempt(t, f.h, check.ID).ID); len(bindings) != 1 || bindings[0].ProviderID != mediaProviderID {
		t.Errorf("bindings = %+v, want one under %s", bindings, mediaProviderID)
	}
	f.deliver(t, taskID, timelineCheckedPayload(timeline, false))

	if check = getAgentAction(f.h.ctx, t, f.h.uow, check.ID); check.Status != domain.AgentActionSucceeded {
		t.Fatalf("check action status = %s, want SUCCEEDED: a check that does not pass is a successful call", check.Status)
	}
	assertRenderRefused(t, f, run, agentRunID, 2)
	facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID)
	if len(facts) != 1 {
		t.Fatalf("facts = %+v, want the one failed check", facts)
	}
	assertMediaChecked(t, facts[0], timelineSha, "TIMELINE", false)
	if submits, _ := f.service.counts(); submits != 1 {
		t.Errorf("the media service saw %d submits, want only the check", submits)
	}
}

// Facts are decided per timeline digest: after the first timeline failed and a revised
// one passed, rendering the old digest is refused while the revised one renders, and the
// render records clip_rendered bound to the timeline it came from.
func TestMediaTools_NewerTimelinePassed_OldDigestRefusedAndNewOneRenders(t *testing.T) {
	f := newMediaFixture(t, agentHarnessOptions{})
	old, revised := canonicalTimelineFor("S12", "S13"), canonicalTimelineFor("S12", "S14")
	oldSha, revisedSha := timelineDigest(old), timelineDigest(revised)
	run, outcome := f.start(t, "wf-media-per-subject",
		timelineCheckCall("S12", "S13"), timelineCheckCall("S12", "S14"), renderCall(oldSha), renderCall(revisedSha))
	agentRunID := factAgentRunID(t, f.h, outcome)

	_, firstCheck := f.waitingAction(t, agentRunID, 1)
	f.deliver(t, firstCheck, timelineCheckedPayload(old, false))
	_, secondCheck := f.waitingAction(t, agentRunID, 2)
	f.deliver(t, secondCheck, timelineCheckedPayload(revised, true))

	assertRenderRefused(t, f, run, agentRunID, 3)
	render, renderTask := f.waitingAction(t, agentRunID, 4)
	task := f.service.task(t, renderTask)
	if task.taskType != externaltool.TaskTypeRender || string(task.input) != `{"timelineSha256":"`+revisedSha+`"}` {
		t.Fatalf("dispatched render = %s %s, want RENDER of the revised digest", task.taskType, task.input)
	}
	f.deliver(t, renderTask, renderedPayload(mediaClipSha, revisedSha))

	if render = getAgentAction(f.h.ctx, t, f.h.uow, render.ID); render.Status != domain.AgentActionSucceeded {
		t.Fatalf("render action status = %s, want SUCCEEDED", render.Status)
	}
	facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID)
	checks := factsOfType(facts, externaltool.FactTypeMediaChecked)
	if len(checks) != 2 {
		t.Fatalf("media_checked facts = %+v, want one per timeline", checks)
	}
	bySubject := map[string]domain.ExecutionFact{checks[0].SubjectRef: checks[0], checks[1].SubjectRef: checks[1]}
	assertMediaChecked(t, bySubject[oldSha], oldSha, "TIMELINE", false)
	assertMediaChecked(t, bySubject[revisedSha], revisedSha, "TIMELINE", true)
	rendered := factsOfType(facts, externaltool.FactTypeClipRendered)
	if len(rendered) != 1 || rendered[0].SubjectRef != mediaClipSha || rendered[0].Verdict != nil {
		t.Fatalf("clip_rendered facts = %+v, want one about the clip without a verdict", rendered)
	}
	var binding map[string]any
	_ = json.Unmarshal(rendered[0].Binding, &binding)
	if binding["timelineSha256"] != revisedSha {
		t.Errorf("clip_rendered binding = %v, want timelineSha256 of the revised timeline", binding)
	}
	if submits, _ := f.service.counts(); submits != 3 {
		t.Errorf("the media service saw %d submits, want two checks and one render", submits)
	}
}

// The canonical timeline a passed check returned is an Artifact of the Run: its id is
// derived from the timeline digest, its content is the service's text byte for byte, and
// the Run reads it through the same use case the artifact content endpoint serves.
func TestMediaTools_TimelineArtifact_ReadableForTheRunUnderItsDigestID(t *testing.T) {
	f := newMediaFixture(t, agentHarnessOptions{})
	timeline := canonicalTimelineFor("S12", "S13")
	timelineSha := timelineDigest(timeline)
	run, outcome := f.start(t, "wf-media-timeline-artifact", timelineCheckCall("S12", "S13"))
	agentRunID := factAgentRunID(t, f.h, outcome)
	check, taskID := f.waitingAction(t, agentRunID, 1)
	f.deliver(t, taskID, timelineCheckedPayload(timeline, true))

	attempt := agentOnlyToolAttempt(t, f.h, check.ID)
	if attempt.Status != domain.ToolAttemptSucceeded {
		t.Fatalf("attempt status = %s, want SUCCEEDED", attempt.Status)
	}
	var result struct {
		TimelineArtifactID string `json:"timelineArtifactId"`
		TimelineSha256     string `json:"timelineSha256"`
		SubjectSha256      string `json:"subjectSha256"`
		Passed             bool   `json:"passed"`
	}
	if err := json.Unmarshal(attempt.Result, &result); err != nil {
		t.Fatalf("decode attempt result %s: %v", attempt.Result, err)
	}
	wantID, err := domain.ArtifactIDForSHA256(timelineSha)
	if err != nil {
		t.Fatal(err)
	}
	if result.TimelineArtifactID != wantID || result.TimelineSha256 != timelineSha || result.SubjectSha256 != timelineSha || !result.Passed {
		t.Fatalf("result = %s, want artifact %s about digest %s, passed", attempt.Result, wantID, timelineSha)
	}
	if !strings.HasPrefix(wantID, "artifact_"+timelineSha[:32]) || len(wantID) != len("artifact_")+32 {
		t.Errorf("artifact id %s is not the artifact_ prefix plus the first 32 hex characters of the digest", wantID)
	}

	reader := service.NewArtifactService(service.Deps{UoW: f.h.uow, ArtifactContent: f.artifacts})
	artifact, content, err := reader.OpenRunContent(f.h.ctx, run.ID, wantID)
	if err != nil {
		t.Fatalf("OpenRunContent: %v", err)
	}
	defer content.Close()
	stored, _ := io.ReadAll(content)
	if string(stored) != timeline {
		t.Errorf("stored timeline = %q, want the service's canonical text verbatim", stored)
	}
	if artifact.MediaType != domain.ArtifactMediaTypeTextPlain || artifact.SHA256 != timelineSha || artifact.SizeBytes != int64(len(timeline)) {
		t.Errorf("artifact = %+v, want UTF-8 text with the timeline digest and size", artifact)
	}
	var linked int
	if err := f.h.pool.QueryRow(f.h.ctx, `SELECT count(*) FROM tool_attempt_artifacts WHERE tool_attempt_id = $1`, attempt.ID).Scan(&linked); err != nil {
		t.Fatalf("count attempt artifact links: %v", err)
	}
	if linked != 1 {
		t.Errorf("artifacts linked to the attempt = %d, want the one timeline", linked)
	}
	facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID)
	if len(facts) != 1 {
		t.Fatalf("facts = %+v, want the one passed check", facts)
	}
	assertMediaChecked(t, facts[0], timelineSha, "TIMELINE", true)
}

// A service that returns a timeline whose content does not digest to the digest it names
// cannot establish an identity a render may trust: the Attempt fails, no Artifact is
// linked and no fact is written.
func TestMediaTools_TimelineDigestMismatch_FailsAttemptWithoutFactOrArtifact(t *testing.T) {
	f := newMediaFixture(t, agentHarnessOptions{})
	timeline := canonicalTimelineFor("S12", "S13")
	run, outcome := f.start(t, "wf-media-timeline-mismatch", timelineCheckCall("S12", "S13"))
	agentRunID := factAgentRunID(t, f.h, outcome)
	_, taskID := f.waitingAction(t, agentRunID, 1)
	payload := strings.Replace(timelineCheckedPayload(timeline, true), `\"from\":\"S12\"`, `\"from\":\"S11\"`, 1)
	if payload == timelineCheckedPayload(timeline, true) {
		t.Fatal("precondition: the tampered payload is unchanged")
	}

	task := f.service.task(t, taskID)
	delivered, err := f.h.svc.HandleCallback(f.h.ctx, service.HandleCallback{Token: task.callbackToken, ExternalTaskID: taskID, Payload: json.RawMessage(payload)})
	if err != nil || !delivered.Accepted {
		t.Fatalf("callback = %+v, %v, want accepted", delivered, err)
	}
	action := assertAgentActionFailed(t, f.h, run.ID, outcome, domain.TerminationToolError, domain.FailureCallback)
	if action.Error == nil || action.Error.Code != "TIMELINE_DIGEST_MISMATCH" {
		t.Errorf("action error = %+v, want TIMELINE_DIGEST_MISMATCH", action.Error)
	}
	attempt := agentOnlyToolAttempt(t, f.h, action.ID)
	var linked int
	if err := f.h.pool.QueryRow(f.h.ctx, `SELECT count(*) FROM tool_attempt_artifacts WHERE tool_attempt_id = $1`, attempt.ID).Scan(&linked); err != nil {
		t.Fatalf("count attempt artifact links: %v", err)
	}
	if attempt.Status != domain.ToolAttemptFailed || linked != 0 {
		t.Errorf("attempt = status %s with %d artifacts, want FAILED with none", attempt.Status, linked)
	}
	if facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID); len(facts) != 0 {
		t.Errorf("facts = %+v, want none", facts)
	}
}

// A clip check that does not pass is a successful call leaving media_checked about the
// clip with a false verdict; whether the Run may still deliver is decided elsewhere.
func TestMediaTools_ClipCheckFailed_RecordsFalseVerdictAboutTheClip(t *testing.T) {
	f := newMediaFixture(t, agentHarnessOptions{})
	timelineSha := timelineDigest(canonicalTimelineFor("S12", "S13"))
	_, outcome := f.start(t, "wf-media-clip-failed", clipCheckCall(mediaClipSha))
	agentRunID := factAgentRunID(t, f.h, outcome)
	check, taskID := f.waitingAction(t, agentRunID, 1)
	task := f.service.task(t, taskID)
	if task.taskType != externaltool.TaskTypeCheckClip || string(task.input) != `{"clipSha256":"`+mediaClipSha+`"}` {
		t.Fatalf("dispatched task = %s %s, want CHECK_CLIP of the clip digest", task.taskType, task.input)
	}
	f.deliver(t, taskID, clipCheckedPayload(mediaClipSha, timelineSha, false))

	if check = getAgentAction(f.h.ctx, t, f.h.uow, check.ID); check.Status != domain.AgentActionSucceeded {
		t.Fatalf("check action status = %s, want SUCCEEDED", check.Status)
	}
	attempt := agentOnlyToolAttempt(t, f.h, check.ID)
	var linked int
	if err := f.h.pool.QueryRow(f.h.ctx, `SELECT count(*) FROM tool_attempt_artifacts WHERE tool_attempt_id = $1`, attempt.ID).Scan(&linked); err != nil {
		t.Fatalf("count attempt artifact links: %v", err)
	}
	if linked != 0 {
		t.Errorf("artifacts linked to a clip check = %d, want none", linked)
	}
	facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID)
	if len(facts) != 1 {
		t.Fatalf("facts = %+v, want the one failed clip check", facts)
	}
	assertMediaChecked(t, facts[0], mediaClipSha, "CLIP", false)
}

// A render result of one Run and a timeline check result of another reach their resume
// transactions together; each commits its own fact exactly once.
func TestMediaTools_RenderAndCheckResultsCommitTogether_EachFactWrittenOnce(t *testing.T) {
	arrived := make(chan struct{})
	release := make(chan struct{})
	var barrier *pollArmedBarrierUoW
	f := newMediaFixture(t, agentHarnessOptions{
		WrapUoW: func(inner store.UnitOfWork) store.UnitOfWork {
			barrier = &pollArmedBarrierUoW{UnitOfWork: inner, arrived: arrived, release: release}
			return barrier
		},
	})
	first, second := canonicalTimelineFor("S12", "S13"), canonicalTimelineFor("S20", "S21")
	firstSha, secondSha := timelineDigest(first), timelineDigest(second)

	_, renderOutcome := f.start(t, "wf-media-race-render", timelineCheckCall("S12", "S13"), renderCall(firstSha))
	renderAgentRunID := factAgentRunID(t, f.h, renderOutcome)
	_, checkTask := f.waitingAction(t, renderAgentRunID, 1)
	f.deliver(t, checkTask, timelineCheckedPayload(first, true))
	renderAction, renderTask := f.waitingAction(t, renderAgentRunID, 2)

	_, checkOutcome := f.start(t, "wf-media-race-check", timelineCheckCall("S20", "S21"))
	checkAgentRunID := factAgentRunID(t, f.h, checkOutcome)
	checkAction, secondCheckTask := f.waitingAction(t, checkAgentRunID, 1)

	renderToken := f.service.task(t, renderTask).callbackToken
	checkToken := f.service.task(t, secondCheckTask).callbackToken
	barrier.armed.Store(true)
	var wg sync.WaitGroup
	var renderOut, checkOut service.CallbackOutcome
	var renderErr, checkErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		renderOut, renderErr = f.h.svc.HandleCallback(f.h.ctx, service.HandleCallback{Token: renderToken, ExternalTaskID: renderTask, Payload: json.RawMessage(renderedPayload(mediaClipSha, firstSha))})
	}()
	go func() {
		defer wg.Done()
		checkOut, checkErr = f.h.svc.HandleCallback(f.h.ctx, service.HandleCallback{Token: checkToken, ExternalTaskID: secondCheckTask, Payload: json.RawMessage(timelineCheckedPayload(second, true))})
	}()
	<-arrived
	<-arrived
	barrier.armed.Store(false)
	close(release)
	wg.Wait()
	if renderErr != nil || checkErr != nil || !renderOut.Accepted || renderOut.Duplicate || !checkOut.Accepted || checkOut.Duplicate {
		t.Fatalf("render callback = %+v, %v; check callback = %+v, %v; want both accepted", renderOut, renderErr, checkOut, checkErr)
	}
	if err := f.h.drainTurns(f.h.ctx); err != nil {
		t.Fatalf("drain turns after the callbacks: %v", err)
	}

	if renderAction = getAgentAction(f.h.ctx, t, f.h.uow, renderAction.ID); renderAction.Status != domain.AgentActionSucceeded {
		t.Errorf("render action status = %s, want SUCCEEDED", renderAction.Status)
	}
	if checkAction = getAgentAction(f.h.ctx, t, f.h.uow, checkAction.ID); checkAction.Status != domain.AgentActionSucceeded {
		t.Errorf("check action status = %s, want SUCCEEDED", checkAction.Status)
	}
	renderFacts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, renderAgentRunID)
	if checks, rendered := factsOfType(renderFacts, externaltool.FactTypeMediaChecked), factsOfType(renderFacts, externaltool.FactTypeClipRendered); len(checks) != 1 || len(rendered) != 1 {
		t.Errorf("facts of the rendering run = %+v, want one media_checked and one clip_rendered", renderFacts)
	} else {
		assertMediaChecked(t, checks[0], firstSha, "TIMELINE", true)
		if rendered[0].SubjectRef != mediaClipSha {
			t.Errorf("clip_rendered = %+v, want it about the clip", rendered[0])
		}
	}
	checkFacts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, checkAgentRunID)
	if len(checkFacts) != 1 {
		t.Fatalf("facts of the checking run = %+v, want exactly one", checkFacts)
	}
	assertMediaChecked(t, checkFacts[0], secondSha, "TIMELINE", true)
	for runID, actionID := range map[string]string{renderOutcome.RunID: renderAction.ID, checkOutcome.RunID: checkAction.ID} {
		events := listEvents(f.h.ctx, t, f.h.uow, runID)
		assertContiguousSeq(t, events)
		if completed := agentPayloadsFor(t, events, domain.EventAgentActionCompleted, "actionId", actionID); len(completed) != 1 {
			t.Errorf("run %s: AGENT_ACTION_COMPLETED for action %s = %d, want exactly 1", runID, actionID, len(completed))
		}
	}
}

// Keep the registry's view of the media Tools consistent with the Specs the fixture
// registers: both Tools resolve their fact declarations against each other.
func TestMediaTools_Registry_FactDeclarationsResolve(t *testing.T) {
	f := newMediaFixture(t, agentHarnessOptions{})
	if err := registry.ValidateFactDeclarations(f.h.tools.ListMetadata(), f.h.nodes.ListMetadata()); err != nil {
		t.Fatalf("ValidateFactDeclarations: %v", err)
	}
}
