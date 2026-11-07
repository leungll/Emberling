//go:build integration

// The create_change_request Tool end to end against PostgreSQL: the Runtime hands a stale
// digest back as STALE_PATCH and an unverified patch back as VERIFICATION_MISSING before
// any Attempt exists, rejects a decision basis whose manifest reference resolves to no item,
// records the request with its content Artifact in the result transaction, makes a repeated
// request with the same content converge on the first record, and refuses a result whose
// content digest the Runtime cannot verify.
//
// They share the agentHarness of agent_loop_test.go, the workspace fixtures of
// agent_workspace_test.go and the runner stub of agent_verify_patch_test.go.
package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/changerequest"
	"github.com/leungll/Emberling/backend/internal/tools/verifypatch"
)

const (
	crSummary      = "Upgrade golang-jwt to v5 to close the header parsing advisory."
	crImpact       = "Token parsing returns sentinel errors; callers were adapted."
	crRollbackPlan = "Redeploy the previous version of the service."
	crDoubleTool   = "fake_change_request"
)

// changeRequestFixture is one Backend with the workspace Tools, verify_patch against the
// runner stub and create_change_request registered, plus the artifact store both Tools
// write text to.
type changeRequestFixture struct {
	h         *agentHarness
	repo      *workspaceRepo
	stub      *verifyRunnerStub
	artifacts *asset.ArtifactStore
	run       domain.Run
	outcome   service.AdvanceOutcome
}

func newChangeRequestFixture(t *testing.T, extra ...registry.ToolRegistration) *changeRequestFixture {
	t.Helper()
	stub := newVerifyRunnerStub()
	runner := httptest.NewServer(stub)
	t.Cleanup(runner.Close)
	artifacts := asset.NewArtifactStore(t.TempDir(), 1<<20)
	if err := artifacts.VerifyReadWrite(context.Background()); err != nil {
		t.Fatalf("artifact store: %v", err)
	}
	repo := newWorkspaceRepo(t)
	tools := []registry.ToolRegistration{
		verifypatch.Registration(runner.URL, runner.Client(), artifacts),
		changerequest.Registration(artifacts),
	}
	h := newWorkspaceHarness(t, workspaceHarnessOptions{repo: repo, extraTools: append(tools, extra...)})
	return &changeRequestFixture{h: h, repo: repo, stub: stub, artifacts: artifacts}
}

// crCall is one scripted TOOL_CALL whose arguments are built when the model is asked, so a
// call can name a record, such as a Tool Attempt, that an earlier round committed.
type crCall func() mockmodel.Scenario

// start saves the Definition allowing every Tool of the fixture plus extraTools, creates the
// Run, claims the Agent NodeRun and scripts the calls; the model answers FINAL once they are
// used up. It then executes the first Turn and drains every Turn the loop commits.
func (f *changeRequestFixture) start(t *testing.T, workflowID string, extraTools []string, calls ...crCall) {
	t.Helper()
	tools := append(append([]string{}, wsAllTools...), verifypatch.ToolName, changerequest.ToolName)
	tools = append(tools, extraTools...)
	saved := f.h.saveDefinition(workspaceDefinition(workflowID, f.repo.commit, true, tools...))
	f.run = f.h.createRun(saved.WorkflowID, saved.Version, `{"question":"`+agentQuestion+`"}`)
	f.outcome = f.h.claimAgentNode(f.run.ID)

	var turn atomic.Int32
	f.h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		n := int(turn.Add(1))
		if n > len(calls) {
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal}
		}
		call := calls[n-1]()
		call.Kind = mockmodel.ScenarioToolCall
		return &call
	}
	f.h.execute(f.outcome)
}

func (f *changeRequestFixture) agentRunID(t *testing.T) string {
	t.Helper()
	return factAgentRunID(t, f.h, f.outcome)
}

// deliverVerification delivers the runner's callback for the one verification the stub
// accepted, with the given verdict, and drains the Turns that follow it.
func (f *changeRequestFixture) deliverVerification(t *testing.T, passed bool) {
	t.Helper()
	token, _ := f.stub.dispatched(t)
	delivered, err := f.h.svc.HandleCallback(f.h.ctx, service.HandleCallback{
		Token: token, ExternalTaskID: verifyExternalTaskID,
		Payload: verifySucceededPayload(f.repo.commit, baselineDigest(), "sha256:verified", passed),
	})
	if err != nil || !delivered.Accepted || delivered.Duplicate {
		t.Fatalf("callback = %+v, %v, want accepted", delivered, err)
	}
	if err := f.h.drainTurns(f.h.ctx); err != nil {
		t.Fatalf("drain turns after the verification: %v", err)
	}
}

