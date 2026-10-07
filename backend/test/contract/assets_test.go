//go:build integration

// Package contract: this file covers the Asset routes of the REST API —
// multipart upload, Metadata read and the controlled download that replaces a signed URL.
// The guarantee every test here protects is that an AssetRef is only ever returned for an
// Asset whose content and Metadata are both committed, and that the internal storage key
// never crosses the HTTP boundary.
package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"

	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
)

// multipartUpload builds a multipart/form-data body with one file part, so a test can
// control the part name and its Content-Type independently of the file's content.
func multipartUpload(t *testing.T, partName, filename, mediaType string, content []byte) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition",
		`form-data; name="`+partName+`"; filename="`+filename+`"`)
	if mediaType != "" {
		header.Set("Content-Type", mediaType)
	}
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("create multipart part: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write multipart part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return writer.FormDataContentType(), buf.Bytes()
}

func (e *testEnv) doUpload(t *testing.T, contentType string, body []byte) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.server.URL+"/api/assets", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build upload request: %v", err)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := e.server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /api/assets: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read upload response: %v", err)
	}
	return resp, raw
}

// uploadPNG uploads a deterministic image/png body and returns the AssetRef it produced.
func (e *testEnv) uploadPNG(t *testing.T, content []byte) map[string]any {
	t.Helper()
	contentType, body := multipartUpload(t, "file", "reference.png", "image/png", content)
	resp, raw := e.doUpload(t, contentType, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/assets: status %d body %s", resp.StatusCode, raw)
	}
	return decodeBody[map[string]any](t, raw)
}

func TestAssets_Upload_ReturnsAssetRef(t *testing.T) {
	env := newTestEnv(t)
	content := bytes.Repeat([]byte("ember"), 64)
	digest := sha256.Sum256(content)

	contentType, body := multipartUpload(t, "file", "reference.png", "image/png", content)
	resp, raw := env.doUpload(t, contentType, body)

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/assets: status %d, want 201; body %s", resp.StatusCode, raw)
	}
	ref := decodeBody[map[string]any](t, raw)
	if id, _ := ref["assetId"].(string); id == "" {
		t.Errorf("assetId: got %v, want a non-empty id; body %s", ref["assetId"], raw)
	}
	if got, _ := ref["mediaType"].(string); got != "image/png" {
		t.Errorf("mediaType: got %v, want image/png", ref["mediaType"])
	}
	if got, _ := ref["sizeBytes"].(float64); int(got) != len(content) {
		t.Errorf("sizeBytes: got %v, want %d", ref["sizeBytes"], len(content))
	}
	if got, _ := ref["sha256"].(string); got != hex.EncodeToString(digest[:]) {
		t.Errorf("sha256: got %v, want %s", ref["sha256"], hex.EncodeToString(digest[:]))
	}
}

func TestAssets_Upload_UnsupportedMediaType_400(t *testing.T) {
	env := newTestEnv(t)

	contentType, body := multipartUpload(t, "file", "notes.pdf", "application/pdf", []byte("%PDF-1.4"))
	resp, raw := env.doUpload(t, contentType, body)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /api/assets with application/pdf: status %d, want 400; body %s", resp.StatusCode, raw)
	}
	if code := errorCode(t, raw); code != "VALIDATION_FAILED" {
		t.Errorf("error code: got %s, want VALIDATION_FAILED", code)
	}
}

func TestAssets_Upload_TooLarge_413(t *testing.T) {
	env := newTestEnvWithOptions(t, testEnvOptions{AssetMaxUploadBytes: 1024})

	contentType, body := multipartUpload(t, "file", "large.png", "image/png", bytes.Repeat([]byte("x"), 4096))
	resp, raw := env.doUpload(t, contentType, body)

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST /api/assets over the limit: status %d, want 413; body %s", resp.StatusCode, raw)
	}
	if code := errorCode(t, raw); code != "PAYLOAD_TOO_LARGE" {
		t.Errorf("error code: got %s, want PAYLOAD_TOO_LARGE", code)
	}
}

// A body far beyond the limit must be refused by the request bound itself, before the
// whole upload has been read, rather than only by the storage layer's streaming cap.
func TestAssets_Upload_BodyBeyondRequestLimit_413(t *testing.T) {
	env := newTestEnvWithOptions(t, testEnvOptions{AssetMaxUploadBytes: 1024})

	contentType, body := multipartUpload(t, "file", "huge.png", "image/png", bytes.Repeat([]byte("x"), 64<<10))
	resp, raw := env.doUpload(t, contentType, body)

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST /api/assets beyond the request limit: status %d, want 413; body %s", resp.StatusCode, raw)
	}
	if code := errorCode(t, raw); code != "PAYLOAD_TOO_LARGE" {
		t.Errorf("error code: got %s, want PAYLOAD_TOO_LARGE", code)
	}
}

func TestAssets_Upload_WrongPartName_400(t *testing.T) {
	env := newTestEnv(t)

	contentType, body := multipartUpload(t, "image", "reference.png", "image/png", []byte("bytes"))
	resp, raw := env.doUpload(t, contentType, body)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /api/assets with a part named \"image\": status %d, want 400; body %s", resp.StatusCode, raw)
	}
}

func TestAssets_Upload_MalformedMultipart_400(t *testing.T) {
	env := newTestEnv(t)

	resp, raw := env.doUpload(t, "multipart/form-data; boundary=zzz", []byte("not a multipart body"))

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /api/assets with a malformed body: status %d, want 400; body %s", resp.StatusCode, raw)
	}
	if code := errorCode(t, raw); code != "VALIDATION_FAILED" {
		t.Errorf("error code: got %s, want VALIDATION_FAILED", code)
	}
}

