package mockprovider

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const imagesTestKey = "sk-images-endpoint-secret"

// imagesRequestBody is an OpenRouter-shaped Images API request carrying a prompt and a
// reference image as a data URL, as the image Adapter sends it.
func imagesRequestBody(model string) string {
	encoded, _ := json.Marshal(map[string]any{
		"model":  model,
		"prompt": "Soft film look prompt-canary",
		"n":      1,
		"input_references": []map[string]any{{
			"type":      "image_url",
			"image_url": map[string]string{"url": "data:image/png;base64,cGhvdG8tY2FuYXJ5"},
		}},
	})
	return string(encoded)
}

func postImages(t *testing.T, baseURL, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+imagesAPIPath, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+imagesTestKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s error = %v", imagesAPIPath, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

type imagesAnswer struct {
	Model string `json:"model"`
	Usage *struct {
		Cost *float64 `json:"cost"`
	} `json:"usage"`
	Data []struct {
		B64JSON string `json:"b64_json"`
	} `json:"data"`
}

func TestServer_ImagesAPI_AnswersOneDeterministicGradientPNG(t *testing.T) {
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))

	resp, raw := postImages(t, p.srv.URL, imagesRequestBody("vendor/image-model"))
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("status = %d %q, want 200 application/json; body=%s", resp.StatusCode, resp.Header.Get("Content-Type"), raw)
	}
	var answer imagesAnswer
	if err := json.Unmarshal(raw, &answer); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	if len(answer.Data) != 1 {
		t.Fatalf("data = %d images, want exactly 1", len(answer.Data))
	}
	if answer.Model != "vendor/image-model" || answer.Usage == nil || answer.Usage.Cost == nil || *answer.Usage.Cost < 0 {
		t.Errorf("model/usage = %q/%+v, want the requested model and a non-negative cost", answer.Model, answer.Usage)
	}
	image, err := base64.StdEncoding.DecodeString(answer.Data[0].B64JSON)
	if err != nil {
		t.Fatalf("b64_json is not base64: %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(image))
	if err != nil {
		t.Fatalf("b64_json is not a PNG: %v", err)
	}
	if decoded.At(0, 0) == decoded.At(15, 15) {
		t.Error("image corners share a colour, want a gradient")
	}

	_, again := postImages(t, p.srv.URL, imagesRequestBody("vendor/image-model"))
	var second imagesAnswer
	_ = json.Unmarshal(again, &second)
	if len(second.Data) != 1 || second.Data[0].B64JSON != answer.Data[0].B64JSON {
		t.Error("a repeated request answered a different image, want the same deterministic image")
	}
	_, other := postImages(t, p.srv.URL, imagesRequestBody("vendor/other-model"))
	var third imagesAnswer
	_ = json.Unmarshal(other, &third)
	if len(third.Data) != 1 || third.Data[0].B64JSON == answer.Data[0].B64JSON {
		t.Error("another model answered the same image, want images to differ by model")
	}
	_, otherPrompt := postImages(t, p.srv.URL, strings.Replace(imagesRequestBody("vendor/image-model"), "Soft film", "Clay", 1))
	var fourth imagesAnswer
	_ = json.Unmarshal(otherPrompt, &fourth)
	if len(fourth.Data) != 1 || fourth.Data[0].B64JSON == answer.Data[0].B64JSON {
		t.Error("another prompt answered the same image, want images to differ by request")
	}

	sum := sha256.Sum256(image)
	var generated []recordEntry
	for _, entry := range p.recordLines(t, "") {
		if entry.Event == recordGenerated {
			generated = append(generated, entry)
		}
	}
	if len(generated) != 4 || generated[0].Kind != kindImage || generated[0].SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("generated lines = %+v, want four image lines, the first with the answered image's digest", generated)
	}
}

