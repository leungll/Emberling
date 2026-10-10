//go:build integration

// Generation limit tests: an Agent Run may freeze an optional limit on the calls it makes
// to Tools registered as counting toward that limit. The limit is checked in the Action's
// claim transaction, under the Run aggregate lock, after the allowlist, Input Schema and
// fact requirement checks and before any Tool Attempt exists. The calls already made are
// the counting Tools' persisted Attempts of the Agent Run, whatever their outcome. A call
// past the limit fails exactly like an invalid Decision -- GENERATION_LIMIT_REACHED,
// termination INVALID_ACTION, no Attempt and no external dispatch.
//
// They share the agentHarness of agent_loop_test.go.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/mockprovider"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// generationMaxTurns leaves room for every scripted Tool round of these tests.
const generationMaxTurns = 8

// generationProvider is a Mock Provider whose image generations are counted as they
// arrive, so a test can prove that a rejected claim never reached the external system.
type generationProvider struct {
	url         string
	generations atomic.Int32
}

func newGenerationProvider(t *testing.T) *generationProvider {
	t.Helper()
	dispatcher := mockprovider.NewDispatcher(nil)
	handler := mockprovider.NewServer(dispatcher)
	p := &generationProvider{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/assets" {
			p.generations.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		server.Close()
		_ = dispatcher.Shutdown(context.Background())
	})
	p.url = server.URL
	return p
}

// generationExecutor is the real generate_image Executor with a call counter and an
// optional hook that runs before the Provider request.
type generationExecutor struct {
	delegate *generateimage.Executor
	// artifacts is where the delegate saves generated images; artifactRoot is its
	// storage root, for tests that inspect or sweep the saved objects.
	artifacts    *asset.ArtifactStore
	artifactRoot string
	during       func(ctx context.Context, action registry.ToolAction)
	calls        atomic.Int32
}

func (g *generationExecutor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	g.calls.Add(1)
	if g.during != nil {
		g.during(ctx, action)
	}
	return g.delegate.Execute(ctx, action)
}

// newGenerationHarness registers the counting generate_image Tool against p next to the
// non-counting lookup Tool.
func newGenerationHarness(t *testing.T, p *generationProvider, notifier service.EventNotifier) (*agentHarness, *generationExecutor) {
	t.Helper()
	artifactRoot := t.TempDir()
	artifacts := asset.NewArtifactStore(artifactRoot, 1<<20)
	executor := &generationExecutor{
		delegate:     generateimage.New(p.url, &http.Client{Timeout: 5 * time.Second}, artifacts),
		artifacts:    artifacts,
		artifactRoot: artifactRoot,
	}
	registration := generateimage.Registration(p.url, nil, artifacts)
	if !registration.Metadata.CountsTowardGenerationLimit {
		t.Fatalf("generate_image must count toward the generation limit")
	}
	registration.Executor = executor
	h := newAgentHarness(t, agentHarnessOptions{Tools: []registry.ToolRegistration{registration}, Notifier: notifier})
	return h, executor
}

// generationDefinition is the fixture graph whose Agent may call generate_image and
// lookup. A nil limit leaves maxGenerationCalls out of the configuration.
func generationDefinition(workflowID string, limit *int) domain.Definition {
	def := agentLoopDefinitionMaxTurns(workflowID, generationMaxTurns)
	encoded, _ := json.Marshal([]string{generateimage.ToolName, lookup.ToolName})
	for i, node := range def.Nodes {
		if node.ID != "node_agent" {
			continue
		}
		config := strings.Replace(string(node.Config), `["`+lookup.ToolName+`"]`, string(encoded), 1)
		if limit != nil {
			config = strings.Replace(config, `"maxTurns":`, `"maxGenerationCalls": `+strconv.Itoa(*limit)+`, "maxTurns":`, 1)
		}
		def.Nodes[i].Config = json.RawMessage(config)
	}
	return def
}

// generationCall asks for one image generated from photoID.
func generationCall(photoID string) mockmodel.Scenario {
	return mockmodel.Scenario{ToolName: generateimage.ToolName, ToolArguments: json.RawMessage(
		`{"photoAssetId":"` + photoID + `","settings":{"style":"natural"}}`)}
}

func generationLookupCall() mockmodel.Scenario {
	return mockmodel.Scenario{ToolName: lookup.ToolName, ToolArguments: json.RawMessage(agentToolArguments)}
}

