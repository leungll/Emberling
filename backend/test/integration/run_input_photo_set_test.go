//go:build integration

// A Media Brief's photo set is a list of AssetRefs, and every one of them is checked
// against the committed `assets` rows inside the transaction that would create the Run,
// exactly like an Image Input's single AssetRef. Only PostgreSQL can show that one missing
// photo leaves no Run behind.
package integration

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/mediabrief"
	"github.com/leungll/Emberling/backend/internal/service"
)

// execPhotoSetHarness is newExecHarness with the media_brief Node Type registered on top
// of its text ones, kept local to this file.
func execPhotoSetHarness(t *testing.T) *execHarness {
	t.Helper()
	h := newExecHarness(t)
	if err := h.nodes.Register(mediabrief.Registration()); err != nil {
		t.Fatalf("register media_brief: %v", err)
	}
	return h
}

// execPhotoSetDefinition is the smallest Definition carrying a Media Brief:
//
//	caption ─▶ brief.brief ─▶ output.text
func execPhotoSetDefinition(workflowID string) domain.Definition {
	return domain.Definition{
		WorkflowID:  workflowID,
		Version:     1,
		Name:        "Photo Set Media Brief",
		Description: "text_input + media_brief -> text_output",
		Nodes: []domain.Node{
			{ID: "caption", Type: "text_input", Name: "Caption",
				Config: json.RawMessage(`{"inputKey":"caption","required":true}`)},
			{ID: "brief", Type: "media_brief", Name: "Brief",
				Config: json.RawMessage(`{"inputKey":"photos"}`)},
			{ID: "output", Type: "text_output", Name: "Output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "caption", SourceHandle: "text", Target: "brief", TargetHandle: "brief"},
			{ID: "e2", Source: "brief", SourceHandle: "text", Target: "output", TargetHandle: "text"},
		},
		CreatedAt: fixtureTime,
	}
}

func execPhotoSetInput(t *testing.T, photos ...domain.AssetRef) json.RawMessage {
	t.Helper()
	input, err := json.Marshal(map[string]any{"caption": "a spring catalogue shoot", "photos": photos})
	if err != nil {
		t.Fatalf("marshal run input: %v", err)
	}
	return input
}

// TestCreateRun_PhotoSetAssetCheck_OneMissingPhotoNotPersisted: a photo set whose second
// photo names an Asset that was never uploaded is refused with the same validation error
// an Image Input gets, pointing at that photo, and no Run is created. Uploading the
// missing photo then lets the very same input create a RUNNING Run, so the rejection is
// caused by the missing photo alone.
func TestCreateRun_PhotoSetAssetCheck_OneMissingPhotoNotPersisted(t *testing.T) {
	h := execPhotoSetHarness(t)
	h.saveDefinition(execPhotoSetDefinition("wf_photo_set_missing"))
	first := execCommitAsset(t, h, domain.Asset{
		AssetID: "asset_photo_first", MediaType: "image/jpeg", SizeBytes: 410,
		SHA256: strings.Repeat("4", 64), CreatedAt: fixtureTime,
	})
	third := execCommitAsset(t, h, domain.Asset{
		AssetID: "asset_photo_third", MediaType: "image/webp", SizeBytes: 430,
		SHA256: strings.Repeat("6", 64), CreatedAt: fixtureTime,
	})
	missingAsset := domain.Asset{
		AssetID: "asset_photo_second", MediaType: "image/png", SizeBytes: 420,
		SHA256: strings.Repeat("5", 64), CreatedAt: fixtureTime,
	}
	input := execPhotoSetInput(t, first, missingAsset.Ref(), third)

	_, err := h.svc.CreateRun(h.ctx, service.CreateRun{
		WorkflowID: "wf_photo_set_missing", DefinitionVersion: 1, Input: input,
	})
	var invalidErr *service.RunInputInvalidError
	if !errors.As(err, &invalidErr) {
		t.Fatalf("create run error: want *service.RunInputInvalidError, got %T: %v", err, err)
	}
	if len(invalidErr.Errors) != 1 {
		t.Fatalf("validation errors: want exactly one for the missing photo, got %+v", invalidErr.Errors)
	}
	verr := invalidErr.Errors[0]
	if verr.NodeID != "brief" || verr.Path != "input/photos/1" {
		t.Errorf("validation error = %+v, want node %q at path %q", verr, "brief", "input/photos/1")
	}
	if !strings.Contains(verr.Message, missingAsset.AssetID) || strings.Contains(verr.Message, "ab/") {
		t.Errorf("validation message = %q, want it to name the missing assetId and no storage key", verr.Message)
	}
	if latest := execLatestRun(t, h, "wf_photo_set_missing"); latest != nil {
		t.Fatalf("latest run after the rejected CreateRun: want none, got %+v", latest)
	}

	execCommitAsset(t, h, missingAsset)
	run, err := h.svc.CreateRun(h.ctx, service.CreateRun{
		WorkflowID: "wf_photo_set_missing", DefinitionVersion: 1, Input: input,
	})
	if err != nil {
		t.Fatalf("create run once every photo is uploaded: %v", err)
	}
	if run.ID == "" || run.Status != domain.RunRunning {
		t.Fatalf("created run: want a RUNNING Run, got %+v", run)
	}
}
