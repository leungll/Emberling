//go:build integration

// Deploy Tool timeout test: an Agent's deploy call that the production service holds past
// the Agent deadline.
//
// It proves what a Tool-level unit test cannot: the deadline that ends the held call
// terminates the Agent Run TIMEOUT rather than TOOL_ERROR, the uncertain deployment is not
// retried, and the production service, which saw exactly one request, never deploys twice.
//
// It shares the agentHarness and the fixture Definition of agent_loop_test.go.
package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/mockproduction"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/deploy"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
)

const deployToolArguments = `{"target":{"service":"checkout","environment":"prod"},"baseCommit":"abc123","patchDigest":"sha256:feed"}`

// deliveredThenAbandoned is the network between the deploy Tool and the production
// service when the Agent deadline runs out mid-call. The Tool's call context is already
// expired (the injected clock starts a year in the real past, as in agent_timeout_test.go),
// and the standard transport would refuse to send at all. This transport sends the request
// regardless, waits until the production barrier reports it held, and only then honours
// the caller's context: the request has reached production, and the caller stops waiting
// for the answer. It is driven entirely by the held notification, never by elapsed time.
type deliveredThenAbandoned struct {
	base http.RoundTripper
	held <-chan string
}

type roundTripResult struct {
	resp *http.Response
	err  error
}

func (d *deliveredThenAbandoned) RoundTrip(req *http.Request) (*http.Response, error) {
	sendCtx, cancel := context.WithCancel(context.WithoutCancel(req.Context()))
	defer cancel()
	done := make(chan roundTripResult, 1)
	go func() {
		resp, err := d.base.RoundTrip(req.WithContext(sendCtx))
		done <- roundTripResult{resp: resp, err: err}
	}()
	select {
	case <-d.held:
	case r := <-done:
		if r.resp != nil {
			_ = r.resp.Body.Close()
		}
		return nil, errors.New("production answered a deployment it should have held")
	}
	<-req.Context().Done()
	cancel()
	if r := <-done; r.resp != nil {
		_ = r.resp.Body.Close()
	}
	return nil, req.Context().Err()
}

type productionRecordLine struct {
	Event       string `json:"event"`
	Kind        string `json:"kind"`
	OperationID string `json:"operationId"`
	Replayed    bool   `json:"replayed"`
}

func readProductionRecord(t *testing.T, path string) []productionRecordLine {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open production record: %v", err)
	}
	defer func() { _ = file.Close() }()
	var lines []productionRecordLine
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var line productionRecordLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("decode production record line %q: %v", scanner.Text(), err)
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read production record: %v", err)
	}
	return lines
}

