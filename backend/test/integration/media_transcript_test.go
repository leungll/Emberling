//go:build integration

// Media Transcript node tests: the node dispatches one TRANSCRIBE task to a fake media
// service written against the task protocol, and every outcome below is a transaction, a
// race or a restart over real PostgreSQL. The service is an in-process httptest server
// reached through the real externaltask Adapter; callbacks are delivered through the same
// callback use case the API enters, with the one-time token the service received.
//
// They share the agentHarness of agent_loop_test.go, with the task_input and
// media_transcript nodes registered beside the Agent, so the same Run also proves what the
// Agent's first Turn saw of the transcript.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/externaltask"
	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/mediatranscript"
	"github.com/leungll/Emberling/backend/internal/nodes/taskinput"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
)

const (
	transcriptSourceID  = "src_welcome"
	transcriptSourceSHA = "9f8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a39281706f5e4d3c2b1a0"
	transcriptDuration  = 421000
	transcriptMaterial  = "S1\t0.000\t2.500\tHello and welcome to the show.\nS2\t2.500\t6.000\tToday we talk about embers.\nS3\t6.000\t9.000\tThank you for watching.\n"
	// transcriptTimeoutMs is the Attempt deadline the Definition sets on the node: ten
	// minutes, the bound a lost task must fail within.
	transcriptTimeoutMs = 600_000
)

// transcriptTaskInput is the Run input: one task object under the key "task".
var transcriptTaskInput = `{"task":{"sourceId":"` + transcriptSourceID + `","sourceSha256":"` + transcriptSourceSHA + `","durationMs":` + itoa(transcriptDuration) + `,"requirement":"a 30 second clip about embers","goal":"DELIVER_CLIP"}}`

// transcriptResult is the result object of a SUCCEEDED TRANSCRIBE task, with overrides.
func transcriptResult(overrides map[string]any) map[string]any {
	result := map[string]any{
		"sourceId": transcriptSourceID, "sourceSha256": transcriptSourceSHA,
		"transcriptSha256": sha256Hex([]byte(transcriptMaterial)), "language": "en",
		"sentenceCount": 3, "lineCount": 3, "durationMs": transcriptDuration, "model": "small.en",
		"material": transcriptMaterial,
	}
	for k, v := range overrides {
		result[k] = v
	}
	return result
}

func succeededCallback(overrides map[string]any) string {
	raw, _ := json.Marshal(map[string]any{"status": "SUCCEEDED", "result": transcriptResult(overrides)})
	return string(raw)
}

// fakeMediaService is the task protocol as the media service speaks it: it records every
// submitted task with its callback token and answers status queries from a script. It
// never calls back on its own; a test delivers the callback it chooses.
type fakeMediaService struct {
	mu      sync.Mutex
	submits []mediaSubmit
	// status answers GET by task id: an HTTP status and a body. Unknown ids are 404.
	status map[string]mediaStatus
}

type mediaSubmit struct {
	TaskID        string          `json:"taskId"`
	Type          string          `json:"type"`
	Input         json.RawMessage `json:"input"`
	CallbackURL   string          `json:"callbackUrl"`
	CallbackToken string          `json:"callbackToken"`
}

type mediaStatus struct {
	code int
	body string
}

func (s *fakeMediaService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/tasks":
		raw, _ := io.ReadAll(r.Body)
		var body mediaSubmit
		if err := json.Unmarshal(raw, &body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.submits = append(s.submits, body)
		s.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"taskId":"`+body.TaskID+`","status":"QUEUED"}`)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/tasks/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/tasks/")
		s.mu.Lock()
		answer, ok := s.status[id]
		s.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"UNKNOWN_TASK"}}`)
			return
		}
		w.WriteHeader(answer.code)
		_, _ = io.WriteString(w, answer.body)
	default:
		http.NotFound(w, r)
	}
}

func (s *fakeMediaService) submitted() []mediaSubmit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]mediaSubmit(nil), s.submits...)
}

// answer scripts the status query of one task.
func (s *fakeMediaService) answer(taskID string, code int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == nil {
		s.status = map[string]mediaStatus{}
	}
	s.status[taskID] = mediaStatus{code: code, body: body}
}

