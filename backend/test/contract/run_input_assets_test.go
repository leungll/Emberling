//go:build integration

// Package contract: this file covers the Run input `AssetRef` rule of
// docs/08-interface-spec.md §3.3 REST API — "未知字段、缺少必填字段、类型错误或无效 `AssetRef`
// 都不能创建 Run". The frozen runInputSchema decides the *shape* of an AssetRef; only a
// lookup against the committed Asset Metadata can decide that the reference is real and
// describes the Asset it names, so POST /api/runs performs that lookup before any Run,
// NodeRun or Event row exists.
package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

// referenceImageGraphRequest is the smallest Definition with an Image Input:
//
//	caption ─▶ output.caption
//	reference ─▶ output.image
//
// It deliberately contains no image_generation node, so these tests exercise the Run
// creation gate alone, with no external dispatch in the picture. The `reference` key is
// required, which is what forces every Run here to carry an AssetRef.
func referenceImageGraphRequest() map[string]any {
	return map[string]any{
		"name":        "Reference Image Input",
		"description": "",
		"nodes": []map[string]any{
			{"id": "caption", "type": "text_input", "config": map[string]any{"inputKey": "caption", "required": true}},
			{"id": "reference", "type": "image_input", "config": map[string]any{
				"inputKey": "reference", "required": true,
				"acceptedMediaTypes": []string{"image/png", "image/jpeg"},
				"maxSizeBytes":       float64(5242880),
			}},
			{"id": "output", "type": "media_output", "config": map[string]any{}},
		},
		"edges": []map[string]any{
			{"id": "e1", "source": "caption", "sourceHandle": "text", "target": "output", "targetHandle": "caption"},
			{"id": "e2", "source": "reference", "sourceHandle": "image", "target": "output", "targetHandle": "image"},
		},
	}
}

// createReferenceImageDefinition saves referenceImageGraphRequest and returns its
// workflow id and version.
func (e *testEnv) createReferenceImageDefinition(t *testing.T) (string, int) {
	t.Helper()
	resp, body := e.doJSON(t, http.MethodPost, "/api/definitions", referenceImageGraphRequest())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)
	if workflowID == "" || version == 0 {
		t.Fatalf("POST /api/definitions returned no version identity: %s", body)
	}
	return workflowID, int(version)
}

// createRunWithReference POSTs one Run carrying reference as the Image Input's AssetRef.
func (e *testEnv) createRunWithReference(t *testing.T, workflowID string, version int, reference map[string]any) (*http.Response, []byte) {
	t.Helper()
	return e.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId":        workflowID,
		"definitionVersion": version,
		"input":             map[string]any{"caption": "a small ember creature", "reference": reference},
	})
}

// assertAssetRefRejected pins the whole rejection contract: HTTP 400 with the existing
// VALIDATION_FAILED code (docs/08-interface-spec.md §6 Error Contract defines no Asset-specific
// 400 code), a detail entry naming the offending Image Input node and the assetId, and no
// Run identity in the body. It also proves the response leaks nothing about storage: an
// asset's storage key is "<shard>/<assetId>", so neither that path nor the word "storage"
// may appear anywhere.
func assertAssetRefRejected(t *testing.T, resp *http.Response, raw []byte, assetID string) {
	t.Helper()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /api/runs status = %d, want %d; body=%s", resp.StatusCode, http.StatusBadRequest, raw)
	}
	if code := errorCode(t, raw); code != "VALIDATION_FAILED" {
		t.Errorf("error code = %q, want %q", code, "VALIDATION_FAILED")
	}
	envelope := decodeBody[map[string]any](t, raw)
	errBody, _ := envelope["error"].(map[string]any)
	details, _ := errBody["details"].(map[string]any)
	errs, _ := details["errors"].([]any)
	if len(errs) == 0 {
		t.Fatalf("error details carry no validation errors: %s", raw)
	}
	first, _ := errs[0].(map[string]any)
	if nodeID, _ := first["nodeId"].(string); nodeID != "reference" {
		t.Errorf("validation error nodeId = %q, want the Image Input node %q; body=%s", first["nodeId"], "reference", raw)
	}
	if message, _ := first["message"].(string); !strings.Contains(message, assetID) {
		t.Errorf("validation error message = %q, want it to name assetId %q", message, assetID)
	}
	if _, present := envelope["id"]; present {
		t.Errorf("a rejected Run creation returned a Run identity: %s", raw)
	}
	if strings.Contains(strings.ToLower(string(raw)), "storage") || strings.Contains(string(raw), "/"+assetID) {
		t.Errorf("rejection response carries an internal storage detail (10-ops §4): %s", raw)
	}
}

// TestCreateRun_ImageInputUnknownAsset_400: a schema-valid AssetRef naming an Asset that
// was never uploaded is not a usable reference. The Run must be refused rather than
// created and left to fail later at image_input execution time.
func TestCreateRun_ImageInputUnknownAsset_400(t *testing.T) {
	env := newTestEnv(t)
	workflowID, version := env.createReferenceImageDefinition(t)
	const unknownID = "asset_never_uploaded"

	resp, raw := env.createRunWithReference(t, workflowID, version, map[string]any{
		"assetId":   unknownID,
		"mediaType": "image/png",
		"sizeBytes": float64(1024),
		"sha256":    strings.Repeat("a", 64),
	})

	assertAssetRefRejected(t, resp, raw, unknownID)
}

