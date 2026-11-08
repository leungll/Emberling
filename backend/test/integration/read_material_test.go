//go:build integration

// read_material tests over real PostgreSQL: the Tool reads a transcript the media
// transcript node stored, through the same Run-scoped artifact read the process wires,
// and the Agent's next Turn records the range it read in its Context Manifest.
package integration

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
	"github.com/leungll/Emberling/backend/internal/tools/readmaterial"
)

// transcriptSecondLine is the one line the Agent reads. The first and third lines never
// leave the artifact, so their text is the marker that nothing else carried the material.
const transcriptSecondLine = "S2\t2.500\t6.000\tToday we talk about embers.\n"

// lateMaterials is the material port bound to the Artifact service after the harness
// exists, as the process binds it; the Tool only ever reads through OpenRunContent.
type lateMaterials struct {
	svc *service.ArtifactService
}

func (m *lateMaterials) OpenText(ctx context.Context, runID, artifactID string) (domain.Artifact, io.ReadCloser, error) {
	return m.svc.OpenRunContent(ctx, runID, artifactID)
}

// newReadMaterialEnv is the transcript environment with read_material registered over
// the real artifact read, and the Agent allowed to call it beside lookup.
func newReadMaterialEnv(t *testing.T) (*transcriptEnv, *lateMaterials) {
	t.Helper()
	materials := &lateMaterials{}
	env := newTranscriptEnv(t, readmaterial.Registration(readmaterial.Ports{Materials: materials}))
	materials.svc = service.NewArtifactService(service.Deps{UoW: env.uow, ArtifactContent: env.artifacts})
	return env, materials
}

// allowReadMaterial lets the Definition's Agent call read_material beside lookup.
func allowReadMaterial(def domain.Definition) domain.Definition {
	for i, node := range def.Nodes {
		if node.ID == "node_agent" {
			def.Nodes[i].Config = json.RawMessage(strings.Replace(string(node.Config),
				`["`+lookup.ToolName+`"]`, `["`+lookup.ToolName+`","`+readmaterial.ToolName+`"]`, 1))
		}
	}
	return def
}

// scriptReadThenFinal makes every Agent read the given range of artifactID on its first
// Turn and finish on its second.
func scriptReadThenFinal(env *transcriptEnv, artifactID string, startLine, lineCount int) {
	env.provider.Script = func(request registry.ModelRequest) *mockmodel.Scenario {
		if len(request.Messages) == 1 {
			arguments, _ := json.Marshal(map[string]any{"artifactId": artifactID, "startLine": startLine, "lineCount": lineCount})
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioToolCall, ToolName: readmaterial.ToolName, ToolArguments: arguments}
		}
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: "cut"}
	}
}

// toolAttemptsOfTurn lists the Tool Attempts of a Turn's Action.
func toolAttemptsOfTurn(ctx context.Context, t *testing.T, uow store.UnitOfWork, turnID string) []domain.ToolAttempt {
	t.Helper()
	var attempts []domain.ToolAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		action, err := tx.AgentActions().GetByTurnID(ctx, turnID)
		if err != nil {
			return err
		}
		attempts, err = tx.ToolAttempts().ListByActionID(ctx, action.ID)
		return err
	}); err != nil {
		t.Fatalf("list Tool Attempts of Turn %s: %v", turnID, err)
	}
	return attempts
}