func TestServer_ImagesAPI_RecordsEachRequestWithoutKeyPromptOrDataURL(t *testing.T) {
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))
	postImages(t, p.srv.URL, imagesRequestBody("vendor/image-model"))
	postImages(t, p.srv.URL, imagesRequestBody("vendor/image-model:mock-status-500"))

	entries := p.recordLines(t, "")
	var arrived []recordEntry
	for _, entry := range entries {
		if entry.Event == recordArrived && entry.Kind == kindImage {
			arrived = append(arrived, entry)
		}
	}
	if len(arrived) != 2 || !strings.HasPrefix(arrived[0].Scenario, imagesAPIScenario) || !strings.HasPrefix(arrived[1].Scenario, imagesAPIScenario) {
		t.Fatalf("arrived image lines = %+v, want two marked as Images API requests", arrived)
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("encode entries: %v", err)
	}
	for _, forbidden := range []string{imagesTestKey, "prompt-canary", "cGhvdG8tY2FuYXJ5", "data:image"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("record carries %q: %s", forbidden, raw)
		}
	}
}

func TestServer_ImagesAPI_StatusDirective_FailsWithoutGeneratedLine(t *testing.T) {
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))

	resp, raw := postImages(t, p.srv.URL, imagesRequestBody("vendor/image-model:mock-status-503"))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), imagesAPIFailureDetail) {
		t.Errorf("failure body = %s, want the fixed upstream failure detail", raw)
	}
	for _, entry := range p.recordLines(t, "") {
		if entry.Event == recordGenerated {
			t.Fatalf("record has a generated line %+v for a failed request", entry)
		}
	}
}

func TestServer_ImagesAPI_InvalidRequestReturnsBadRequest(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewDispatcher(nil)))
	t.Cleanup(srv.Close)
	for name, body := range map[string]string{
		"not JSON":           `{`,
		"no model":           `{"prompt":"x","n":1}`,
		"two images":         `{"model":"vendor/m","prompt":"x","n":2}`,
		"status out of band": imagesRequestBody("vendor/m:mock-status-200"),
		"status not numeric": imagesRequestBody("vendor/m:mock-status-abc"),
	} {
		t.Run(name, func(t *testing.T) {
			if resp, raw := postImages(t, srv.URL, body); resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", resp.StatusCode, raw)
			}
		})
	}
}

func TestBarrier_ImageKind_HoldsImagesAPIRequestUntilRelease(t *testing.T) {
	p := newControlledProvider(t, filepath.Join(t.TempDir(), "record.jsonl"))
	p.control(t, "/control/pause", `{"kinds":["image"]}`)

	statuses := make(chan int, 1)
	go func() {
		resp, err := http.Post(p.srv.URL+imagesAPIPath, "application/json", strings.NewReader(imagesRequestBody("vendor/image-model")))
		if err != nil {
			statuses <- 0
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		statuses <- resp.StatusCode
	}()
	if label := p.waitHeld(t); label != kindImage {
		t.Fatalf("held label = %q, want %q", label, kindImage)
	}
	if state := p.barrierState(t); len(state.Held) != 1 || state.Held[0].Kind != kindImage {
		t.Fatalf("barrier = %+v, want the one image request held", state)
	}
	if resp := postJSON(t, p.srv.URL+"/v1/text/generate", generateRequest{Prompt: "hi"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("generate status = %d, want 200 while only images are paused", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if released := p.control(t, "/control/release", "").Released; released != 1 {
		t.Fatalf("released = %d, want 1", released)
	}
	select {
	case status := <-statuses:
		if status != http.StatusOK {
			t.Fatalf("status after release = %d, want 200", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the released request did not answer")
	}
}

func TestServer_WithHeldHook_ReportsHeldRequests(t *testing.T) {
	record, err := OpenRecord(filepath.Join(t.TempDir(), "record.jsonl"))
	if err != nil {
		t.Fatalf("OpenRecord() error = %v", err)
	}
	held := make(chan string, 1)
	server := NewServer(NewDispatcher(nil), WithTestControls(record), WithHeldHook(held))
	srv := httptest.NewServer(server)
	t.Cleanup(func() {
		server.StopHolding()
		srv.Close()
		_ = record.Close()
	})
	if resp, err := http.Post(srv.URL+"/control/pause", "application/json", strings.NewReader(`{"kinds":["image"]}`)); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("pause = %v %v, want 200", resp, err)
	}
	go func() {
		resp, err := http.Post(srv.URL+imagesAPIPath, "application/json", strings.NewReader(imagesRequestBody("vendor/image-model")))
		if err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case label := <-held:
		if label != kindImage {
			t.Fatalf("held label = %q, want %q", label, kindImage)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the held hook did not report the held request")
	}
}