func fixed(scenario mockmodel.Scenario) crCall { return func() mockmodel.Scenario { return scenario } }

func verifyBaselineCall() crCall {
	return fixed(wsCall(verifypatch.ToolName, map[string]any{"patchDigest": baselineDigest()}))
}

func basisItem(statement string, turnNo int, manifestRef string) map[string]any {
	return map[string]any{"statement": statement, "turnNo": turnNo, "manifestRef": manifestRef}
}

func changeRequestArguments(patchDigest string, basis ...map[string]any) map[string]any {
	return map[string]any{
		"patchDigest": patchDigest, "summary": crSummary, "impact": crImpact, "rollbackPlan": crRollbackPlan,
		"decisionBasis": basis,
	}
}

// instructionsBasis cites the INSTRUCTIONS item of turnNo's Manifest, whose reference is
// the Agent Run id and therefore exists in every Turn's Manifest.
func (f *changeRequestFixture) instructionsBasis(t *testing.T, turnNo int) map[string]any {
	t.Helper()
	return basisItem("The task instructions require the mirrored fixed version.", turnNo, f.agentRunID(t))
}

// verificationResultBasis cites the TOOL_RESULT item of the verification, which enters the
// request of every Turn after the callback resumed the loop.
func (f *changeRequestFixture) verificationResultBasis(t *testing.T, turnNo int) map[string]any {
	t.Helper()
	attempt := agentOnlyToolAttempt(t, f.h, wsActionOfTurn(t, f.h, f.agentRunID(t), 1).ID)
	return basisItem("The verification of the patch passed every check.", turnNo, attempt.ID)
}

func changeRequestsOf(t *testing.T, h *agentHarness, agentRunID string) []domain.ChangeRequest {
	t.Helper()
	var requests []domain.ChangeRequest
	if err := h.uow.WithinReadTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		requests, err = tx.ChangeRequests().ListByAgentRun(ctx, agentRunID, 100)
		return err
	}); err != nil {
		t.Fatalf("list change requests: %v", err)
	}
	return requests
}

// assertReturned proves the Action of Turn turnNo was handed back to the Agent with code
// before any Attempt existed: FAILED with the code, no Attempt, no AGENT_ACTION_STARTED,
// AGENT_ACTION_FAILED with returnedToAgent and the AUTHORIZATION source, a REJECTED tool
// message, no change request row and exactly one new Turn after it.
func (f *changeRequestFixture) assertReturned(t *testing.T, turnNo int, code domain.AgentReturnCode) map[string]any {
	t.Helper()
	agentRunID := f.agentRunID(t)
	action := wsActionOfTurn(t, f.h, agentRunID, turnNo)
	if action.Status != domain.AgentActionFailed || action.Error == nil || action.Error.Code != string(code) {
		t.Fatalf("action of turn %d = status %s error %+v, want FAILED with %s", turnNo, action.Status, action.Error, code)
	}
	if attempts := agentToolAttempts(f.h.ctx, t, f.h.uow, action.ID); len(attempts) != 0 {
		t.Errorf("tool attempts of the refused call = %+v, want none", attempts)
	}
	if requests := changeRequestsOf(t, f.h, agentRunID); len(requests) != 0 {
		t.Errorf("change requests after the refusal = %+v, want none", requests)
	}
	events := listEvents(f.h.ctx, t, f.h.uow, f.run.ID)
	if started := agentPayloadsFor(t, events, domain.EventAgentActionStarted, "actionId", action.ID); len(started) != 0 {
		t.Errorf("AGENT_ACTION_STARTED for the refused call = %v, want none", started)
	}
	failed := agentOnlyPayloadFor(t, events, domain.EventAgentActionFailed, "actionId", action.ID)
	if failed["returnedToAgent"] != true || failed["failureSource"] != string(domain.FailureAuthorization) {
		t.Errorf("AGENT_ACTION_FAILED = %v, want returnedToAgent with the AUTHORIZATION source", failed)
	}
	message := wsToolMessage(t, f.h, agentRunID, turnNo, action.ID)
	if message["status"] != "REJECTED" || message["code"] != string(code) {
		t.Errorf("tool message = %v, want REJECTED %s", message, code)
	}
	if text, _ := message["message"].(string); strings.Contains(text, crSummary) {
		t.Errorf("tool message repeats the call's arguments: %s", text)
	}
	if !agentTurnNoExists(f.h.ctx, t, f.h.uow, agentRunID, turnNo+1) || agentTurnNoExists(f.h.ctx, t, f.h.uow, agentRunID, turnNo+2) {
		t.Errorf("want exactly one Turn to follow the refusal of turn %d", turnNo)
	}
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, f.outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Errorf("agent termination = %s, want FINAL_RESPONSE after the Agent answered in the next Turn", agentTermination(agentRun))
	}
	return message
}