func TestAssets_Get_Metadata_NoStorageKey(t *testing.T) {
	env := newTestEnv(t)
	ref := env.uploadPNG(t, []byte("reference image bytes"))
	assetID, _ := ref["assetId"].(string)

	resp, raw := env.doJSON(t, http.MethodGet, "/api/assets/"+assetID, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/assets/%s: status %d, want 200; body %s", assetID, resp.StatusCode, raw)
	}
	metadata := decodeBody[map[string]any](t, raw)
	for _, field := range []string{"assetId", "mediaType", "sizeBytes", "sha256"} {
		if metadata[field] != ref[field] {
			t.Errorf("%s: metadata has %v, the AssetRef had %v", field, metadata[field], ref[field])
		}
	}
	for key := range metadata {
		if strings.Contains(strings.ToLower(key), "storage") {
			t.Errorf("metadata exposes an internal storage field %q: %s", key, raw)
		}
	}
	// A storage key is "<shard>/<assetId>", so its value cannot hide under another field
	// name either: no key path may appear anywhere in the body.
	if strings.Contains(string(raw), "/"+assetID) {
		t.Errorf("metadata response carries a storage key value: %s", raw)
	}
}

func TestAssets_Content_StreamsBytesWithHeaders(t *testing.T) {
	env := newTestEnv(t)
	content := bytes.Repeat([]byte("pixels"), 32)
	digest := sha256.Sum256(content)
	ref := env.uploadPNG(t, content)
	assetID, _ := ref["assetId"].(string)

	resp, raw := env.doJSON(t, http.MethodGet, "/api/assets/"+assetID+"/content", nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET content: status %d, want 200; body %s", resp.StatusCode, raw)
	}
	if !bytes.Equal(raw, content) {
		t.Errorf("content: got %d bytes, want %d", len(raw), len(content))
	}
	if got := resp.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type: got %q, want image/png", got)
	}
	if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(content)) {
		t.Errorf("Content-Length: got %q, want %d", got, len(content))
	}
	if got, want := resp.Header.Get("ETag"), `"`+hex.EncodeToString(digest[:])+`"`; got != want {
		t.Errorf("ETag: got %q, want %q", got, want)
	}
	if got := resp.Header.Get("Cache-Control"); !strings.HasPrefix(got, "private") {
		t.Errorf("Cache-Control: got %q, want a private cache directive", got)
	}
	if got := resp.Header.Get("Content-Disposition"); got != "inline" {
		t.Errorf("Content-Disposition: got %q, want inline", got)
	}
}

func TestAssets_Unknown_404(t *testing.T) {
	env := newTestEnv(t)

	for _, path := range []string{"/api/assets/asset_missing", "/api/assets/asset_missing/content"} {
		resp, raw := env.doJSON(t, http.MethodGet, path, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s: status %d, want 404; body %s", path, resp.StatusCode, raw)
		}
		if code := errorCode(t, raw); code != "ASSET_NOT_FOUND" {
			t.Errorf("GET %s: error code %s, want ASSET_NOT_FOUND", path, code)
		}
	}
}

// TestAssets_Restart_MetadataAndContentStillReadable is the Asset track's recovery proof.
// An Asset is two committed facts -- the `assets` row and the binary under its storage key
// -- and neither lives in the process. A restarted Backend is a new process
// image over the same database and the same storage volume, so an AssetRef handed out
// before the restart must still resolve afterwards, byte for byte.
//
// The restart is the same one the Run recovery tests perform: stop the Backend the way a
// process exit would, then wire a second one over the surviving state.
func TestAssets_Restart_MetadataAndContentStillReadable(t *testing.T) {
	before := newTestEnv(t)
	content := bytes.Repeat([]byte("ember pixels"), 40)
	digest := hex.EncodeToString(sha256Sum(content))
	ref := before.uploadPNG(t, content)
	assetID, _ := ref["assetId"].(string)
	if sha, _ := ref["sha256"].(string); sha != digest {
		t.Fatalf("uploaded AssetRef sha256 = %q, want %q", sha, digest)
	}

	before.stop()
	after := newTestEnvWithOptions(t, testEnvOptions{Pool: before.pool, AssetStorageRoot: before.assetRoot})

	resp, raw := after.doJSON(t, http.MethodGet, "/api/assets/"+assetID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/assets/%s after restart: status %d, want 200; body %s", assetID, resp.StatusCode, raw)
	}
	metadata := decodeBody[map[string]any](t, raw)
	for _, field := range []string{"assetId", "mediaType", "sizeBytes", "sha256"} {
		if metadata[field] != ref[field] {
			t.Errorf("%s after restart: got %v, want the pre-restart AssetRef's %v", field, metadata[field], ref[field])
		}
	}

	resp, body := after.doJSON(t, http.MethodGet, "/api/assets/"+assetID+"/content", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET content after restart: status %d, want 200; body %s", resp.StatusCode, body)
	}
	if !bytes.Equal(body, content) {
		t.Fatalf("content after restart: got %d bytes, want the %d uploaded", len(body), len(content))
	}
	if got := hex.EncodeToString(sha256Sum(body)); got != digest {
		t.Errorf("content digest after restart: got %s, want %s", got, digest)
	}
	if got, want := resp.Header.Get("ETag"), `"`+digest+`"`; got != want {
		t.Errorf("ETag after restart: got %q, want %q", got, want)
	}
	if strings.Contains(string(raw), "/"+assetID) {
		t.Errorf("metadata response after restart carries a storage key value: %s", raw)
	}
}

// sha256Sum keeps the digest comparisons above readable; hex.EncodeToString cannot take
// the [32]byte sha256.Sum256 returns directly.
func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}