// startGenerationRun scripts the TOOL_CALLs, saves the Definition and creates the Run.
func startGenerationRun(h *agentHarness, workflowID string, limit *int, calls ...mockmodel.Scenario) domain.Run {
	h.t.Helper()
	factScriptToolCalls(h, calls...)
	def := h.saveDefinition(generationDefinition(workflowID, limit))
	return h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
}

// generationLimit is a configured maxGenerationCalls value.
func generationLimit(v int) *int { return &v }

// countAgentRunAttempts counts the Tool Attempts of one Agent Run for the given Tools.
func countAgentRunAttempts(t *testing.T, h *agentHarness, agentRunID string, tools ...string) int {
	t.Helper()
	var count int
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		count, err = tx.ToolAttempts().CountByAgentRunForTools(ctx, agentRunID, tools)
		return err
	}); err != nil {
		t.Fatalf("count tool attempts of agent run %s: %v", agentRunID, err)
	}
	return count
}

// assertGenerationLimitRejected proves action was rejected in its claim transaction:
// FAILED with GENERATION_LIMIT_REACHED and exactly {limit, used}, no Tool Attempt and no
// AGENT_ACTION_STARTED for it, and an AGENT_ACTION_FAILED carrying the same code.
func assertGenerationLimitRejected(t *testing.T, h *agentHarness, runID string, action domain.AgentAction, wantLimit, wantUsed int) {
	t.Helper()
	if action.Status != domain.AgentActionFailed || action.CompletedAt == nil {
		t.Errorf("action = status %s completed_at %v, want FAILED with completed_at", action.Status, action.CompletedAt)
	}
	if action.Error == nil || action.Error.Code != runtime.CodeGenerationLimitReached {
		t.Fatalf("action error = %+v, want %s", action.Error, runtime.CodeGenerationLimitReached)
	}
	var details map[string]int
	if err := json.Unmarshal(action.Error.Details, &details); err != nil {
		t.Fatalf("decode error details %s: %v", action.Error.Details, err)
	}
	if len(details) != 2 || details["limit"] != wantLimit || details["used"] != wantUsed {
		t.Errorf("error details = %v, want exactly limit=%d used=%d", details, wantLimit, wantUsed)
	}
	if attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID); len(attempts) != 0 {
		t.Errorf("tool attempts of the rejected action = %+v, want none", attempts)
	}

	events := listEvents(h.ctx, t, h.uow, runID)
	if started := agentPayloadsFor(t, events, domain.EventAgentActionStarted, "actionId", action.ID); len(started) != 0 {
		t.Errorf("AGENT_ACTION_STARTED for the rejected action = %v, want none", started)
	}
	failed := agentOnlyPayloadFor(t, events, domain.EventAgentActionFailed, "actionId", action.ID)
	if failed["failureSource"] != string(domain.FailureSyncExecution) {
		t.Errorf("AGENT_ACTION_FAILED failureSource = %v, want SYNC_EXECUTION", failed["failureSource"])
	}
	if errorObject, _ := failed["error"].(map[string]any); errorObject["code"] != runtime.CodeGenerationLimitReached {
		t.Errorf("AGENT_ACTION_FAILED error = %v, want code %s", failed["error"], runtime.CodeGenerationLimitReached)
	}
}

// assertAgentInvalidAction proves the Agent terminated as INVALID_ACTION exactly once
// and took its NodeRun and Run to FAILED.
func assertAgentInvalidAction(t *testing.T, h *agentHarness, runID string, outcome service.AdvanceOutcome) {
	t.Helper()
	agentRun, ok := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if !ok {
		t.Fatalf("agent run disappeared")
	}
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationInvalidAction {
		t.Errorf("agent run termination = %s, want INVALID_ACTION", agentTermination(agentRun))
	}
	if got := agentNodeRun(h.ctx, t, h.uow, runID).Status; got != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", got)
	}
	if got := agentRunRow(h.ctx, t, h.uow, runID).Status; got != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", got)
	}
	events := listEvents(h.ctx, t, h.uow, runID)
	if terminated := agentLastEventPayload(t, events, domain.EventAgentFailed); terminated["termination"] != string(domain.TerminationInvalidAction) {
		t.Errorf("AGENT_FAILED termination = %v, want INVALID_ACTION", terminated["termination"])
	}
	types := agentEventTypesFor(events, outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentFailed) != 1 || agentCountEventType(types, domain.EventNodeFailed) != 1 {
		t.Errorf("events = %v, want exactly one AGENT_FAILED and one NODE_FAILED", types)
	}
}