// transcriptEnv is one Backend process over a database, with the fake media service and
// the artifact store the node writes to. restart builds another over the same database.
type transcriptEnv struct {
	*agentHarness
	media     *fakeMediaService
	server    *httptest.Server
	artifacts *asset.ArtifactStore
	// tools are the Tools registered beside lookup, kept so a restart registers them too.
	tools []registry.ToolRegistration
}

func newTranscriptEnv(t *testing.T, tools ...registry.ToolRegistration) *transcriptEnv {
	t.Helper()
	media := &fakeMediaService{}
	server := httptest.NewServer(media)
	t.Cleanup(server.Close)
	artifacts := asset.NewArtifactStore(t.TempDir(), 1<<20)
	env := &transcriptEnv{media: media, server: server, artifacts: artifacts, tools: tools}
	env.agentHarness = newAgentHarness(t, agentHarnessOptions{Nodes: env.nodes(t), Tools: tools})
	env.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: "cut"}
	}
	return env
}

func (env *transcriptEnv) nodes(t *testing.T) []registry.NodeRegistration {
	t.Helper()
	transcript, err := mediatranscript.Registration(mediatranscript.Ports{
		Tasks:     externaltask.New(env.server.URL, env.server.Client()),
		Artifacts: env.artifacts,
	})
	if err != nil {
		t.Fatalf("media transcript registration: %v", err)
	}
	return []registry.NodeRegistration{taskinput.Registration(), transcript}
}

// restart models a new process over the same database: a new service with a new runner
// identity, the same clock, the same media service and the same artifact volume.
func (env *transcriptEnv) restart(t *testing.T) {
	t.Helper()
	previous := env.agentHarness
	env.agentHarness = newAgentHarness(t, agentHarnessOptions{
		Pool: previous.pool, Clock: previous.clock, Nodes: env.nodes(t), Tools: env.tools, RunnerID: previous.runnerID + "-restarted",
	})
	env.provider.Script = previous.provider.Script
}

// transcriptDefinition is task_input -> media_transcript -> agent -> text_output, with the
// Agent's input wired to the transcript reference. The node's execution policy sets the
// deadline a lost task must fail within and leaves an Attempt to spare.
func transcriptDefinition(workflowID string) domain.Definition {
	def := agentLoopDefinition(workflowID)
	def.Nodes = []domain.Node{
		{ID: "node_task", Type: "task_input", Name: "Task", Config: json.RawMessage(`{"inputKey":"task","required":true}`)},
		{ID: "node_transcript", Type: mediatranscript.NodeType, Name: "Transcript", Config: json.RawMessage(`{}`),
			ExecutionPolicy: &domain.ExecutionPolicy{TimeoutMs: transcriptTimeoutMs, MaxAttempts: 2, Backoff: domain.BackoffFixed}},
		def.Nodes[1],
		def.Nodes[2],
	}
	def.Edges = []domain.Edge{
		{ID: "e1", Source: "node_task", SourceHandle: "task", Target: "node_transcript", TargetHandle: mediatranscript.TaskPort},
		{ID: "e2", Source: "node_transcript", SourceHandle: mediatranscript.TranscriptPort, Target: "node_agent", TargetHandle: "input"},
		{ID: "e3", Source: "node_agent", SourceHandle: "text", Target: "node_output", TargetHandle: "text"},
	}
	return def
}

// transcriptDispatch is what a committed dispatch leaves the test holding.
type transcriptDispatch struct {
	run       domain.Run
	nodeRunID string
	attemptID string
	taskID    string
	token     string
}

// dispatch saves the Definition, creates the Run and drives it until the transcript
// task is submitted to the fake service and the Attempt is DISPATCHED.
func (env *transcriptEnv) dispatch(t *testing.T, workflowID string) transcriptDispatch {
	t.Helper()
	def := env.saveDefinition(transcriptDefinition(workflowID))
	run := env.createRun(def.WorkflowID, def.Version, transcriptTaskInput)
	return env.dispatchRun(t, run)
}

