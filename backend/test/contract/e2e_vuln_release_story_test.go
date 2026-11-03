//go:build integration

// The release variants of the vulnerability-fix demo over the public API. Each variant
// test plays one story the demo script runs, with the scripted trajectory it binds and
// the fault it injects into the mock production service, and asserts the facts the
// script's story checks read: the release Actions with their approvals, Attempts,
// operations and occupations, the completion verdict, the Agent's report and production's
// own record. This file holds the story harness the variant tests share.
package contract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leungll/Emberling/backend/internal/fixture/mockproduction"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/changerequest"
	"github.com/leungll/Emberling/backend/internal/tools/deployrelease"
	"github.com/leungll/Emberling/backend/internal/tools/verifypatch"
)

// releaseStoryOptions are the release bounds and wiring one story runs with. Zero values
// take the demo's defaults: a long confirm deadline and void grace, so nothing but the
// story's own fault decides what happens, and one re-release.
type releaseStoryOptions struct {
	script    string
	confirmMs int
	graceMs   int
	maxTurns  int
	// client is the HTTP client the release Tool sends with; nil means its default.
	client *http.Client
}

// releaseStory is one story's mock production service, clock and current Backend. The
// production service and the clock outlive a restart of the Backend.
type releaseStory struct {
	f             *vulnFixture
	production    *mockproduction.Server
	productionURL string
	clock         *takeoverClock
	opts          releaseStoryOptions
	env           *testEnv
	operator      *approvalEnv
	rec           *reconciler.Reconciler
	workflowID    string
	version       int
	// processes counts the Backends started, so each has its own process identity.
	processes int
}

func newReleaseStory(t *testing.T, opts releaseStoryOptions) *releaseStory {
	t.Helper()
	if opts.confirmMs == 0 {
		opts.confirmMs = 3600000
	}
	if opts.graceMs == 0 {
		opts.graceMs = 3600000
	}
	record, err := mockproduction.OpenRecord(filepath.Join(t.TempDir(), "production.jsonl"))
	if err != nil {
		t.Fatalf("open mock production record: %v", err)
	}
	// A zero completion delay deploys an accepted release inside the accepting request, so
	// the first poll learns DEPLOYED unless a story pauses completion.
	production := mockproduction.NewServer(mockproduction.WithTestControls(record), mockproduction.WithCompletionDelay(0))
	productionHTTP := httptest.NewServer(production)
	t.Cleanup(func() {
		production.StopHolding()
		productionHTTP.Close()
		_ = record.Close()
	})
	s := &releaseStory{
		f: newVulnFixture(t), production: production, productionURL: productionHTTP.URL,
		clock: &takeoverClock{}, opts: opts,
	}
	s.startBackend(t, nil)
	s.workflowID, s.version = saveVulnDefinitionWith(t, s.env, opts.script, func(config map[string]any) {
		config["allowedTools"] = []string{"read_file", "list_files", "write_file", verifypatch.ToolName, changerequest.ToolName, deployrelease.ToolName}
		workspace, _ := config["workspace"].(map[string]any)
		workspace["deployTarget"] = map[string]any{"service": "token-service", "environment": "staging"}
		config["approvalTimeoutMs"] = 3600000
		config["releaseConfirmTimeoutMs"] = opts.confirmMs
		config["voidGraceMs"] = opts.graceMs
		config["maxReleaseRetries"] = 1
		config["humanWaitTimeoutMs"] = 3600000
		if opts.maxTurns > 0 {
			config["maxTurns"] = opts.maxTurns
		}
	})
	return s
}

// startBackend starts a Backend over pool (nil provisions a fresh database) against the
// story's production service and clock.
func (s *releaseStory) startBackend(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	s.processes++
	runnerID := fmt.Sprintf("release-story-process-%d", s.processes)
	s.env = s.f.startBackend(t, testEnvOptions{
		Pool:                   pool,
		RunnerID:               runnerID,
		Clock:                  s.clock,
		OperatorToken:          testOperatorToken,
		ExtraToolRegistrations: []registry.ToolRegistration{deployrelease.Registration(s.productionURL, s.opts.client)},
	})
	s.operator = &approvalEnv{testEnv: s.env}
	s.rec = reconciler.New(reconciler.Config{UoW: s.env.uow, Executor: s.env.execution, Clock: s.clock, BatchLimit: 100, RunnerID: runnerID})
}

