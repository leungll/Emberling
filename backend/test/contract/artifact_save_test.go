//go:build integration

package contract

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
)

// TestArtifactSave_GenerateImage_CommitsArtifactRowsWithoutExposingStorageKeys runs the
// generate-review-generate Agent against a real Mock Provider and proves each generated
// image became an Execution Artifact: the Attempt result carries its reference, the row
// exists, and its storage key appears in no API response and no Event payload.
func TestArtifactSave_GenerateImage_CommitsArtifactRowsWithoutExposingStorageKeys(t *testing.T) {
	ctx := context.Background()
	env := newTraceFactsEnv(t)
	scriptGenerateReviewGenerate(env)
	workflowID, version := saveTraceFactsDefinition(t, env, nil)
	runID := createAgentRun(t, env, workflowID, version, "make two images")
	snapshot := env.waitForTerminal(t, runID, agentTraceWait)
	run, _ := snapshot["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", status, snapshot)
	}
	status, traceBody := getAgentTrace(t, env, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, traceBody)
	}
	trace := decodeBody[traceFactsResponse](t, traceBody)
	_, snapshotBody := env.doJSON(t, http.MethodGet, "/api/runs/"+runID, nil)

	var generated []domain.ArtifactRef
	var storageKeys []string
	var eventPayloads strings.Builder
	if err := env.uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		for _, turn := range trace.Turns {
			for _, ref := range turn.ToolAttempts {
				if ref.ToolName != generateimage.ToolName {
					continue
				}
				attempt, err := tx.ToolAttempts().Get(ctx, ref.ID)
				if err != nil {
					return err
				}
				var result struct {
					Artifact domain.ArtifactRef `json:"artifact"`
				}
				if err := json.Unmarshal(attempt.Result, &result); err != nil {
					return err
				}
				record, err := tx.Artifacts().Get(ctx, result.Artifact.ArtifactID)
				if err != nil {
					return err
				}
				if record.Artifact.Ref() != result.Artifact {
					t.Errorf("artifact row %+v does not match the Attempt's reference %+v", record.Artifact, result.Artifact)
				}
				generated = append(generated, result.Artifact)
				storageKeys = append(storageKeys, record.StorageKey)
			}
		}
		events, err := tx.Events().ListAfter(ctx, runID, 0, 1000)
		if err != nil {
			return err
		}
		for _, ev := range events {
			eventPayloads.Write(ev.Payload)
		}
		return nil
	}); err != nil {
		t.Fatalf("read committed facts: %v", err)
	}

	if len(generated) != 2 {
		t.Fatalf("generate_image Attempts with an artifact = %d, want 2", len(generated))
	}
	for _, ref := range generated {
		if err := ref.Validate(); err != nil || ref.MediaType != "image/png" {
			t.Errorf("artifact %+v: want a valid png reference (%v)", ref, err)
		}
	}
	for _, key := range storageKeys {
		for name, body := range map[string]string{
			"agent trace": string(traceBody), "snapshot": string(snapshotBody), "events": eventPayloads.String(),
		} {
			if strings.Contains(body, key) || strings.Contains(body, "artifacts/") {
				t.Errorf("%s exposes an artifact storage key", name)
			}
		}
	}
}