func (env *transcriptEnv) dispatchRun(t *testing.T, run domain.Run) transcriptDispatch {
	t.Helper()
	before := len(env.media.submitted())
	var outcome service.AdvanceOutcome
	for i := 0; i < 5; i++ {
		outcome = env.advance(run.ID)
		if !outcome.Claimed {
			t.Fatalf("advance claimed nothing before the transcript node")
		}
		env.execute(outcome)
		if outcome.NodeType == mediatranscript.NodeType {
			break
		}
	}
	if outcome.NodeType != mediatranscript.NodeType {
		t.Fatalf("the transcript node was never claimed")
	}
	submits := env.media.submitted()
	if len(submits) != before+1 {
		t.Fatalf("media service received %d tasks, want one new TRANSCRIBE task", len(submits)-before)
	}
	submit := submits[len(submits)-1]
	if submit.Type != mediatranscript.TaskType || string(submit.Input) != `{"sourceId":"`+transcriptSourceID+`"}` || submit.CallbackToken == "" {
		t.Fatalf("submitted task = %+v, want TRANSCRIBE of %s with a callback token", submit, transcriptSourceID)
	}
	if got := getAttempt(env.ctx, t, env.uow, outcome.AttemptID).Status; got != domain.NodeAttemptDispatched {
		t.Fatalf("Attempt status after dispatch = %s, want DISPATCHED", got)
	}
	return transcriptDispatch{run: run, nodeRunID: outcome.NodeRunID, attemptID: outcome.AttemptID, taskID: submit.TaskID, token: submit.CallbackToken}
}

// callback delivers one callback payload for the dispatched task, as the API would.
func (env *transcriptEnv) callback(t *testing.T, d transcriptDispatch, payload string) service.CallbackOutcome {
	t.Helper()
	outcome, err := env.svc.HandleCallback(env.ctx, service.HandleCallback{
		Token: d.token, ExternalTaskID: d.taskID, Payload: json.RawMessage(payload),
	})
	if err != nil {
		t.Fatalf("callback for task %s: %v", d.taskID, err)
	}
	return outcome
}

// finish drives the rest of the Run, the Agent included, to its terminal status.
func (env *transcriptEnv) finish(t *testing.T, runID string) domain.Run {
	t.Helper()
	for i := 0; i < 10; i++ {
		outcome := env.advance(runID)
		if !outcome.Claimed {
			break
		}
		env.execute(outcome)
	}
	return getRun(env.ctx, t, env.uow, runID)
}

func (env *transcriptEnv) nodeLinks(t *testing.T, runID string) []nodeLinkRow {
	t.Helper()
	rows, err := env.pool.Query(env.ctx, `SELECT node_attempt_id, artifact_id, run_id FROM node_attempt_artifacts WHERE run_id = $1`, runID)
	if err != nil {
		t.Fatalf("list node artifact links: %v", err)
	}
	defer rows.Close()
	var links []nodeLinkRow
	for rows.Next() {
		var link nodeLinkRow
		if err := rows.Scan(&link.NodeAttemptID, &link.ArtifactID, &link.RunID); err != nil {
			t.Fatalf("scan node artifact link: %v", err)
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list node artifact links: %v", err)
	}
	return links
}

func (env *transcriptEnv) artifactForRun(runID, artifactID string) (store.ArtifactRecord, error) {
	var record store.ArtifactRecord
	err := env.uow.WithinReadTx(env.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		record, err = tx.Artifacts().GetForRun(ctx, runID, artifactID)
		return err
	})
	return record, err
}

// transcriptOutput reads the node's committed `transcript` port.
func (env *transcriptEnv) transcriptOutput(t *testing.T, nodeRunID string) mediatranscript.Transcript {
	t.Helper()
	nodeRun := getNodeRun(env.ctx, t, env.uow, nodeRunID)
	var ports map[string]json.RawMessage
	if err := json.Unmarshal(nodeRun.Output, &ports); err != nil {
		t.Fatalf("decode transcript node output: %v", err)
	}
	var transcript mediatranscript.Transcript
	if err := json.Unmarshal(ports[mediatranscript.TranscriptPort], &transcript); err != nil {
		t.Fatalf("decode transcript port: %v", err)
	}
	return transcript
}

