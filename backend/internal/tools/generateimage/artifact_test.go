package generateimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/mockprovider"
)

// imageProvider is a scripted Provider: POST /v1/assets answers with an imageUrl on a
// public origin the Tool must not fetch from, and GET on that path serves imageBody with
// imageStatus. It counts both calls so a test can prove the Tool never retries.
type imageProvider struct {
	server      *httptest.Server
	imageStatus int
	imageBody   []byte
	generations atomic.Int32
	fetches     atomic.Int32
}

const unreachablePublicOrigin = "http://public-images.invalid:9101"

func newImageProvider(t *testing.T, status int, body []byte) *imageProvider {
	t.Helper()
	p := &imageProvider{imageStatus: status, imageBody: body}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == assetsPath:
			p.generations.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"assetId":"img_0123456789abcdef","imageUrl":"`+unreachablePublicOrigin+`/v1/assets/img_0123456789abcdef.png"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/assets/img_0123456789abcdef.png":
			p.fetches.Add(1)
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(p.imageStatus)
			_, _ = w.Write(p.imageBody)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.server.Close)
	return p
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	// The Mock Provider's own asset route is the reference PNG every mode serves.
	srv := httptest.NewServer(mockprovider.NewServer(mockprovider.NewDispatcher(nil)))
	defer srv.Close()
	response, err := srv.Client().Get(srv.URL + "/v1/assets/img_0123456789abcdef.png")
	if err != nil {
		t.Fatalf("fetch reference png: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("reference png: status %d, err %v", response.StatusCode, err)
	}
	return body
}

const generationArguments = `{"photoAssetId":"asset_photo_1","settings":{"style":"film"}}`

func TestExecute_ImageFetched_SavesArtifactAndDeclaresIt(t *testing.T) {
	png := pngBytes(t)
	provider := newImageProvider(t, http.StatusOK, png)
	artifacts := newArtifacts(t)

	got, err := New(provider.server.URL, provider.server.Client(), artifacts).Execute(context.Background(), action(generationArguments))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	validateOutput(t, got.Result.Output)

	var out result
	if err := json.Unmarshal(got.Result.Output, &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	sum := sha256.Sum256(png)
	digest := hex.EncodeToString(sum[:])
	if out.Artifact.SHA256 != digest || out.Artifact.SizeBytes != int64(len(png)) ||
		out.Artifact.MediaType != "image/png" || out.Artifact.ArtifactID != "artifact_"+digest[:32] {
		t.Fatalf("artifact = %+v, want the reference of the fetched png", out.Artifact)
	}
	// The fetch went to the configured Provider, while the result keeps the public URL.
	if !strings.HasPrefix(out.ImageURL, unreachablePublicOrigin+"/") {
		t.Errorf("imageUrl = %q, want the public URL the Provider returned", out.ImageURL)
	}
	if len(got.Result.Artifacts) != 1 || got.Result.Artifacts[0] != out.Artifact {
		t.Fatalf("declared artifacts = %+v, want exactly the output's artifact", got.Result.Artifacts)
	}
	objects, err := artifacts.Objects(context.Background())
	if err != nil || len(objects) != 1 || objects[0].ArtifactID != out.Artifact.ArtifactID {
		t.Fatalf("stored objects = %+v (err %v), want the artifact's object", objects, err)
	}
	for _, field := range []string{"storageKey", "storage_key", "artifacts/"} {
		if bytes.Contains(got.Result.Output, []byte(field)) {
			t.Errorf("output %s carries a storage location (%q)", got.Result.Output, field)
		}
	}
}

func TestExecute_ImageCannotBeSaved_FailsOnceWithoutURL(t *testing.T) {
	png := pngBytes(t)
	cases := map[string]struct {
		status   int
		body     []byte
		maxBytes int64
	}{
		"image not found":     {status: http.StatusNotFound, body: []byte("missing"), maxBytes: 1 << 20},
		"image too large":     {status: http.StatusOK, body: png, maxBytes: int64(len(png)) - 1},
		"response not image":  {status: http.StatusOK, body: []byte("<html>no image</html>"), maxBytes: 1 << 20},
		"provider error page": {status: http.StatusInternalServerError, body: png, maxBytes: 1 << 20},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			provider := newImageProvider(t, tc.status, tc.body)
			root := t.TempDir()
			artifacts := asset.NewArtifactStore(root, tc.maxBytes)

			_, err := New(provider.server.URL, provider.server.Client(), artifacts).Execute(context.Background(), action(generationArguments))

			if err == nil {
				t.Fatal("execute error = nil, want a save failure")
			}
			for _, leak := range []string{provider.server.URL, unreachablePublicOrigin, "img_0123456789abcdef", root, "artifacts/"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("error %q leaks %q", err, leak)
				}
			}
			if g, f := provider.generations.Load(), provider.fetches.Load(); g != 1 || f != 1 {
				t.Errorf("generations=%d fetches=%d, want exactly one of each (no retry)", g, f)
			}
			if objects, _ := artifacts.Objects(context.Background()); len(objects) != 0 {
				t.Errorf("objects after failure: %+v, want none", objects)
			}
		})
	}
}

func TestExecute_NoArtifactStore_FailsBeforeCallingTheProvider(t *testing.T) {
	provider := newImageProvider(t, http.StatusOK, pngBytes(t))

	if _, err := New(provider.server.URL, provider.server.Client(), nil).Execute(context.Background(), action(generationArguments)); err == nil {
		t.Fatal("execute error = nil, want a configuration failure")
	}
	if g := provider.generations.Load(); g != 0 {
		t.Errorf("generations = %d, want 0", g)
	}
}

func TestFetchURL_ReplacesOnlySchemeAndHost(t *testing.T) {
	e := New("http://mockprovider:9101/", nil, nil)
	got, err := e.fetchURL("https://localhost:9101/v1/assets/img_1.png?v=2")
	if err != nil {
		t.Fatalf("fetchURL: %v", err)
	}
	if want := "http://mockprovider:9101/v1/assets/img_1.png?v=2"; got != want {
		t.Errorf("fetchURL = %q, want %q", got, want)
	}
	for _, bad := range []string{"", "/v1/assets/x.png", "file:///etc/passwd", "http://host"} {
		if _, err := e.fetchURL(bad); err == nil {
			t.Errorf("fetchURL(%q) succeeded, want error", bad)
		}
	}
}