// TestCreateRun_ImageInputSha256Mismatch_400: `asset_id` points at immutable content
// (05 §1.2 "Asset Metadata 与 AssetRef"), so a digest that disagrees with the committed
// Metadata describes content this Asset never held. Accepting it would let a Run claim a
// provenance PostgreSQL contradicts.
func TestCreateRun_ImageInputSha256Mismatch_400(t *testing.T) {
	env := newTestEnv(t)
	workflowID, version := env.createReferenceImageDefinition(t)
	content := bytes.Repeat([]byte("ember"), 64)
	ref := env.uploadPNG(t, content)
	assetID, _ := ref["assetId"].(string)

	other := sha256.Sum256([]byte("different content"))
	resp, raw := env.createRunWithReference(t, workflowID, version, map[string]any{
		"assetId":   assetID,
		"mediaType": ref["mediaType"],
		"sizeBytes": ref["sizeBytes"],
		"sha256":    hex.EncodeToString(other[:]),
	})

	assertAssetRefRejected(t, resp, raw, assetID)
}

// TestCreateRun_ImageInputMediaTypeMismatch_400: the media type travels downstream inside
// the `source: ASSET` ImageRef the Image Input publishes, so a Provider would be told the
// wrong thing about content Emberling stored. The frozen runInputSchema cannot catch this
// -- image/jpeg is in acceptedMediaTypes -- which is exactly why the lookup exists.
func TestCreateRun_ImageInputMediaTypeMismatch_400(t *testing.T) {
	env := newTestEnv(t)
	workflowID, version := env.createReferenceImageDefinition(t)
	ref := env.uploadPNG(t, bytes.Repeat([]byte("pixels"), 32))
	assetID, _ := ref["assetId"].(string)

	resp, raw := env.createRunWithReference(t, workflowID, version, map[string]any{
		"assetId":   assetID,
		"mediaType": "image/jpeg",
		"sizeBytes": ref["sizeBytes"],
		"sha256":    ref["sha256"],
	})

	assertAssetRefRejected(t, resp, raw, assetID)
}

// TestCreateRun_ImageInputValidAsset_RunCreated is the positive half: the AssetRef the
// upload itself returned is accepted unchanged, the Run is created, and the Image Input
// publishes the canonical `source: ASSET` ImageRef (08 §2.2) -- never a browser URL, a
// storage key or binary content.
func TestCreateRun_ImageInputValidAsset_RunCreated(t *testing.T) {
	env := newTestEnv(t)
	workflowID, version := env.createReferenceImageDefinition(t)
	content := bytes.Repeat([]byte("ember"), 64)
	ref := env.uploadPNG(t, content)
	assetID, _ := ref["assetId"].(string)

	resp, raw := env.createRunWithReference(t, workflowID, version, ref)

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs with the uploaded AssetRef: status = %d, want %d; body=%s", resp.StatusCode, http.StatusCreated, raw)
	}
	runID, _ := decodeBody[map[string]any](t, raw)["id"].(string)
	if runID == "" {
		t.Fatalf("POST /api/runs returned no Run id: %s", raw)
	}

	snap := env.waitForTerminal(t, runID, e2eWait)
	run, _ := snap["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("Run status = %q, want %q; snapshot=%v", status, "COMPLETED", run)
	}

	nodeRun := env.nodeRunOf(t, runID, "reference")
	if nodeRun == nil {
		t.Fatalf("Run Snapshot carries no image_input NodeRun: %v", snap)
	}
	nodeRunID, _ := nodeRun["id"].(string)
	_, detailRaw := env.nodeRunDetail(t, runID, nodeRunID)
	assertAssetImageRef(t, detailRaw, assetID, ref)
}

// assertAssetImageRef finds the `image` port value inside raw and checks it is the
// canonical `source: ASSET` ImageRef of the named Asset, with no storage detail attached.
func assertAssetImageRef(t *testing.T, raw []byte, assetID string, want map[string]any) {
	t.Helper()
	detail := decodeBody[map[string]any](t, raw)
	nodeRun, _ := detail["nodeRun"].(map[string]any)
	output, _ := nodeRun["output"].(map[string]any)
	image, _ := output["image"].(map[string]any)
	if image == nil {
		t.Fatalf("image_input NodeRun output carries no `image` port: %s", raw)
	}
	if source, _ := image["source"].(string); source != "ASSET" {
		t.Errorf("image.source = %v, want %q", image["source"], "ASSET")
	}
	asset, _ := image["asset"].(map[string]any)
	if asset == nil {
		t.Fatalf("`source: ASSET` ImageRef carries no asset: %s", raw)
	}
	for _, field := range []string{"assetId", "mediaType", "sizeBytes", "sha256"} {
		if asset[field] != want[field] {
			t.Errorf("image.asset.%s = %v, want the uploaded AssetRef's %v", field, asset[field], want[field])
		}
	}
	for _, forbidden := range []string{"uri", "artifact", "storageKey", "downloadUrl"} {
		if _, present := image[forbidden]; present {
			t.Errorf("`source: ASSET` ImageRef carries a %q member: %s", forbidden, raw)
		}
	}
	if strings.Contains(strings.ToLower(string(raw)), "storagekey") || strings.Contains(string(raw), "/"+assetID) {
		t.Errorf("image_input NodeRun detail carries an internal storage detail (10-ops §4): %s", raw)
	}
}