// assertNoMaterial proves the transcript text reached no NodeRun output, Attempt result,
// Event payload or model request of the Run.
func (env *transcriptEnv) assertNoMaterial(t *testing.T, runID string) {
	t.Helper()
	const marker = "Hello and welcome"
	for _, nodeRun := range listNodeRuns(env.ctx, t, env.uow, runID) {
		if strings.Contains(string(nodeRun.Output), marker) || strings.Contains(string(nodeRun.Input), marker) {
			t.Errorf("node run %s carries the transcript text", nodeRun.ID)
		}
		var attempts []domain.NodeAttempt
		if err := env.uow.WithinReadTx(env.ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			attempts, err = tx.NodeAttempts().ListByNodeRun(ctx, nodeRun.ID)
			return err
		}); err != nil {
			t.Fatalf("list attempts: %v", err)
		}
		for _, attempt := range attempts {
			if strings.Contains(string(attempt.Result), marker) || strings.Contains(string(attempt.Input), marker) {
				t.Errorf("attempt %s carries the transcript text", attempt.ID)
			}
		}
	}
	for _, event := range listEvents(env.ctx, t, env.uow, runID) {
		if strings.Contains(string(event.Payload), marker) {
			t.Errorf("event %s %s carries the transcript text", event.ID, event.Type)
		}
	}
	for i, request := range env.provider.Requests() {
		encoded, _ := json.Marshal(request)
		if strings.Contains(string(encoded), marker) {
			t.Errorf("model request %d carries the transcript text", i)
		}
	}
}

// TestMediaTranscript_Callback_StoresMaterialLinksArtifactAndAgentSeesReferenceOnly: the
// callback completes the node with a reference to the stored material; the Run reads the
// artifact through the node link; the Agent's first Turn receives the reference, its
// Manifest records the artifact as a reference, and the transcript text appears in no
// output, Event or model request.
func TestMediaTranscript_Callback_StoresMaterialLinksArtifactAndAgentSeesReferenceOnly(t *testing.T) {
	env := newTranscriptEnv(t)
	d := env.dispatch(t, "wf-transcript-callback")

	if outcome := env.callback(t, d, succeededCallback(nil)); !outcome.Accepted || outcome.Duplicate {
		t.Fatalf("callback outcome = %+v, want accepted", outcome)
	}
	if got := getNodeRun(env.ctx, t, env.uow, d.nodeRunID).Status; got != domain.NodeRunSucceeded {
		t.Fatalf("transcript NodeRun status = %s, want SUCCEEDED", got)
	}
	transcript := env.transcriptOutput(t, d.nodeRunID)
	wantSHA := sha256Hex([]byte(transcriptMaterial))
	if transcript.SHA256 != wantSHA || transcript.LineCount != 3 || transcript.SentenceCount != 3 || transcript.Language != "en" ||
		transcript.DurationMs != transcriptDuration || transcript.SourceID != transcriptSourceID || transcript.SourceSHA256 != transcriptSourceSHA {
		t.Fatalf("transcript output = %+v, want the reference of the stored material", transcript)
	}

	links := env.nodeLinks(t, d.run.ID)
	if len(links) != 1 || links[0].NodeAttemptID != d.attemptID || links[0].ArtifactID != transcript.ArtifactID {
		t.Fatalf("node artifact links = %+v, want one for attempt %s and artifact %s", links, d.attemptID, transcript.ArtifactID)
	}
	record, err := env.artifactForRun(d.run.ID, transcript.ArtifactID)
	if err != nil {
		t.Fatalf("GetForRun: %v", err)
	}
	content, err := env.artifacts.Open(env.ctx, record.StorageKey)
	if err != nil {
		t.Fatalf("open stored material: %v", err)
	}
	defer content.Close()
	stored, _ := io.ReadAll(content)
	if string(stored) != transcriptMaterial {
		t.Fatalf("stored material = %q, want the transcript", stored)
	}

	if run := env.finish(t, d.run.ID); run.Status != domain.RunCompleted {
		t.Fatalf("Run status = %s, want COMPLETED", run.Status)
	}
	agentRun := getAgentRunByNodeRun(env.ctx, t, env.uow, agentNodeRun(env.ctx, t, env.uow, d.run.ID).ID)
	manifests := listManifests(env.ctx, t, env.uow, agentRun.ID)
	if len(manifests) == 0 {
		t.Fatal("the Agent Run has no Turn Manifest")
	}
	first := manifests[0]
	if inputs := manifestItemsOfKind(first.Items, domain.ManifestTaskInput); len(inputs) != 1 {
		t.Fatalf("first Turn TASK_INPUT items = %+v, want one", inputs)
	}
	artifactItems := manifestItemsOfKind(first.Items, domain.ManifestArtifact)
	if len(artifactItems) != 1 || artifactItems[0].Ref != transcript.ArtifactID || artifactItems[0].SHA256 != wantSHA || artifactItems[0].Included {
		t.Fatalf("first Turn ARTIFACT items = %+v, want one omitted reference to %s", artifactItems, transcript.ArtifactID)
	}
	requests := env.provider.Requests()
	if len(requests) == 0 || !strings.Contains(string(requests[0].Messages[0].Content), transcript.ArtifactID) {
		t.Fatalf("the first model request does not carry the artifact id; requests=%d", len(requests))
	}
	env.assertNoMaterial(t, d.run.ID)
}