// TestReadMaterial_AgentReadsStoredTranscript_ManifestRecordsRangeAndNothingElseCarriesIt:
// the Agent reads the second line of the transcript its Run stored; the read's committed
// result carries that line and the range; the second Turn's Manifest records one
// MATERIAL_RANGE item whose digest is that of the text the request carried; and the lines
// the Agent never read reach no output, Attempt, Event or model request.
func TestReadMaterial_AgentReadsStoredTranscript_ManifestRecordsRangeAndNothingElseCarriesIt(t *testing.T) {
	env, _ := newReadMaterialEnv(t)
	wantSHA := sha256Hex([]byte(transcriptMaterial))
	artifactID, err := domain.ArtifactIDForSHA256(wantSHA)
	if err != nil {
		t.Fatal(err)
	}
	scriptReadThenFinal(env, artifactID, 2, 1)
	def := env.saveDefinition(allowReadMaterial(transcriptDefinition("wf-read-material")))
	d := env.dispatchRun(t, env.createRun(def.WorkflowID, def.Version, transcriptTaskInput))
	env.callback(t, d, succeededCallback(nil))
	if run := env.finish(t, d.run.ID); run.Status != domain.RunCompleted {
		t.Fatalf("Run status = %s, want COMPLETED", run.Status)
	}

	agentRun := getAgentRunByNodeRun(env.ctx, t, env.uow, agentNodeRun(env.ctx, t, env.uow, d.run.ID).ID)
	turns := listTurnsOfAgentRun(env.ctx, t, env.uow, agentRun.ID)
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want a read Turn and a FINAL Turn", len(turns))
	}
	attempts := toolAttemptsOfTurn(env.ctx, t, env.uow, turns[0].ID)
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptSucceeded {
		t.Fatalf("read Attempts = %+v, want one SUCCEEDED", attempts)
	}
	var result struct {
		Material struct {
			ArtifactID string `json:"artifactId"`
			SHA256     string `json:"sha256"`
			StartLine  int    `json:"startLine"`
			LineCount  int    `json:"lineCount"`
			TotalLines int    `json:"totalLines"`
		} `json:"material"`
		Text      string `json:"text"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(attempts[0].Result, &result); err != nil {
		t.Fatalf("decode read result %s: %v", attempts[0].Result, err)
	}
	if result.Material.ArtifactID != artifactID || result.Material.SHA256 != wantSHA || result.Material.StartLine != 2 ||
		result.Material.LineCount != 1 || result.Material.TotalLines != 3 || result.Text != transcriptSecondLine || result.Truncated {
		t.Fatalf("read result = %+v, want line 2 of 3 of %s", result, artifactID)
	}

	manifest, err := getManifest(env.ctx, t, env.uow, turns[1].ID)
	if err != nil {
		t.Fatalf("second Turn Manifest: %v", err)
	}
	ranges := manifestItemsOfKind(manifest.Items, domain.ManifestMaterialRange)
	if len(ranges) != 1 {
		t.Fatalf("second Turn MATERIAL_RANGE items = %+v, want one", ranges)
	}
	got := ranges[0]
	if got.Ref != artifactID || got.Version != wantSHA[:12] || !got.Included || got.OmittedReason != nil ||
		got.SHA256 != sha256Hex([]byte(transcriptSecondLine)) || got.Length != len(transcriptSecondLine) ||
		got.Range == nil || *got.Range != (domain.ManifestRange{StartLine: 2, LineCount: 1, TotalLines: 3}) {
		t.Fatalf("MATERIAL_RANGE item = %+v (range %+v), want line 2 of 3 of %s digesting the text the request carried", got, got.Range, artifactID)
	}
	if first, err := getManifest(env.ctx, t, env.uow, turns[0].ID); err != nil {
		t.Fatalf("first Turn Manifest: %v", err)
	} else if items := manifestItemsOfKind(first.Items, domain.ManifestMaterialRange); len(items) != 0 {
		t.Fatalf("first Turn carries a MATERIAL_RANGE before anything was read: %+v", items)
	}
	// The second request carried exactly the text the Manifest digested, and no other
	// line of the transcript reached any output, Attempt, Event or request.
	requests := env.provider.Requests()
	if len(requests) != 2 || !strings.Contains(string(requests[1].Messages[len(requests[1].Messages)-1].Content), "Today we talk about embers.") {
		t.Fatalf("the second model request does not carry the line the Agent read; requests=%d", len(requests))
	}
	env.assertNoMaterial(t, d.run.ID)
	for _, event := range listEvents(env.ctx, t, env.uow, d.run.ID) {
		if strings.Contains(string(event.Payload), "Thank you for watching") || strings.Contains(string(event.Payload), "Today we talk") {
			t.Errorf("event %s %s carries transcript text", event.ID, event.Type)
		}
	}
}

// TestReadMaterial_OtherRunsArtifact_NotFoundResultAndNoMaterialRange: an Agent of a Run
// that never transcribed asks for the artifact another Run stored. The read is a
// successful NOT_FOUND result, so the Agent goes on and the Run completes; its Manifest
// records a plain tool result and no range; and no transcript text reaches that Run.
func TestReadMaterial_OtherRunsArtifact_NotFoundResultAndNoMaterialRange(t *testing.T) {
	env, _ := newReadMaterialEnv(t)
	artifactID, err := domain.ArtifactIDForSHA256(sha256Hex([]byte(transcriptMaterial)))
	if err != nil {
		t.Fatal(err)
	}
	// The owning Run stores the transcript and finishes without reading it.
	env.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: "cut"}
	}
	owner := env.saveDefinition(transcriptDefinition("wf-read-material-owner"))
	d := env.dispatchRun(t, env.createRun(owner.WorkflowID, owner.Version, transcriptTaskInput))
	env.callback(t, d, succeededCallback(nil))
	if run := env.finish(t, d.run.ID); run.Status != domain.RunCompleted {
		t.Fatalf("owning Run status = %s, want COMPLETED", run.Status)
	}
	if _, err := env.artifactForRun(d.run.ID, artifactID); err != nil {
		t.Fatalf("the owning Run cannot read its transcript: %v", err)
	}

	// Another Run, with no transcript of its own, reads the owner's artifact.
	scriptReadThenFinal(env, artifactID, 1, 3)
	other := env.saveDefinition(allowReadMaterial(agentLoopDefinition("wf-read-material-other")))
	run := env.createRun(other.WorkflowID, other.Version, `{"question":"`+agentQuestion+`"}`)
	env.execute(env.claimAgentNode(run.ID))
	if got := env.finish(t, run.ID); got.Status != domain.RunCompleted {
		t.Fatalf("reading Run status = %s, want COMPLETED after a NOT_FOUND read", got.Status)
	}

	agentRun := getAgentRunByNodeRun(env.ctx, t, env.uow, agentNodeRun(env.ctx, t, env.uow, run.ID).ID)
	turns := listTurnsOfAgentRun(env.ctx, t, env.uow, agentRun.ID)
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want a read Turn and a FINAL Turn", len(turns))
	}
	attempts := toolAttemptsOfTurn(env.ctx, t, env.uow, turns[0].ID)
	var notFound map[string]any
	if len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptSucceeded ||
		json.Unmarshal(attempts[0].Result, &notFound) != nil || len(notFound) != 1 || notFound["status"] != "NOT_FOUND" {
		t.Fatalf("read Attempts = %+v, want one SUCCEEDED {\"status\":\"NOT_FOUND\"} result", attempts)
	}
	manifest, err := getManifest(env.ctx, t, env.uow, turns[1].ID)
	if err != nil {
		t.Fatalf("second Turn Manifest: %v", err)
	}
	if items := manifestItemsOfKind(manifest.Items, domain.ManifestMaterialRange); len(items) != 0 {
		t.Fatalf("MATERIAL_RANGE items = %+v, want none for a NOT_FOUND read", items)
	}
	if items := manifestItemsOfKind(manifest.Items, domain.ManifestToolResult); len(items) != 1 || items[0].Label != readmaterial.ToolName+" result" {
		t.Fatalf("TOOL_RESULT items = %+v, want the read_material result", items)
	}
	env.assertNoMaterial(t, run.ID)
	for _, request := range env.provider.Requests() {
		encoded, _ := json.Marshal(request)
		if strings.Contains(string(encoded), "Today we talk") || strings.Contains(string(encoded), "Thank you for watching") {
			t.Fatalf("a model request carries the other Run's transcript: %s", encoded)
		}
	}
}
