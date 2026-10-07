//go:build integration

// Run input AssetRef checking is a transactional guarantee, not a pure decision: POST
// /api/runs resolves each Image Input's AssetRef against the committed `assets` rows from
// inside the same transaction that would have created the Run. Only PostgreSQL can show
// that a rejected reference leaves no Run behind, which is why this test lives here rather
// than beside the pure runtime.RunInputAssetRefs unit tests.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/imageinput"
	"github.com/leungll/Emberling/backend/internal/nodes/mediaoutput"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
)

// execAssetHarness is newExecHarness with the two media Node Types this scenario needs
// registered on top of its four text ones. The harness exposes its Node Registry, so the
// extra registrations stay local to this file instead of widening every other test's
// Registry.
func execAssetHarness(t *testing.T) *execHarness {
	t.Helper()
	h := newExecHarness(t)
	for _, reg := range []registry.NodeRegistration{
		imageinput.Registration(),
		mediaoutput.Registration(),
	} {
		if err := h.nodes.Register(reg); err != nil {
			t.Fatalf("register node type: %v", err)
		}
	}
	return h
}

// execMediaDefinition is the smallest Definition carrying an Image Input:
//
//	caption ─▶ output.caption
//	reference ─▶ output.image
func execMediaDefinition(workflowID string) domain.Definition {
	return domain.Definition{
		WorkflowID:  workflowID,
		Version:     1,
		Name:        "Reference Image Input",
		Description: "text_input + image_input -> media_output",
		Nodes: []domain.Node{
			{ID: "caption", Type: "text_input", Name: "Caption",
				Config: json.RawMessage(`{"inputKey":"caption","required":true}`)},
			{ID: "reference", Type: "image_input", Name: "Reference",
				Config: json.RawMessage(`{"inputKey":"reference","required":true,"acceptedMediaTypes":["image/png"],"maxSizeBytes":5242880}`)},
			{ID: "output", Type: "media_output", Name: "Output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "caption", SourceHandle: "text", Target: "output", TargetHandle: "caption"},
			{ID: "e2", Source: "reference", SourceHandle: "image", Target: "output", TargetHandle: "image"},
		},
		CreatedAt: fixtureTime,
	}
}

// execCommitAsset inserts one Asset row directly through the Store. The binary itself is
// irrelevant here: CreateRun reads Metadata only, and never touches storage.
func execCommitAsset(t *testing.T, h *execHarness, asset domain.Asset) domain.AssetRef {
	t.Helper()
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Assets().Create(ctx, store.AssetRecord{Asset: asset, StorageKey: "ab/" + asset.AssetID})
	}); err != nil {
		t.Fatalf("commit asset %s: %v", asset.AssetID, err)
	}
	return asset.Ref()
}

// execRunInput renders one Run input carrying ref as the Image Input's AssetRef.
func execRunInput(t *testing.T, ref domain.AssetRef) json.RawMessage {
	t.Helper()
	input, err := json.Marshal(map[string]any{"caption": "a small ember creature", "reference": ref})
	if err != nil {
		t.Fatalf("marshal run input: %v", err)
	}
	return input
}

// TestCreateRun_AssetCheck_RunNotPersistedOnMismatch: an AssetRef whose digest disagrees
// with the committed Metadata describes content the Asset never held. The check runs inside
// CreateRun's transaction, before the first Run, NodeRun or Event insert, so the rejection
// must leave the database exactly as it was -- not a RUNNING Run that would later fail at
// image_input execution time.
//
// The same Definition and the same Asset are then re-used with the Metadata's own AssetRef
// to show the rollback is caused by the mismatch alone.
func TestCreateRun_AssetCheck_RunNotPersistedOnMismatch(t *testing.T) {
	h := execAssetHarness(t)
	h.saveDefinition(execMediaDefinition("wf_asset_mismatch"))
	ref := execCommitAsset(t, h, domain.Asset{
		AssetID:   "asset_reference_image",
		MediaType: "image/png",
		SizeBytes: 320,
		SHA256:    strings.Repeat("1", 64),
		CreatedAt: fixtureTime,
	})

	mismatched := ref
	mismatched.SHA256 = strings.Repeat("2", 64)
	_, err := h.svc.CreateRun(h.ctx, service.CreateRun{
		WorkflowID: "wf_asset_mismatch", DefinitionVersion: 1, Input: execRunInput(t, mismatched),
	})
	if err == nil {
		t.Fatal("create run with an AssetRef whose sha256 disagrees with the committed Metadata: want an error, got nil")
	}
	var invalidErr *service.RunInputInvalidError
	if !errors.As(err, &invalidErr) {
		t.Fatalf("create run error: want *service.RunInputInvalidError, got %T: %v", err, err)
	}
	if len(invalidErr.Errors) != 1 || invalidErr.Errors[0].NodeID != "reference" {
		t.Fatalf("validation errors: want exactly one naming node %q, got %+v", "reference", invalidErr.Errors)
	}
	if msg := invalidErr.Errors[0].Message; !strings.Contains(msg, ref.AssetID) || strings.Contains(msg, "ab/") {
		t.Errorf("validation message = %q, want it to name the assetId and no storage key", msg)
	}

	if latest := execLatestRun(t, h, "wf_asset_mismatch"); latest != nil {
		t.Fatalf("latest run after the rejected CreateRun: want none, got %+v", latest)
	}

	// The check bites on the mismatch only: the committed Metadata's own AssetRef is
	// accepted by the very same transaction path.
	run, err := h.svc.CreateRun(h.ctx, service.CreateRun{
		WorkflowID: "wf_asset_mismatch", DefinitionVersion: 1, Input: execRunInput(t, ref),
	})
	if err != nil {
		t.Fatalf("create run with the committed AssetRef: %v", err)
	}
	if run.ID == "" || run.Status != domain.RunRunning {
		t.Fatalf("created run: want a RUNNING Run, got %+v", run)
	}
}

// TestCreateRun_AssetCheck_UnknownAssetNotPersisted: a schema-valid AssetRef naming an
// Asset that was never uploaded is refused the same way, so a Run can never reference
// content PostgreSQL has no row for.
func TestCreateRun_AssetCheck_UnknownAssetNotPersisted(t *testing.T) {
	h := execAssetHarness(t)
	h.saveDefinition(execMediaDefinition("wf_asset_unknown"))

	_, err := h.svc.CreateRun(h.ctx, service.CreateRun{
		WorkflowID: "wf_asset_unknown", DefinitionVersion: 1,
		Input: execRunInput(t, domain.AssetRef{
			AssetID:   "asset_never_uploaded",
			MediaType: "image/png",
			SizeBytes: 320,
			SHA256:    strings.Repeat("3", 64),
		}),
	})
	var invalidErr *service.RunInputInvalidError
	if !errors.As(err, &invalidErr) {
		t.Fatalf("create run error: want *service.RunInputInvalidError, got %T: %v", err, err)
	}
	if latest := execLatestRun(t, h, "wf_asset_unknown"); latest != nil {
		t.Fatalf("latest run after the rejected CreateRun: want none, got %+v", latest)
	}
}

func execLatestRun(t *testing.T, h *execHarness, workflowID string) *domain.Run {
	t.Helper()
	var latest *domain.Run
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		latest, err = tx.Runs().LatestByWorkflow(ctx, workflowID)
		return err
	}); err != nil {
		t.Fatalf("query latest run: %v", err)
	}
	return latest
}