// generationActionOfTurn returns the Action of the Agent Run's Turn turnNo.
func generationActionOfTurn(t *testing.T, h *agentHarness, agentRunID string, turnNo int) domain.AgentAction {
	t.Helper()
	turn := agentTurnByNo(h.ctx, t, h.uow, agentRunID, turnNo)
	action, found := agentActionOfTurn(h.ctx, t, h.uow, turn.ID)
	if !found {
		t.Fatalf("no action exists for turn %d", turnNo)
	}
	return action
}

// seedGenerationTurn commits a decided Turn turnNo of the Agent Run whose single Action
// calls generate_image. With failed set it also stands for a call that reached the
// Provider and failed: the Action is claimed and a FAILED generate_image Attempt is
// recorded for it. Otherwise the Action stays READY. The MVP loop never reaches either
// shape naturally -- it runs one Action per Turn and a failed Tool call ends the Agent --
// so the rows are written through the store.
func seedGenerationTurn(t *testing.T, h *agentHarness, agentRunID string, turnNo int, prefix string, failed bool) domain.AgentAction {
	t.Helper()
	now := h.clock.Now()
	turnID, decisionID, actionID := prefix+"-turn", prefix+"-decision", prefix+"-action"
	toolName := generateimage.ToolName
	arguments := json.RawMessage(`{"photoAssetId":"photo-seeded","settings":{"style":"natural"}}`)
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.AgentTurns().Create(ctx, newAgentTurn(turnID, agentRunID, turnNo)); err != nil {
			return err
		}
		if _, err := tx.AgentTurns().ClaimReady(ctx, turnID, now); err != nil {
			return err
		}
		if err := tx.AgentTurns().MarkCompleted(ctx, turnID, now, json.RawMessage(`{"kind":"TOOL_CALL"}`), nil); err != nil {
			return err
		}
		decision := newAgentDecision(decisionID, turnID)
		decision.ToolName = &toolName
		decision.Arguments = arguments
		decision.CreatedAt = now
		if err := tx.AgentDecisions().Create(ctx, decision); err != nil {
			return err
		}
		action := newAgentAction(actionID, turnID, decisionID)
		action.CreatedAt = now
		if err := tx.AgentActions().Create(ctx, action); err != nil {
			return err
		}
		if !failed {
			return nil
		}
		if won, err := tx.AgentActions().ClaimReady(ctx, actionID, now); err != nil || !won {
			return errors.Join(err, errors.New("seeded action was not claimed"))
		}
		attempt := newToolAttempt(prefix+"-attempt", actionID, 1)
		attempt.ToolName = toolName
		attempt.Input = arguments
		attempt.StartedAt = now
		if err := tx.ToolAttempts().Create(ctx, attempt); err != nil {
			return err
		}
		providerErr := domain.ExecutionError{Code: "TOOL_ERROR", Message: "provider rejected the generation"}
		if err := tx.ToolAttempts().MarkFailed(ctx, attempt.ID, domain.ToolAttemptStarted, now, providerErr); err != nil {
			return err
		}
		return tx.AgentActions().MarkFailed(ctx, actionID, domain.AgentActionRunning, now, providerErr)
	}); err != nil {
		t.Fatalf("seed generation turn %d: %v", turnNo, err)
	}
	return getAgentAction(h.ctx, t, h.uow, actionID)
}