// TestMediaTranscript_DuplicateCallback_WritesTheResultOnce: a second delivery of the
// same callback is a duplicate: no second link, no second completion Event, no second
// stored result.
func TestMediaTranscript_DuplicateCallback_WritesTheResultOnce(t *testing.T) {
	env := newTranscriptEnv(t)
	d := env.dispatch(t, "wf-transcript-duplicate")

	env.callback(t, d, succeededCallback(nil))
	if outcome := env.callback(t, d, succeededCallback(nil)); !outcome.Duplicate {
		t.Fatalf("second callback outcome = %+v, want Duplicate", outcome)
	}
	if links := env.nodeLinks(t, d.run.ID); len(links) != 1 {
		t.Fatalf("node artifact links = %+v, want exactly one", links)
	}
	events := listEvents(env.ctx, t, env.uow, d.run.ID)
	if n := len(asyncNodeEventsOfType(events, d.nodeRunID, domain.EventNodeCompleted)); n != 1 {
		t.Fatalf("NODE_COMPLETED events = %d, want 1", n)
	}
	if n := len(asyncNodeEventsOfType(events, d.nodeRunID, domain.EventNodeCallbackReceived)); n != 1 {
		t.Fatalf("NODE_CALLBACK_RECEIVED events = %d, want 1", n)
	}
}

// TestMediaTranscript_SourceHashMismatch_FailsTheNodeWithoutArtifact: a result that names
// another source hash fails the Attempt and the NodeRun under the stable code; nothing is
// linked and the Run ends FAILED.
func TestMediaTranscript_SourceHashMismatch_FailsTheNodeWithoutArtifact(t *testing.T) {
	env := newTranscriptEnv(t)
	d := env.dispatch(t, "wf-transcript-mismatch")

	env.callback(t, d, succeededCallback(map[string]any{"sourceSha256": strings.Repeat("ab", 32)}))

	attempt := getAttempt(env.ctx, t, env.uow, d.attemptID)
	if attempt.Status != domain.NodeAttemptFailed || attempt.Error == nil || attempt.Error.Code != mediatranscript.CodeSourceMismatch {
		t.Fatalf("Attempt = %s error=%+v, want FAILED with %s", attempt.Status, attempt.Error, mediatranscript.CodeSourceMismatch)
	}
	if got := getNodeRun(env.ctx, t, env.uow, d.nodeRunID).Status; got != domain.NodeRunFailed {
		t.Fatalf("NodeRun status = %s, want FAILED", got)
	}
	if got := getRun(env.ctx, t, env.uow, d.run.ID).Status; got != domain.RunFailed {
		t.Fatalf("Run status = %s, want FAILED", got)
	}
	if links := env.nodeLinks(t, d.run.ID); len(links) != 0 {
		t.Fatalf("node artifact links = %+v, want none", links)
	}
	env.assertNoMaterial(t, d.run.ID)
}