// restart stops the current Backend, the way SIGKILL ends the process, and starts another
// over the same database and volumes under a new process identity.
func (s *releaseStory) restart(t *testing.T) {
	t.Helper()
	pool := s.env.pool
	s.env.stop()
	s.startBackend(t, pool)
}

// createRun starts a Run of the story's Definition over the repair task with goal.
func (s *releaseStory) createRun(t *testing.T, goal string) string {
	t.Helper()
	task := map[string]any{"advisory": vulnAdvisory, "goal": goal, "service": "token-service"}
	resp, body := s.env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": s.workflowID, "definitionVersion": s.version, "input": map[string]any{"task": task},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, body=%s", resp.StatusCode, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)
	return runID
}

// decide posts an operator decision and returns the HTTP status and the decoded body.
func (s *releaseStory) decide(t *testing.T, approvalID, decision, reason string) (int, map[string]any) {
	t.Helper()
	body := map[string]any{"decision": decision}
	if reason != "" {
		body["reason"] = reason
	}
	resp, raw := s.operator.doOperator(t, testOperatorToken, http.MethodPost, "/api/approvals/"+approvalID+"/decision", body)
	return resp.StatusCode, decodeBody[map[string]any](t, raw)
}

// approval reads one approval through the operator API.
func (s *releaseStory) approval(t *testing.T, approvalID string) map[string]any {
	t.Helper()
	resp, raw := s.operator.doOperator(t, testOperatorToken, http.MethodGet, "/api/approvals/"+approvalID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET approval %s status = %d, body=%s", approvalID, resp.StatusCode, raw)
	}
	return decodeBody[map[string]any](t, raw)
}

// approvalsOfRun lists every approval of runID, whatever its status.
func (s *releaseStory) approvalsOfRun(t *testing.T, runID string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, status := range []string{"PENDING", "APPROVED", "REJECTED", "EXPIRED"} {
		resp, raw := s.operator.doOperator(t, testOperatorToken, http.MethodGet, "/api/approvals?status="+status, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/approvals?status=%s status = %d, body=%s", status, resp.StatusCode, raw)
		}
		items, _ := decodeBody[map[string]any](t, raw)["items"].([]any)
		for _, item := range items {
			if approval, _ := item.(map[string]any); approval["runId"] == runID {
				out = append(out, approval)
			}
		}
	}
	return out
}