// ---------------------------------------------------------------------------
// Refusals at claim
// ---------------------------------------------------------------------------

// The baseline is verified, then a write moves the workspace to patch 1; a change request
// naming the verified but superseded baseline digest is STALE_PATCH, not a missing
// verification: the workspace check decides before the fact requirement is consulted.
func TestChangeRequest_StalePatchDigest_ReturnedWithoutAttemptOrRecord(t *testing.T) {
	f := newChangeRequestFixture(t)
	f.start(t, "wf-cr-stale", nil,
		verifyBaselineCall(),
		fixed(wsWrite(wsTokenPath, wsNewToken)),
		func() mockmodel.Scenario {
			return wsCall(changerequest.ToolName, changeRequestArguments(baselineDigest(), f.instructionsBasis(t, 1)))
		},
	)
	f.deliverVerification(t, true)

	message := f.assertReturned(t, 3, domain.ReturnStalePatch)
	latest := wsLatestPatch(t, f.h, f.agentRunID(t))
	if latest == nil || latest.Seq != 1 {
		t.Fatalf("latest patch = %+v, want the write's patch 1", latest)
	}
	if text, _ := message["message"].(string); !strings.Contains(text, baselineDigest()) || !strings.Contains(text, latest.PatchDigest) {
		t.Errorf("tool message %q should name the stale digest and the current one", text)
	}
	if facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, f.agentRunID(t)); len(facts) != 1 {
		t.Errorf("facts = %d, want the one verification of the baseline", len(facts))
	}
}

// Nothing verified the baseline: the call is VERIFICATION_MISSING, naming the fact type.
func TestChangeRequest_NoVerificationFact_ReturnedAsVerificationMissing(t *testing.T) {
	f := newChangeRequestFixture(t)
	f.start(t, "wf-cr-unverified", nil, func() mockmodel.Scenario {
		return wsCall(changerequest.ToolName, changeRequestArguments(baselineDigest(), f.instructionsBasis(t, 1)))
	})

	message := f.assertReturned(t, 1, domain.ReturnVerificationMissing)
	if text, _ := message["message"].(string); !strings.Contains(text, changerequest.RequiredFactType) {
		t.Errorf("tool message %q should name the missing fact type", text)
	}
}

// The baseline was verified and failed: a fact exists, but not with the verdict the Tool
// requires, so the call is still VERIFICATION_MISSING.
func TestChangeRequest_FailedVerification_ReturnedAsVerificationMissing(t *testing.T) {
	f := newChangeRequestFixture(t)
	f.start(t, "wf-cr-failed-verify", nil,
		verifyBaselineCall(),
		func() mockmodel.Scenario {
			return wsCall(changerequest.ToolName, changeRequestArguments(baselineDigest(), f.verificationResultBasis(t, 2)))
		},
	)
	f.deliverVerification(t, false)

	message := f.assertReturned(t, 2, domain.ReturnVerificationMissing)
	if text, _ := message["message"].(string); !strings.Contains(text, changerequest.RequiredFactType) {
		t.Errorf("tool message %q should name the fact type", text)
	}
	facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, f.agentRunID(t))
	if len(facts) != 1 || facts[0].Verdict == nil || *facts[0].Verdict {
		t.Errorf("facts = %+v, want the one failed verification", facts)
	}
}

// ---------------------------------------------------------------------------
// Decision basis against the Context Manifests
// ---------------------------------------------------------------------------