// TestMediaTranscript_CrashAfterDispatch_RestartedProcessCompletesFromCallbackOrPoll: the
// process dies after the dispatch committed. A restarted process with a new runner
// identity leaves the dispatched Attempt to its task: the orphan scan does not touch it,
// and both a callback and a Provider poll complete it from the committed Binding and the
// Attempt's input alone.
func TestMediaTranscript_CrashAfterDispatch_RestartedProcessCompletesFromCallbackOrPoll(t *testing.T) {
	t.Run("callback", func(t *testing.T) {
		env := newTranscriptEnv(t)
		d := env.dispatch(t, "wf-transcript-crash-callback")
		env.restart(t)

		rec := reconciler.New(reconciler.Config{UoW: env.uow, Executor: env.svc, Clock: env.clock, BatchLimit: 100, RunnerID: env.runnerID})
		if _, err := rec.RunOnce(env.ctx); err != nil {
			t.Fatalf("reconcile after restart: %v", err)
		}
		if got := getAttempt(env.ctx, t, env.uow, d.attemptID).Status; got != domain.NodeAttemptDispatched {
			t.Fatalf("Attempt status after the restart's scan = %s, want DISPATCHED", got)
		}
		if outcome := env.callback(t, d, succeededCallback(nil)); !outcome.Accepted || outcome.Duplicate {
			t.Fatalf("callback after restart = %+v, want accepted", outcome)
		}
		if got := getNodeRun(env.ctx, t, env.uow, d.nodeRunID).Status; got != domain.NodeRunSucceeded {
			t.Fatalf("NodeRun status = %s, want SUCCEEDED", got)
		}
		transcript := env.transcriptOutput(t, d.nodeRunID)
		if _, err := env.artifactForRun(d.run.ID, transcript.ArtifactID); err != nil {
			t.Fatalf("GetForRun after restart: %v", err)
		}
		if run := env.finish(t, d.run.ID); run.Status != domain.RunCompleted {
			t.Fatalf("Run status = %s, want COMPLETED", run.Status)
		}
	})
	t.Run("poll", func(t *testing.T) {
		env := newTranscriptEnv(t)
		d := env.dispatch(t, "wf-transcript-crash-poll")
		env.restart(t)
		env.media.answer(d.taskID, http.StatusOK, `{"taskId":"`+d.taskID+`",`+strings.TrimPrefix(succeededCallback(nil), "{"))

		env.clock.Advance(6 * time.Second)
		outcome, err := env.svc.PollAttempt(env.ctx, service.PollAttempt{AttemptID: d.attemptID})
		if err != nil {
			t.Fatalf("poll after restart: %v", err)
		}
		if outcome.Kind != service.PollAttemptResumed || outcome.Failed {
			t.Fatalf("poll outcome = %+v, want RESUMED", outcome)
		}
		if got := getNodeRun(env.ctx, t, env.uow, d.nodeRunID).Status; got != domain.NodeRunSucceeded {
			t.Fatalf("NodeRun status = %s, want SUCCEEDED", got)
		}
		transcript := env.transcriptOutput(t, d.nodeRunID)
		if _, err := env.artifactForRun(d.run.ID, transcript.ArtifactID); err != nil {
			t.Fatalf("GetForRun after poll: %v", err)
		}
		events := listEvents(env.ctx, t, env.uow, d.run.ID)
		if n := len(asyncNodeEventsOfType(events, d.nodeRunID, domain.EventNodeCallbackReceived)); n != 0 {
			t.Fatalf("NODE_CALLBACK_RECEIVED events after a poll completion = %d, want 0", n)
		}
		env.assertNoMaterial(t, d.run.ID)
	})
}