// drive runs reconciler passes, the poll and void work a running process's Reconciler
// ticker would do, each after advancing the clock by step, until every Run is terminal.
// The short pause between passes gives the in-process workers the Turns a pass made ready;
// it is a bounded wait for their committed facts, not a race the test depends on.
func (s *releaseStory) drive(t *testing.T, step time.Duration, runIDs ...string) {
	t.Helper()
	for pass := 1; !s.env.allTerminal(t, runIDs); pass++ {
		if pass > 600 {
			t.Fatalf("runs %v did not end after %d reconciler passes\n%s", runIDs, pass-1, runStateDump(t, s.env, runIDs[0]))
		}
		s.clock.advance(step)
		if report, err := s.rec.RunOnce(context.Background()); err != nil || len(report.Errors) != 0 {
			t.Fatalf("reconciler pass %d: err=%v errors=%v", pass, err, report.Errors)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// releaseStoryTurn is one Turn of the Agent Trace as the release stories read it.
type releaseStoryTurn struct {
	TurnNo   int `json:"turnNo"`
	Decision *struct {
		Kind string  `json:"kind"`
		Tool *string `json:"tool"`
	} `json:"decision"`
	Action *struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		FailureSource *string `json:"failureSource"`
		Approval      *struct {
			ID        string  `json:"id"`
			Status    string  `json:"status"`
			DecidedBy *string `json:"decidedBy"`
			Reason    *string `json:"reason"`
		} `json:"approval"`
		Release *struct {
			Operations []struct {
				OperationKey string `json:"operationKey"`
				Outcome      string `json:"outcome"`
				Status       string `json:"status"`
			} `json:"operations"`
			Occupation *struct {
				Status        string  `json:"status"`
				ReleaseReason *string `json:"releaseReason"`
				OperationKey  string  `json:"operationKey"`
				TargetKey     string  `json:"targetKey"`
			} `json:"occupation"`
			HumanWait *struct {
				Status string `json:"status"`
			} `json:"humanWait"`
		} `json:"release"`
	} `json:"action"`
	ToolAttempts []struct {
		ID        string `json:"id"`
		ToolName  string `json:"toolName"`
		AttemptNo int    `json:"attemptNo"`
		Status    string `json:"status"`
		Error     *struct {
			Code string `json:"code"`
		} `json:"error"`
		CallbackBinding *struct {
			ProviderID     string `json:"providerId"`
			ExternalTaskID string `json:"externalTaskId"`
		} `json:"callbackBinding"`
	} `json:"toolAttempts"`
}

// releaseStoryTrace is the Agent Trace slice the release stories read besides vulnTrace:
// the Agent Run's clock and every Turn with its approval and release view.
type releaseStoryTrace struct {
	AgentRun struct {
		Termination *string    `json:"termination"`
		Deadline    time.Time  `json:"deadline"`
		PausedAt    *time.Time `json:"pausedAt"`
		PauseReason *string    `json:"pauseReason"`
	} `json:"agentRun"`
	Turns []releaseStoryTurn `json:"turns"`
}

// releases returns the Turns whose Decision asked for a release, in Turn order.
func (tr releaseStoryTrace) releases() []releaseStoryTurn {
	var out []releaseStoryTurn
	for _, turn := range tr.Turns {
		if turn.Action != nil && turn.Decision != nil && turn.Decision.Tool != nil && *turn.Decision.Tool == deployrelease.ToolName {
			out = append(out, turn)
		}
	}
	return out
}

// traces reads the Agent Trace of runID in both shapes.
func (s *releaseStory) traces(t *testing.T, runID string) (vulnTrace, releaseStoryTrace) {
	t.Helper()
	agentNodeRun := nodeRunIDOf(t, s.env.runSnapshot(t, runID), "node_agent")
	status, raw := getAgentTrace(t, s.env, runID, agentNodeRun)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, raw)
	}
	return loadVulnTrace(t, s.env, runID, agentNodeRun), decodeBody[releaseStoryTrace](t, raw)
}

// agentReport returns the Agent's FINAL report the result node received inside the
// completion verdict, and that verdict's outcome.
func agentReport(t *testing.T, snapshot map[string]any) (string, map[string]any) {
	t.Helper()
	text, _ := nodeRunOutputOf(t, snapshot, "node_result")["text"].(string)
	if text == "" {
		// A FAILED Agent NodeRun publishes nothing downstream; its verdict is in the Trace.
		return "", nil
	}
	var result vulnResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("result %q is not JSON: %v", text, err)
	}
	var reportText string
	if err := json.Unmarshal(result.AgentReport, &reportText); err != nil {
		t.Fatalf("agentReport %s is not the Agent's text answer: %v", result.AgentReport, err)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(reportText), &report); err != nil {
		t.Fatalf("agent report %q is not JSON: %v", reportText, err)
	}
	return result.Outcome, report
}

// productionCount counts production's record lines of event about operationID.
func productionCount(lines []map[string]any, event, operationID string) int {
	n := 0
	for _, line := range recordLines(lines, event, "release") {
		if line["operationId"] == operationID {
			n++
		}
	}
	return n
}

// deployedUnderAction counts production's deployed lines under any operation of actionID.
func deployedUnderAction(lines []map[string]any, actionID string) int {
	n := 0
	for _, line := range recordLines(lines, "deployed", "release") {
		if op, _ := line["operationId"].(string); strings.HasPrefix(op, "op_"+actionID+"_") {
			n++
		}
	}
	return n
}

// eventPayloads returns the payloads of every event of typ.
func eventPayloads(events []map[string]any, typ string) []map[string]any {
	var out []map[string]any
	for _, event := range eventsOfType(events, typ) {
		payload, _ := event["payload"].(map[string]any)
		out = append(out, payload)
	}
	return out
}

// strOf dereferences an optional string for comparison and messages.
func strOf(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