// assertBasisRejected proves the call of Turn turnNo ended the Agent Run as an invalid
// Action in its claim transaction: no Attempt, no record, no new Turn.
func (f *changeRequestFixture) assertBasisRejected(t *testing.T, turnNo int, wantReason string) {
	t.Helper()
	agentRunID := f.agentRunID(t)
	action := wsActionOfTurn(t, f.h, agentRunID, turnNo)
	if action.Status != domain.AgentActionFailed || action.Error == nil || action.Error.Code != "DECISION_BASIS_INVALID" {
		t.Fatalf("action = status %s error %+v, want FAILED with DECISION_BASIS_INVALID", action.Status, action.Error)
	}
	if !strings.Contains(action.Error.Message, wantReason) {
		t.Errorf("action error = %q, want it to mention %q", action.Error.Message, wantReason)
	}
	if attempts := agentToolAttempts(f.h.ctx, t, f.h.uow, action.ID); len(attempts) != 0 {
		t.Errorf("tool attempts = %+v, want none", attempts)
	}
	if requests := changeRequestsOf(t, f.h, agentRunID); len(requests) != 0 {
		t.Errorf("change requests = %+v, want none", requests)
	}
	if agentTurnNoExists(f.h.ctx, t, f.h.uow, agentRunID, turnNo+1) {
		t.Errorf("a Turn follows the rejected call; the Agent Run should have ended")
	}
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, f.outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationInvalidAction {
		t.Errorf("agent termination = %s, want INVALID_ACTION", agentTermination(agentRun))
	}
	if got := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID).Status; got != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", got)
	}
}

func TestChangeRequest_ManifestRefToMissingTurn_Rejected(t *testing.T) {
	f := newChangeRequestFixture(t)
	f.start(t, "wf-cr-basis-turn", nil,
		verifyBaselineCall(),
		func() mockmodel.Scenario {
			return wsCall(changerequest.ToolName, changeRequestArguments(baselineDigest(), f.instructionsBasis(t, 99)))
		},
	)
	f.deliverVerification(t, true)
	f.assertBasisRejected(t, 2, "turn 99")
}

func TestChangeRequest_ManifestRefToMissingItem_Rejected(t *testing.T) {
	f := newChangeRequestFixture(t)
	f.start(t, "wf-cr-basis-item", nil,
		verifyBaselineCall(),
		func() mockmodel.Scenario {
			return wsCall(changerequest.ToolName, changeRequestArguments(baselineDigest(),
				f.verificationResultBasis(t, 2), basisItem("An item nobody saw.", 1, "tool_attempt_never_existed")))
		},
	)
	f.deliverVerification(t, true)
	f.assertBasisRejected(t, 2, "tool_attempt_never_existed")
}

// ---------------------------------------------------------------------------
// Recording, convergence and the content digest
// ---------------------------------------------------------------------------

// recordedRequest returns the one change request of the fixture's Agent Run together with
// the SUCCEEDED Attempt of Turn turnNo that produced it.
func (f *changeRequestFixture) recordedRequest(t *testing.T, turnNo int) (domain.ChangeRequest, domain.ToolAttempt) {
	t.Helper()
	requests := changeRequestsOf(t, f.h, f.agentRunID(t))
	if len(requests) != 1 {
		t.Fatalf("change requests = %+v, want exactly one", requests)
	}
	attempt := agentOnlyToolAttempt(t, f.h, wsActionOfTurn(t, f.h, f.agentRunID(t), turnNo).ID)
	if attempt.Status != domain.ToolAttemptSucceeded {
		t.Fatalf("attempt of turn %d = %+v, want SUCCEEDED", turnNo, attempt)
	}
	return requests[0], attempt
}

// artifactContent reads the content of artifactID back through the Run-scoped artifact row.
func (f *changeRequestFixture) artifactContent(t *testing.T, artifactID string) (store.ArtifactRecord, string) {
	t.Helper()
	var record store.ArtifactRecord
	if err := f.h.uow.WithinReadTx(f.h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		record, err = tx.Artifacts().GetForRun(ctx, f.run.ID, artifactID)
		return err
	}); err != nil {
		t.Fatalf("artifact %s for run %s: %v", artifactID, f.run.ID, err)
	}
	content, err := f.artifacts.Open(f.h.ctx, record.StorageKey)
	if err != nil {
		t.Fatalf("open artifact content: %v", err)
	}
	defer content.Close()
	stored, _ := io.ReadAll(content)
	return record, string(stored)
}