// TestMediaTranscript_LostTask_AttemptFailsAtDeadlineAndNodeRunEnds: the service no
// longer knows the task. The node's poll resolves nothing, so the Attempt deadline ends
// it. The Runtime then fails the NodeRun and the Run: a NodeRun that entered
// WAITING_CALLBACK leaves it only for SUCCEEDED or FAILED, so no new Attempt dispatches
// the task again even though the execution policy has one to spare and the side effect is
// safe to repeat. That re-dispatch is a contract gap reported with this change, not a
// behaviour this test endorses.
func TestMediaTranscript_LostTask_AttemptFailsAtDeadlineAndNodeRunEnds(t *testing.T) {
	env := newTranscriptEnv(t)
	d := env.dispatch(t, "wf-transcript-lost")

	env.clock.Advance(6 * time.Second)
	outcome, err := env.svc.PollAttempt(env.ctx, service.PollAttempt{AttemptID: d.attemptID})
	if err != nil {
		t.Fatalf("poll of a lost task: %v", err)
	}
	if outcome.Kind != service.PollAttemptClaimedNotFinal || outcome.Status != registry.PollStatusUnknown {
		t.Fatalf("poll outcome = %+v, want CLAIMED_NOT_FINAL with UNKNOWN", outcome)
	}
	if got := getAttempt(env.ctx, t, env.uow, d.attemptID).Status; got != domain.NodeAttemptDispatched {
		t.Fatalf("Attempt status after the unknown answer = %s, want DISPATCHED", got)
	}

	env.clock.Advance(transcriptTimeoutMs * time.Millisecond)
	if err := env.svc.TimeoutAttempt(env.ctx, d.attemptID); err != nil {
		t.Fatalf("timeout: %v", err)
	}
	attempt := getAttempt(env.ctx, t, env.uow, d.attemptID)
	if attempt.Status != domain.NodeAttemptFailed || attempt.Error == nil || attempt.Error.Code != "TIMEOUT" {
		t.Fatalf("Attempt = %s error=%+v, want FAILED with TIMEOUT", attempt.Status, attempt.Error)
	}
	if got := getNodeRun(env.ctx, t, env.uow, d.nodeRunID).Status; got != domain.NodeRunFailed {
		t.Fatalf("NodeRun status = %s, want FAILED", got)
	}
	if got := getRun(env.ctx, t, env.uow, d.run.ID).Status; got != domain.RunFailed {
		t.Fatalf("Run status = %s, want FAILED", got)
	}
	if n := len(env.media.submitted()); n != 1 {
		t.Fatalf("media service received %d tasks, want the one original dispatch", n)
	}
	if links := env.nodeLinks(t, d.run.ID); len(links) != 0 {
		t.Fatalf("node artifact links = %+v, want none", links)
	}
}

// TestMediaTranscript_OtherRun_CannotReadTheArtifact: a second Run of the same Definition
// cannot read the first Run's transcript artifact, and a late callback of a Run that ended
// cannot make it readable either.
func TestMediaTranscript_OtherRun_CannotReadTheArtifact(t *testing.T) {
	env := newTranscriptEnv(t)
	first := env.dispatch(t, "wf-transcript-other-run")
	env.callback(t, first, succeededCallback(nil))
	transcript := env.transcriptOutput(t, first.nodeRunID)

	second := env.createRun(first.run.WorkflowID, first.run.DefinitionVersion, transcriptTaskInput)
	if _, err := env.artifactForRun(second.ID, transcript.ArtifactID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetForRun from the second Run before it transcribed: err = %v, want ErrNotFound", err)
	}

	// The second Run's task fails at the service; its Attempt declares nothing.
	d := env.dispatchRun(t, second)
	env.callback(t, d, `{"status":"FAILED","error":{"code":"SOURCE_UNAVAILABLE","message":"the source could not be read"}}`)
	if got := getNodeRun(env.ctx, t, env.uow, d.nodeRunID).Status; got != domain.NodeRunFailed {
		t.Fatalf("second Run's NodeRun status = %s, want FAILED", got)
	}
	if _, err := env.artifactForRun(second.ID, transcript.ArtifactID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetForRun from the second Run after its task failed: err = %v, want ErrNotFound", err)
	}
	if _, err := env.artifactForRun(first.run.ID, transcript.ArtifactID); err != nil {
		t.Fatalf("GetForRun from the first Run: %v", err)
	}
}