// TestDeployTimeout_HeldPastAgentDeadline_TimesOutAndNeverDeploysTwice holds the one
// deploy call of an Agent Run at the production barrier until the Agent deadline has run
// out. The Attempt and Action fail with TIMEOUT, the Agent Run terminates TIMEOUT, the
// Run fails, and the production record shows the single request arriving and then either
// abandoned or deployed once, never a second deployment.
func TestDeployTimeout_HeldPastAgentDeadline_TimesOutAndNeverDeploysTwice(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "production-record.jsonl")
	record, err := mockproduction.OpenRecord(recordPath)
	if err != nil {
		t.Fatalf("open production record: %v", err)
	}
	held := make(chan string, 1)
	production := mockproduction.NewServer(mockproduction.WithTestControls(record), mockproduction.WithHeldNotify(held))
	server := httptest.NewServer(production)
	closed := false
	closeProduction := func() {
		if closed {
			return
		}
		closed = true
		// StopHolding first, so a request still held cannot keep Close waiting.
		production.StopHolding()
		server.Close()
		_ = record.Close()
	}
	t.Cleanup(closeProduction)
	controlPost(t, server.URL+"/control/pause", `{"kinds":["deploy"]}`)

	client := &http.Client{Transport: &deliveredThenAbandoned{base: server.Client().Transport, held: held}}
	h := newAgentHarness(t, agentHarnessOptions{
		Clock: newExecClock(agentPastDeadlineClock),
		Tools: []registry.ToolRegistration{deploy.Registration(server.URL, client)},
	})
	agentScriptToolCallThenFinal(h, mockmodel.Scenario{ToolName: deploy.ToolName, ToolArguments: json.RawMessage(deployToolArguments)})
	def := agentLoopDefinition("wf-deploy-timeout")
	for i := range def.Nodes {
		if def.Nodes[i].ID == "node_agent" {
			def.Nodes[i].Config = json.RawMessage(strings.Replace(string(def.Nodes[i].Config),
				`"allowedTools": ["`+lookup.ToolName+`"]`, `"allowedTools": ["`+deploy.ToolName+`"]`, 1))
		}
	}
	def = h.saveDefinition(def)
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	// The frozen deadline has passed on the injected clock as well, so the timeout
	// transaction commits rather than deciding the Agent Run is still live.
	h.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
	h.execute(outcome)

	// Releasing now lets a request that is somehow still held reach a deployment, which
	// the record assertions below must still accept exactly once. Close waits for the
	// production handler to finish, so every record line has been written.
	controlPost(t, server.URL+"/control/release", `{}`)
	closeProduction()

	action, found := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if !found {
		t.Fatalf("no action was committed for turn %s", outcome.AgentTurnID)
	}
	if action.Status != domain.AgentActionFailed {
		t.Errorf("action status = %s, want FAILED", action.Status)
	}
	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 {
		t.Fatalf("tool attempts = %d, want exactly 1: an uncertain deployment is never retried", len(attempts))
	}
	if attempts[0].Status != domain.ToolAttemptFailed {
		t.Errorf("tool attempt status = %s, want FAILED", attempts[0].Status)
	}
	if attempts[0].Error == nil || attempts[0].Error.Code != "TIMEOUT" {
		t.Errorf("tool attempt error = %+v, want code TIMEOUT", attempts[0].Error)
	}
	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Errorf("agent run termination = %v, want TIMEOUT, never TOOL_ERROR", agentTermination(agentRun))
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	failed := agentOnlyPayloadFor(t, events, domain.EventAgentActionFailed, "actionId", action.ID)
	if failed["failureSource"] != string(domain.FailureTimeout) {
		t.Errorf("AGENT_ACTION_FAILED failureSource = %v, want TIMEOUT", failed["failureSource"])
	}
	if nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID); nodeRun.Status != domain.NodeRunFailed {
		t.Errorf("agent node run status = %s, want FAILED", nodeRun.Status)
	}
	if got := agentRunRow(h.ctx, t, h.uow, run.ID).Status; got != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", got)
	}

	wantOperationID := deploy.OperationID(action.ID, 1)
	counts := map[string]int{}
	for _, line := range readProductionRecord(t, recordPath) {
		if line.Kind != "deploy" {
			continue
		}
		if line.OperationID != wantOperationID {
			t.Errorf("record line %+v names operationId %q, want %q", line, line.OperationID, wantOperationID)
		}
		counts[line.Event]++
		if line.Event == "deployed" && line.Replayed {
			t.Errorf("record shows a replayed deployment; production saw a second request")
		}
	}
	if counts["arrived"] != 1 {
		t.Errorf("arrived lines = %d, want exactly 1", counts["arrived"])
	}
	if counts["deployed"] > 1 {
		t.Errorf("deployed lines = %d, want at most 1", counts["deployed"])
	}
	if counts["abandoned"]+counts["deployed"] != 1 {
		t.Errorf("record = %v, want the request either abandoned or deployed, exactly once", counts)
	}
	if counts["abandoned"] == 1 && counts["released"] != 0 {
		t.Errorf("record = %v, an abandoned request must not also be released", counts)
	}
}

func controlPost(t *testing.T, url, body string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s status = %d, want 200", url, resp.StatusCode)
	}
}