// The happy path: the request commits with its content Artifact, the stored bytes digest to
// content_digest, the row binds the verified digest and the verification fact, the tool
// message names the new record, and the Trace projection lists it without the content.
func TestChangeRequest_ContentDigestMatchesArtifactContent(t *testing.T) {
	f := newChangeRequestFixture(t)
	f.start(t, "wf-cr-record", nil,
		verifyBaselineCall(),
		func() mockmodel.Scenario {
			return wsCall(changerequest.ToolName, changeRequestArguments(baselineDigest(),
				f.verificationResultBasis(t, 2), f.instructionsBasis(t, 1)))
		},
	)
	f.deliverVerification(t, true)
	agentRunID := f.agentRunID(t)

	request, attempt := f.recordedRequest(t, 2)
	facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID)
	if len(facts) != 1 {
		t.Fatalf("facts = %+v, want the one verification", facts)
	}
	if request.AgentRunID != agentRunID || request.PatchDigest != baselineDigest() || request.VerificationFactID != facts[0].ID || request.ToolAttemptID != attempt.ID {
		t.Errorf("change request = %+v, want it bound to the baseline digest, fact %s and attempt %s", request, facts[0].ID, attempt.ID)
	}
	record, content := f.artifactContent(t, request.ContentArtifactID)
	sum := sha256.Sum256([]byte(content))
	if digest := hex.EncodeToString(sum[:]); digest != request.ContentDigest || record.Artifact.SHA256 != request.ContentDigest {
		t.Errorf("stored content digests to %s, artifact row %s, change request %s; want all equal", digest, record.Artifact.SHA256, request.ContentDigest)
	}
	if record.Artifact.MediaType != domain.ArtifactMediaTypeTextPlain {
		t.Errorf("artifact media type = %s, want text", record.Artifact.MediaType)
	}
	verifyAttempt := agentOnlyToolAttempt(t, f.h, wsActionOfTurn(t, f.h, agentRunID, 1).ID)
	for _, want := range []string{"# Change request", crSummary, crImpact, crRollbackPlan, baselineDigest(), facts[0].ID, "turn 2, manifest item `" + verifyAttempt.ID + "`"} {
		if !strings.Contains(content, want) {
			t.Errorf("content lacks %q:\n%s", want, content)
		}
	}
	basis, err := domain.DecodeChangeRequestBasis(request.DecisionBasis)
	if err != nil || len(basis) != 2 || basis[0].TurnNo != 2 || basis[1].ManifestRef != agentRunID {
		t.Errorf("decision basis = %s (%v), want the two cited items", request.DecisionBasis, err)
	}

	message := wsToolMessage(t, f.h, agentRunID, 2, wsActionOfTurn(t, f.h, agentRunID, 2).ID)
	if message["changeRequestId"] != request.ID || message["existing"] != false || message["contentDigest"] != request.ContentDigest {
		t.Errorf("tool message = %v, want it to name change request %s as new", message, request.ID)
	}
	var output map[string]any
	_ = json.Unmarshal(attempt.Result, &output)
	if output["changeRequestId"] != request.ID {
		t.Errorf("attempt result = %s, want it to name change request %s", attempt.Result, request.ID)
	}
	if strings.Contains(string(attempt.Result), crSummary) {
		t.Errorf("attempt result carries the request text: %s", attempt.Result)
	}

	query := service.NewQueryService(service.Deps{UoW: f.h.uow})
	trace, err := query.AgentTrace(f.h.ctx, f.run.ID, f.outcome.NodeRunID)
	if err != nil {
		t.Fatalf("agent trace: %v", err)
	}
	if len(trace.ChangeRequests.Requests) != 1 || trace.ChangeRequests.Requests[0].ID != request.ID || trace.ChangeRequests.Truncated {
		t.Errorf("trace change requests = %+v, want the one record", trace.ChangeRequests)
	}
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, f.outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Errorf("agent termination = %s, want FINAL_RESPONSE", agentTermination(agentRun))
	}
}