// stopAtModelCall stops the in-process chain at the Decision commit of model call n,
// leaving that Turn's Action READY, and returns the claimed Agent NodeRun.
func stopAtModelCall(t *testing.T, h *agentHarness, stop *agentStopNotifier, runID string, n int32) service.AdvanceOutcome {
	t.Helper()
	outcome := h.claimAgentNode(runID)
	stopCtx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	stop.cancel = cancel
	var modelCalls atomic.Int32
	h.provider.BeforeReturn = func(context.Context) {
		if modelCalls.Add(1) == n {
			stop.arm()
		}
	}
	err := h.svc.Execute(stopCtx, outcome)
	if n > 1 {
		if err != nil {
			t.Fatalf("execute agent node run: %v", err)
		}
		err = h.drainTurns(stopCtx)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("agent chain = %v, want context.Canceled at the Decision commit of model call %d", err, n)
	}
	h.provider.BeforeReturn = nil
	return outcome
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestAgentGenerationLimit_ThirdCountedCall_RejectedAtClaim covers the limit itself: with
// a limit of 2 the first two generations run and reach the Provider, and the third is
// rejected in its claim transaction with limit=2 used=2, so exactly two Attempts and two
// Provider generations exist and the Agent terminates as INVALID_ACTION.
func TestAgentGenerationLimit_ThirdCountedCall_RejectedAtClaim(t *testing.T) {
	p := newGenerationProvider(t)
	h, executor := newGenerationHarness(t, p, nil)
	run := startGenerationRun(h, "wf-generation-limit-third", generationLimit(2),
		generationCall("photo-a"), generationCall("photo-b"), generationCall("photo-c"))
	outcome := h.claimAgentNode(run.ID)
	h.execute(outcome)

	agentRunID := factAgentRunID(t, h, outcome)
	for turnNo := 1; turnNo <= 2; turnNo++ {
		if action := generationActionOfTurn(t, h, agentRunID, turnNo); action.Status != domain.AgentActionSucceeded {
			t.Errorf("generation of turn %d = %s, want SUCCEEDED", turnNo, action.Status)
		}
	}
	assertGenerationLimitRejected(t, h, run.ID, generationActionOfTurn(t, h, agentRunID, 3), 2, 2)
	if got := countAgentRunAttempts(t, h, agentRunID, generateimage.ToolName); got != 2 {
		t.Errorf("generate_image attempts = %d, want 2", got)
	}
	if got := executor.calls.Load(); got != 2 {
		t.Errorf("generate_image executor calls = %d, want 2", got)
	}
	if got := p.generations.Load(); got != 2 {
		t.Errorf("mock provider generations = %d, want 2", got)
	}
	if agentTurnNoExists(h.ctx, t, h.uow, agentRunID, 4) {
		t.Errorf("a Turn 4 exists after the rejected generation")
	}
	assertAgentInvalidAction(t, h, run.ID, outcome)
}

// TestAgentGenerationLimit_NonCountingTools_NotLimited covers the counting Tool set:
// lookup does not count toward the limit, so three lookups run under a limit of 2
// without consuming any of it -- the second generation after them still runs -- and only
// a third generation is rejected, with used=2.
func TestAgentGenerationLimit_NonCountingTools_NotLimited(t *testing.T) {
	p := newGenerationProvider(t)
	h, _ := newGenerationHarness(t, p, nil)
	run := startGenerationRun(h, "wf-generation-limit-non-counting", generationLimit(2),
		generationCall("photo-a"), generationLookupCall(), generationLookupCall(), generationLookupCall(),
		generationCall("photo-b"), generationCall("photo-c"))
	outcome := h.claimAgentNode(run.ID)
	h.execute(outcome)

	agentRunID := factAgentRunID(t, h, outcome)
	for turnNo := 1; turnNo <= 5; turnNo++ {
		if action := generationActionOfTurn(t, h, agentRunID, turnNo); action.Status != domain.AgentActionSucceeded {
			t.Errorf("tool call of turn %d = %s error %+v, want SUCCEEDED", turnNo, action.Status, action.Error)
		}
	}
	assertGenerationLimitRejected(t, h, run.ID, generationActionOfTurn(t, h, agentRunID, 6), 2, 2)
	if got := countAgentRunAttempts(t, h, agentRunID, lookup.ToolName); got != 3 {
		t.Errorf("lookup attempts = %d, want 3", got)
	}
	if got := countAgentRunAttempts(t, h, agentRunID, generateimage.ToolName); got != 2 {
		t.Errorf("generate_image attempts = %d, want 2", got)
	}
	if got := p.generations.Load(); got != 2 {
		t.Errorf("mock provider generations = %d, want 2", got)
	}
	assertAgentInvalidAction(t, h, run.ID, outcome)
}

// TestAgentGenerationLimit_FailedAttempt_ConsumesBudget covers what "used" means: every
// persisted Attempt of a counting Tool, whatever its outcome. A generation that reached
// the Provider and failed consumes the only slot of a limit of 1, so the next generation
// is rejected with used=1 and never reaches the Provider.
func TestAgentGenerationLimit_FailedAttempt_ConsumesBudget(t *testing.T) {
	p := newGenerationProvider(t)
	stop := &agentStopNotifier{}
	h, executor := newGenerationHarness(t, p, stop)
	run := startGenerationRun(h, "wf-generation-limit-failed", generationLimit(1), generationCall("photo-a"))
	outcome := stopAtModelCall(t, h, stop, run.ID, 1)
	agentRunID := factAgentRunID(t, h, outcome)
	ready := generationActionOfTurn(t, h, agentRunID, 1)
	if ready.Status != domain.AgentActionReady {
		t.Fatalf("generation action = %s, want READY before the claim", ready.Status)
	}

	seedGenerationTurn(t, h, agentRunID, 2, "gen-limit-failed", true)
	if got := countAgentRunAttempts(t, h, agentRunID, generateimage.ToolName); got != 1 {
		t.Fatalf("generate_image attempts before the claim = %d, want the seeded FAILED one", got)
	}

	if err := h.svc.ExecuteAgentAction(h.ctx, ready.ID, domain.ClaimImmediate); err != nil {
		t.Fatalf("execute generation action: %v", err)
	}
	assertGenerationLimitRejected(t, h, run.ID, getAgentAction(h.ctx, t, h.uow, ready.ID), 1, 1)
	if got := executor.calls.Load(); got != 0 {
		t.Errorf("generate_image executor calls = %d, want 0", got)
	}
	if got := p.generations.Load(); got != 0 {
		t.Errorf("mock provider generations = %d, want 0", got)
	}
	assertAgentInvalidAction(t, h, run.ID, outcome)
}

// TestAgentGenerationLimit_ConcurrentClaimsForLastSlot_ExactlyOneWins covers the race
// for the last slot of a limit of 2. One generation already ran; two READY generation
// Actions are then claimed concurrently. The Run aggregate lock serializes the claims,
// so the first claim creates the second Attempt and the other sees used=2 and is
// rejected. The winner is held inside the Tool call until the loser has returned, so
// the two claims genuinely overlap. Exactly two Attempts and two Provider generations
// exist afterwards.
func TestAgentGenerationLimit_ConcurrentClaimsForLastSlot_ExactlyOneWins(t *testing.T) {
	p := newGenerationProvider(t)
	stop := &agentStopNotifier{}
	h, executor := newGenerationHarness(t, p, stop)
	run := startGenerationRun(h, "wf-generation-limit-race", generationLimit(2),
		generationCall("photo-a"), generationCall("photo-b"))
	outcome := stopAtModelCall(t, h, stop, run.ID, 2)
	agentRunID := factAgentRunID(t, h, outcome)
	if first := generationActionOfTurn(t, h, agentRunID, 1); first.Status != domain.AgentActionSucceeded {
		t.Fatalf("first generation = %s, want SUCCEEDED", first.Status)
	}
	looped := generationActionOfTurn(t, h, agentRunID, 2)
	if looped.Status != domain.AgentActionReady {
		t.Fatalf("second generation = %s, want READY before the race", looped.Status)
	}
	// The seeded Turn takes the last Turn number, clear of the number the loop's own next
	// Turn would take.
	seeded := seedGenerationTurn(t, h, agentRunID, generationMaxTurns, "gen-limit-race", false)

	// Only the first Tool call is held. A second one (there must be none) runs through,
	// so a lost race fails the assertions below instead of deadlocking.
	var heldCalls atomic.Int32
	inFlight := make(chan struct{}, 1)
	release := make(chan struct{})
	executor.during = func(context.Context, registry.ToolAction) {
		if heldCalls.Add(1) == 1 {
			inFlight <- struct{}{}
			<-release
		}
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, actionID := range []string{looped.ID, seeded.ID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- h.svc.ExecuteAgentAction(h.ctx, actionID, domain.ClaimImmediate)
		}()
	}
	close(start)
	<-inFlight
	// The winner is parked in the Tool call, so the first result is the loser's.
	if err := <-errs; err != nil {
		t.Errorf("execute the losing generation action: %v", err)
	}
	close(release)
	wg.Wait()
	if err := <-errs; err != nil {
		t.Errorf("execute the winning generation action: %v", err)
	}

	var winners, losers []domain.AgentAction
	for _, id := range []string{looped.ID, seeded.ID} {
		action := getAgentAction(h.ctx, t, h.uow, id)
		if len(agentToolAttempts(h.ctx, t, h.uow, id)) == 1 {
			winners = append(winners, action)
		} else {
			losers = append(losers, action)
		}
	}
	if len(winners) != 1 || len(losers) != 1 {
		t.Fatalf("winners %d losers %d, want exactly one of each", len(winners), len(losers))
	}
	assertGenerationLimitRejected(t, h, run.ID, losers[0], 2, 2)
	// Only the claims are under test. Two live Tool Actions in one Agent Run exist only
	// through the seeded Turn, and what the winner's result transaction does after the
	// loser has already terminated the Agent is outside the claim-time limit check, so it
	// is logged rather than asserted.
	winnerAttempt := agentOnlyToolAttempt(t, h, winners[0].ID)
	t.Logf("winner after the race: action %s, attempt %s, next turn exists %v, run %s",
		winners[0].Status, winnerAttempt.Status, agentTurnNoExists(h.ctx, t, h.uow, agentRunID, 3),
		agentRunRow(h.ctx, t, h.uow, run.ID).Status)
	if got := countAgentRunAttempts(t, h, agentRunID, generateimage.ToolName); got != 2 {
		t.Errorf("generate_image attempts = %d, want exactly the limit (2)", got)
	}
	if got := executor.calls.Load(); got != 2 {
		t.Errorf("generate_image executor calls = %d, want 2", got)
	}
	if got := p.generations.Load(); got != 2 {
		t.Errorf("mock provider generations = %d, want 2", got)
	}
	assertAgentInvalidAction(t, h, run.ID, outcome)
}

// TestAgentGenerationLimit_FrozenOnAgentRun_NullMeansUnlimited covers the frozen value:
// the Agent Run row carries the configured limit from its creation, and an Agent
// configured without one stores NULL and runs five generations unhindered.
func TestAgentGenerationLimit_FrozenOnAgentRun_NullMeansUnlimited(t *testing.T) {
	t.Run("limited", func(t *testing.T) {
		p := newGenerationProvider(t)
		stop := &agentStopNotifier{}
		h, _ := newGenerationHarness(t, p, stop)
		run := startGenerationRun(h, "wf-generation-limit-frozen", generationLimit(3), generationCall("photo-a"))
		outcome := stopAtModelCall(t, h, stop, run.ID, 1)

		agentRun := getAgentRun(h.ctx, t, h.uow, factAgentRunID(t, h, outcome))
		if agentRun.MaxGenerationCalls == nil || *agentRun.MaxGenerationCalls != 3 {
			t.Errorf("agent run maxGenerationCalls = %v, want 3 frozen at creation", agentRun.MaxGenerationCalls)
		}
	})

	t.Run("unlimited", func(t *testing.T) {
		p := newGenerationProvider(t)
		h, executor := newGenerationHarness(t, p, nil)
		run := startGenerationRun(h, "wf-generation-limit-unlimited", nil,
			generationCall("photo-a"), generationCall("photo-b"), generationCall("photo-c"),
			generationCall("photo-d"), generationCall("photo-e"))
		outcome := h.claimAgentNode(run.ID)
		h.execute(outcome)

		agentRunID := factAgentRunID(t, h, outcome)
		var stored *int
		if err := h.pool.QueryRow(h.ctx,
			`SELECT max_generation_calls FROM agent_runs WHERE id = $1`, agentRunID).Scan(&stored); err != nil {
			t.Fatalf("read max_generation_calls: %v", err)
		}
		if stored != nil {
			t.Errorf("max_generation_calls = %d, want NULL", *stored)
		}
		if got := getAgentRun(h.ctx, t, h.uow, agentRunID).MaxGenerationCalls; got != nil {
			t.Errorf("agent run maxGenerationCalls = %d, want nil", *got)
		}
		for turnNo := 1; turnNo <= 5; turnNo++ {
			if action := generationActionOfTurn(t, h, agentRunID, turnNo); action.Status != domain.AgentActionSucceeded {
				t.Errorf("generation of turn %d = %s error %+v, want SUCCEEDED", turnNo, action.Status, action.Error)
			}
		}
		if got := countAgentRunAttempts(t, h, agentRunID, generateimage.ToolName); got != 5 {
			t.Errorf("generate_image attempts = %d, want 5", got)
		}
		if got := executor.calls.Load(); got != 5 || p.generations.Load() != 5 {
			t.Errorf("executor calls %d, provider generations %d, want 5 each", got, p.generations.Load())
		}
		for _, event := range listEvents(h.ctx, t, h.uow, run.ID) {
			if event.Type == domain.EventAgentActionFailed || event.Type == domain.EventAgentFailed {
				t.Errorf("unexpected %s: %s", event.Type, event.Payload)
			}
		}
	})
}
