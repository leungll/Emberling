package mockprovider

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func postImage(t *testing.T, baseURL string, body string) (*http.Response, imageResponse) {
	t.Helper()
	resp, err := http.Post(baseURL+"/v1/assets", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/assets error = %v", err)
	}
	defer resp.Body.Close()
	var decoded imageResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			t.Fatalf("decode image response: %v", err)
		}
	}
	return resp, decoded
}

func newMediaTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(NewServer(NewDispatcher(nil)))
	t.Cleanup(srv.Close)
	return srv
}

func kindEvents(entries []recordEntry, kind string) []string {
	var events []string
	for _, entry := range entries {
		if entry.Kind == kind {
			events = append(events, entry.Event)
		}
	}
	return events
}

func TestServer_GenerateImage_RecordsGeneratedLineAndServesReachableAsset(t *testing.T) {
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))
	body := `{"photoAssetId":"asset_photo_1","settingsDigest":"sha256:aa"}`

	resp, generated := postImage(t, p.srv.URL, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/assets status = %d, want 200", resp.StatusCode)
	}
	if !generatedIDPattern.MatchString(generated.AssetID) {
		t.Fatalf("assetId = %q, want the generated id shape", generated.AssetID)
	}
	if want := p.srv.URL + "/v1/assets/" + generated.AssetID + ".png"; generated.ImageURL != want {
		t.Fatalf("imageUrl = %q, want %q", generated.ImageURL, want)
	}

	_, again := postImage(t, p.srv.URL, body)
	if again.AssetID != generated.AssetID {
		t.Fatalf("repeated request assetId = %q, want the same %q", again.AssetID, generated.AssetID)
	}
	_, other := postImage(t, p.srv.URL, `{"photoAssetId":"asset_photo_1","settingsDigest":"sha256:bb"}`)
	if other.AssetID == generated.AssetID {
		t.Fatal("different settings digest generated the same assetId")
	}

	image, err := http.Get(generated.ImageURL)
	if err != nil {
		t.Fatalf("GET imageUrl error = %v", err)
	}
	defer image.Body.Close()
	if image.StatusCode != http.StatusOK || image.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("GET imageUrl = %d %q, want 200 image/png", image.StatusCode, image.Header.Get("Content-Type"))
	}
	raw, _ := io.ReadAll(image.Body)
	decoded, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("imageUrl does not serve a decodable PNG: %v", err)
	}
	sum := sha256.Sum256([]byte(generated.AssetID))
	if got := color.RGBAModel.Convert(decoded.At(0, 0)).(color.RGBA); got != (color.RGBA{R: sum[0], G: sum[1], B: sum[2], A: 0xff}) {
		t.Fatalf("pixel colour = %+v, want the colour derived from the asset id", got)
	}

	entries := p.recordLines(t, "")
	var generatedLines []recordEntry
	for _, entry := range entries {
		if entry.Event == recordGenerated {
			generatedLines = append(generatedLines, entry)
		}
	}
	if len(generatedLines) != 3 || generatedLines[0].AssetID != generated.AssetID || generatedLines[0].Kind != kindImage {
		t.Fatalf("generated lines = %+v, want three, the first naming %s", generatedLines, generated.AssetID)
	}
	if got := kindEvents(entries, kindImage)[:3]; got[0] != recordArrived || got[1] != recordGenerated || got[2] != recordResponded {
		t.Fatalf("image record events = %v, want arrived, generated, responded", got)
	}
}

func TestServer_GenerateImage_OutcomeFailed_Returns422WithoutGeneratedLine(t *testing.T) {
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))

	resp, _ := postImage(t, p.srv.URL, `{"photoAssetId":"asset_photo_1","settingsDigest":"sha256:aa","outcome":"failed"}`)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	events := kindEvents(p.recordLines(t, ""), kindImage)
	if len(events) != 2 || events[0] != recordArrived || events[1] != recordResponded {
		t.Fatalf("image record events = %v, want arrived and responded only", events)
	}
}

func TestServer_GenerateImage_InvalidRequestReturnsBadRequest(t *testing.T) {
	srv := newMediaTestServer(t)
	for name, body := range map[string]string{
		"missing digest":  `{"photoAssetId":"asset_photo_1"}`,
		"missing photo":   `{"settingsDigest":"sha256:aa"}`,
		"unknown member":  `{"photoAssetId":"p","settingsDigest":"d","prompt":"x"}`,
		"unknown outcome": `{"photoAssetId":"p","settingsDigest":"d","outcome":"lost"}`,
		"not json":        `{`,
	} {
		t.Run(name, func(t *testing.T) {
			if resp, _ := postImage(t, srv.URL, body); resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

func TestServer_Asset_NameOutsideTheGeneratedShapeIsNotFound(t *testing.T) {
	srv := newMediaTestServer(t)
	for _, name := range []string{"img_0011223344556677", "img_00112233445566.png", "other.png", "img_0011223344556677.jpg"} {
		resp, err := http.Get(srv.URL + "/v1/assets/" + name)
		if err != nil {
			t.Fatalf("GET %s error = %v", name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET /v1/assets/%s status = %d, want 404", name, resp.StatusCode)
		}
	}
}

func TestServer_VideoTask_RecordsVideoDispatchedAndCallbackReportsVideoURL(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))

	statuses := p.asyncTask(t, taskRequest{ExternalTaskID: "task-video", CallbackURL: receiver.server.URL, CallbackToken: "tok", Media: mediaVideo})
	if status := waitStatus(t, statuses); status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", status)
	}
	waitForCalls(t, p.notify, 1)

	calls := receiver.Calls()
	if len(calls) != 1 {
		t.Fatalf("callbacks = %d, want 1", len(calls))
	}
	var payload struct {
		Status   string `json:"status"`
		VideoURL string `json:"videoUrl"`
	}
	if err := json.Unmarshal(calls[0].Body.Payload, &payload); err != nil {
		t.Fatalf("callback payload is not JSON: %v", err)
	}
	if payload.Status != "SUCCEEDED" || !strings.HasPrefix(payload.VideoURL, p.srv.URL+"/v1/videos/vid_") || !strings.HasSuffix(payload.VideoURL, ".mp4") {
		t.Fatalf("callback payload = %+v, want SUCCEEDED with a video URL on this Provider", payload)
	}
	if events := eventsFor(p.recordLines(t, ""), "task-video"); !contains(events, recordVideoDispatched) {
		t.Fatalf("record events = %v, want a video_dispatched line", events)
	}
}

func TestServer_VideoTask_OutcomeFailedKeepsFailurePayload(t *testing.T) {
	receiver := newCallbackReceiver()
	defer receiver.Close()
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))

	statuses := p.asyncTask(t, taskRequest{ExternalTaskID: "task-video-failed", CallbackURL: receiver.server.URL, CallbackToken: "tok", Media: mediaVideo, Outcome: outcomeFailed})
	if status := waitStatus(t, statuses); status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", status)
	}
	waitForCalls(t, p.notify, 1)

	if got := string(receiver.Calls()[0].Body.Payload); got != string(failedPayload) {
		t.Fatalf("callback payload = %s, want the failure payload", got)
	}
}

func TestServer_Tasks_UnknownMediaReturnsBadRequest(t *testing.T) {
	srv := newMediaTestServer(t)
	resp := postJSON(t, srv.URL+"/v1/tasks", map[string]any{"callbackUrl": "http://x", "callbackToken": "t", "media": "audio"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