// The same request twice: one row, the second call SUCCEEDED with a tool message naming the
// first record as existing, and both Attempts linked to the one content Artifact.
func TestChangeRequest_SameContentTwice_OneRecordAndSecondCallNamesIt(t *testing.T) {
	f := newChangeRequestFixture(t)
	request := func() mockmodel.Scenario {
		return wsCall(changerequest.ToolName, changeRequestArguments(baselineDigest(), f.verificationResultBasis(t, 2)))
	}
	f.start(t, "wf-cr-duplicate", nil, verifyBaselineCall(), request, request)
	f.deliverVerification(t, true)
	agentRunID := f.agentRunID(t)

	first, firstAttempt := f.recordedRequest(t, 2)
	secondAttempt := agentOnlyToolAttempt(t, f.h, wsActionOfTurn(t, f.h, agentRunID, 3).ID)
	if secondAttempt.Status != domain.ToolAttemptSucceeded {
		t.Fatalf("second attempt = %+v, want SUCCEEDED", secondAttempt)
	}
	if first.ToolAttemptID != firstAttempt.ID {
		t.Errorf("the record names attempt %s, want the first call's attempt %s", first.ToolAttemptID, firstAttempt.ID)
	}
	for turnNo, want := range map[int]bool{2: false, 3: true} {
		message := wsToolMessage(t, f.h, agentRunID, turnNo, wsActionOfTurn(t, f.h, agentRunID, turnNo).ID)
		if message["changeRequestId"] != first.ID || message["existing"] != want {
			t.Errorf("tool message of turn %d = %v, want change request %s with existing %t", turnNo, message, first.ID, want)
		}
	}
	var linked int
	if err := f.h.pool.QueryRow(f.h.ctx,
		`SELECT count(*) FROM tool_attempt_artifacts WHERE artifact_id = $1 AND tool_attempt_id = ANY($2)`,
		first.ContentArtifactID, []string{firstAttempt.ID, secondAttempt.ID}).Scan(&linked); err != nil {
		t.Fatalf("count attempt artifact links: %v", err)
	}
	if linked != 2 {
		t.Errorf("attempts linked to the content artifact = %d, want both", linked)
	}
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, f.outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Errorf("agent termination = %s, want FINAL_RESPONSE", agentTermination(agentRun))
	}
}

// tamperedChangeRequest wraps the real executor and flips the content digest the result
// claims, so the result transaction's own check is what stands between a defective executor
// and a recorded request.
type tamperedChangeRequest struct {
	inner registry.ToolExecutor
}

func (d *tamperedChangeRequest) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	got, err := d.inner.Execute(ctx, action)
	if err != nil {
		return got, err
	}
	got.Result.ChangeRequest.ContentDigest = strings.Repeat("0", 64)
	return got, nil
}

func TestChangeRequest_ContentDigestMismatch_ResultTransactionRejects(t *testing.T) {
	stub := newVerifyRunnerStub()
	runner := httptest.NewServer(stub)
	t.Cleanup(runner.Close)
	artifacts := asset.NewArtifactStore(t.TempDir(), 1<<20)
	if err := artifacts.VerifyReadWrite(context.Background()); err != nil {
		t.Fatalf("artifact store: %v", err)
	}
	double := changerequest.Registration(artifacts)
	double.Metadata.Name = crDoubleTool
	double.Executor = &tamperedChangeRequest{inner: changerequest.New(artifacts)}
	repo := newWorkspaceRepo(t)
	h := newWorkspaceHarness(t, workspaceHarnessOptions{repo: repo, extraTools: []registry.ToolRegistration{
		verifypatch.Registration(runner.URL, runner.Client(), artifacts), changerequest.Registration(artifacts), double,
	}})
	f := &changeRequestFixture{h: h, repo: repo, stub: stub, artifacts: artifacts}
	f.start(t, "wf-cr-tampered", []string{crDoubleTool},
		verifyBaselineCall(),
		func() mockmodel.Scenario {
			return wsCall(crDoubleTool, changeRequestArguments(baselineDigest(), f.verificationResultBasis(t, 2)))
		},
	)
	f.deliverVerification(t, true)
	agentRunID := f.agentRunID(t)

	action := wsActionOfTurn(t, f.h, agentRunID, 2)
	if action.Status != domain.AgentActionFailed || action.Error == nil || action.Error.Code != "CHANGE_REQUEST_INVALID" {
		t.Fatalf("action = status %s error %+v, want FAILED with CHANGE_REQUEST_INVALID", action.Status, action.Error)
	}
	if !strings.Contains(action.Error.Message, "digest") {
		t.Errorf("action error = %q, want it to mention the digest", action.Error.Message)
	}
	attempt := agentOnlyToolAttempt(t, f.h, action.ID)
	if attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != "CHANGE_REQUEST_INVALID" || len(attempt.Result) != 0 {
		t.Errorf("attempt = %+v, want FAILED with CHANGE_REQUEST_INVALID and no result", attempt)
	}
	if requests := changeRequestsOf(t, f.h, agentRunID); len(requests) != 0 {
		t.Errorf("change requests = %+v, want none", requests)
	}
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, f.outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationToolError {
		t.Errorf("agent termination = %s, want TOOL_ERROR", agentTermination(agentRun))
	}
	if got := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID).Status; got != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", got)
	}
}
