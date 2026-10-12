//go:build integration

package contract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/fixture/mockprovider"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
)

// artifactContentRun is one completed Run whose first generate_image Attempt declared
// artifact, with the artifact's storage key for tests that tamper with its content.
type artifactContentRun struct {
	runID      string
	workflowID string
	version    int
	artifact   domain.ArtifactRef
	storageKey string
}

// newArtifactContentEnv starts a Backend whose generate_image calls reach a real Mock
// Provider, logging into logs.
func newArtifactContentEnv(t *testing.T, logs *syncBuffer) *testEnv {
	t.Helper()
	dispatcher := mockprovider.NewDispatcher(nil)
	provider := httptest.NewServer(mockprovider.NewServer(dispatcher))
	t.Cleanup(func() {
		provider.Close()
		_ = dispatcher.Shutdown(context.Background())
	})
	return newTestEnvWithOptions(t, testEnvOptions{
		MockTaskBaseURL: provider.URL,
		Logger:          slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
}

// runWithGeneratedArtifact runs the generate-review-generate Agent to completion and
// returns the artifact its first generation declared.
func runWithGeneratedArtifact(t *testing.T, env *testEnv) artifactContentRun {
	t.Helper()
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

	out := artifactContentRun{runID: runID, workflowID: workflowID, version: version}
	if err := env.uow.WithinReadTx(context.Background(), func(ctx context.Context, tx store.Tx) error {
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
				out.artifact = result.Artifact
				out.storageKey = record.StorageKey
				return nil
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read the generated artifact: %v", err)
	}
	if out.artifact.ArtifactID == "" {
		t.Fatalf("no generate_image Attempt declared an artifact")
	}
	return out
}

func artifactContentPath(runID, artifactID string) string {
	return "/api/runs/" + runID + "/artifacts/" + artifactID + "/content"
}

// getArtifactContent performs the raw GET, without assuming a JSON body.
func getArtifactContent(t *testing.T, env *testEnv, runID, artifactID string) (*http.Response, []byte) {
	t.Helper()
	resp, err := env.server.Client().Get(env.server.URL + artifactContentPath(runID, artifactID))
	if err != nil {
		t.Fatalf("GET artifact content: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read artifact content: %v", err)
	}
	return resp, body
}

// assertArtifactNotFound checks the single 404 every out-of-scope read answers with.
func assertArtifactNotFound(t *testing.T, env *testEnv, runID, artifactID string) []byte {
	t.Helper()
	resp, body := getArtifactContent(t, env, runID, artifactID)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET %s status = %d, want 404; body=%s", artifactContentPath(runID, artifactID), resp.StatusCode, body)
	}
	envelope := decodeBody[struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}](t, body)
	if envelope.Error.Code != "ARTIFACT_NOT_FOUND" {
		t.Fatalf("error code = %q, want ARTIFACT_NOT_FOUND; body=%s", envelope.Error.Code, body)
	}
	return body
}

// TestArtifactContent_Headers_TypeNosniffETagImmutable reads an artifact of its own Run:
// the exact bytes the result declared, with the stored media type, size, digest ETag and
// an immutable private cache policy.
func TestArtifactContent_Headers_TypeNosniffETagImmutable(t *testing.T) {
	env := newArtifactContentEnv(t, &syncBuffer{})
	generated := runWithGeneratedArtifact(t, env)

	resp, body := getArtifactContent(t, env, generated.runID, generated.artifact.ArtifactID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	wantHeaders := map[string]string{
		"Content-Type":           generated.artifact.MediaType,
		"Content-Length":         strconv.FormatInt(generated.artifact.SizeBytes, 10),
		"Etag":                   `"` + generated.artifact.SHA256 + `"`,
		"Cache-Control":          "private, max-age=31536000, immutable",
		"X-Content-Type-Options": "nosniff",
		"Content-Disposition":    "inline",
	}
	for name, want := range wantHeaders {
		if got := resp.Header.Get(name); got != want {
			t.Errorf("header %s = %q, want %q", name, got, want)
		}
	}
	if resp.Header.Get("Accept-Ranges") == "bytes" {
		t.Errorf("Accept-Ranges = bytes, want no range support")
	}
	sum := sha256.Sum256(body)
	if int64(len(body)) != generated.artifact.SizeBytes || hex.EncodeToString(sum[:]) != generated.artifact.SHA256 {
		t.Errorf("body: %d bytes with sha256 %x, want the declared %d bytes with %s",
			len(body), sum, generated.artifact.SizeBytes, generated.artifact.SHA256)
	}
}

// TestArtifactContent_OtherRunsArtifact_NotFound covers the Run scope: another Run, a Run
// that does not exist and an artifact the Run never declared all answer the same 404, so
// the response does not reveal whether the artifact exists elsewhere.
func TestArtifactContent_OtherRunsArtifact_NotFound(t *testing.T) {
	env := newArtifactContentEnv(t, &syncBuffer{})
	generated := runWithGeneratedArtifact(t, env)
	// The script answers FINAL once its generations are spent, so this Run generates
	// nothing and declares no artifact.
	otherRunID := createAgentRun(t, env, generated.workflowID, generated.version, "answer directly")
	env.waitForTerminal(t, otherRunID, agentTraceWait)

	otherRun := assertArtifactNotFound(t, env, otherRunID, generated.artifact.ArtifactID)
	missingRun := assertArtifactNotFound(t, env, "run_does_not_exist", generated.artifact.ArtifactID)
	undeclared := assertArtifactNotFound(t, env, generated.runID, "artifact_"+strings.Repeat("0", 32))
	if !bytes.Equal(otherRun, missingRun) || !bytes.Equal(otherRun, undeclared) {
		t.Errorf("404 bodies differ between causes:\nother run: %s\nmissing run: %s\nundeclared: %s", otherRun, missingRun, undeclared)
	}
}

// TestArtifactContent_OrphanWithoutLink_NotFound covers content no committed result of
// the Run declared: an object without metadata and an object with metadata but no link
// are both unreadable through the Run.
func TestArtifactContent_OrphanWithoutLink_NotFound(t *testing.T) {
	env := newArtifactContentEnv(t, &syncBuffer{})
	generated := runWithGeneratedArtifact(t, env)
	artifacts := asset.NewArtifactStore(env.assetRoot, defaultTestArtifactMaxBytes)

	objectOnly, err := artifacts.Write(context.Background(), bytes.NewReader(gradientPNG(t, 1)))
	if err != nil {
		t.Fatalf("write orphan object: %v", err)
	}
	assertArtifactNotFound(t, env, generated.runID, objectOnly.ArtifactID)

	withMetadata, err := artifacts.Write(context.Background(), bytes.NewReader(gradientPNG(t, 2)))
	if err != nil {
		t.Fatalf("write orphan object: %v", err)
	}
	key, err := asset.ArtifactStorageKey(withMetadata.SHA256)
	if err != nil {
		t.Fatalf("ArtifactStorageKey: %v", err)
	}
	if err := env.uow.WithinTx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		return tx.Artifacts().CreateIfAbsent(ctx, store.ArtifactRecord{
			Artifact: domain.Artifact{
				ArtifactID: withMetadata.ArtifactID, MediaType: withMetadata.MediaType,
				SizeBytes: withMetadata.SizeBytes, SHA256: withMetadata.SHA256, CreatedAt: time.Now(),
			},
			StorageKey: key,
		})
	}); err != nil {
		t.Fatalf("commit orphan metadata: %v", err)
	}
	assertArtifactNotFound(t, env, generated.runID, withMetadata.ArtifactID)
}

// TestArtifactContent_CorruptContent_FailsWithoutBody covers content that no longer
// matches its committed metadata, or is gone: the read fails with a 500 that carries none
// of the content, and the log names the artifact id but no storage key or path.
func TestArtifactContent_CorruptContent_FailsWithoutBody(t *testing.T) {
	logs := &syncBuffer{}
	env := newArtifactContentEnv(t, logs)
	generated := runWithGeneratedArtifact(t, env)
	path := filepath.Join(env.assetRoot, filepath.FromSlash(generated.storageKey))
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stored object: %v", err)
	}

	sameSize := bytes.Clone(original)
	sameSize[len(sameSize)-1] ^= 0xff
	tampered := map[string]func(){
		"same size, different bytes": func() { replaceObject(t, path, sameSize) },
		"truncated":                  func() { replaceObject(t, path, original[:len(original)-1]) },
		"longer":                     func() { replaceObject(t, path, append(bytes.Clone(original), 0)) },
		"missing": func() {
			if err := os.Remove(path); err != nil {
				t.Fatalf("remove stored object: %v", err)
			}
		},
	}
	for name, tamper := range tampered {
		t.Run(name, func(t *testing.T) {
			tamper()
			resp, body := getArtifactContent(t, env, generated.runID, generated.artifact.ArtifactID)
			if resp.StatusCode != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500; body=%s", resp.StatusCode, body)
			}
			if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "image/") {
				t.Errorf("Content-Type = %q, want no image response", ct)
			}
			if bytes.Contains(body, original[:8]) || bytes.Contains(body, []byte(generated.artifact.SHA256)) {
				t.Errorf("500 body carries content or its digest: %q", body)
			}
			code := decodeBody[struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}](t, body).Error.Code
			if code != "INTERNAL" {
				t.Errorf("error code = %q, want INTERNAL", code)
			}
		})
	}

	logged := logs.String()
	if !strings.Contains(logged, generated.artifact.ArtifactID) {
		t.Errorf("log does not name the artifact id: %s", logged)
	}
	for _, leak := range []string{generated.storageKey, env.assetRoot, "artifacts/" + generated.artifact.SHA256[:2]} {
		if strings.Contains(logged, leak) {
			t.Errorf("log leaks %q: %s", leak, logged)
		}
	}
}

// TestArtifactContent_Read_WritesNoEventOrState covers the read's side effects: reading
// an artifact any number of times leaves every Event, Run, Attempt and artifact row as it
// was.
func TestArtifactContent_Read_WritesNoEventOrState(t *testing.T) {
	env := newArtifactContentEnv(t, &syncBuffer{})
	generated := runWithGeneratedArtifact(t, env)

	before := executionStateDigest(t, env, generated.runID)
	for i := 0; i < 3; i++ {
		if resp, body := getArtifactContent(t, env, generated.runID, generated.artifact.ArtifactID); resp.StatusCode != http.StatusOK {
			t.Fatalf("read %d status = %d; body=%s", i, resp.StatusCode, body)
		}
	}
	assertArtifactNotFound(t, env, generated.runID, "artifact_"+strings.Repeat("f", 32))
	if after := executionStateDigest(t, env, generated.runID); after != before {
		t.Errorf("execution state changed by reads:\nbefore %s\nafter  %s", before, after)
	}
}

// executionStateDigest summarises everything a read must not touch.
func executionStateDigest(t *testing.T, env *testEnv, runID string) string {
	t.Helper()
	ctx := context.Background()
	var lastSeq int64
	var status string
	var updatedAt time.Time
	if err := env.pool.QueryRow(ctx, `SELECT last_seq, status, updated_at FROM runs WHERE id = $1`, runID).Scan(&lastSeq, &status, &updatedAt); err != nil {
		t.Fatalf("read run: %v", err)
	}
	counts := make([]string, 0, 6)
	for _, table := range []string{"events", "node_runs", "tool_attempts", "execution_facts", "execution_artifacts", "tool_attempt_artifacts"} {
		var n int
		if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts = append(counts, table+"="+strconv.Itoa(n))
	}
	return strconv.FormatInt(lastSeq, 10) + " " + status + " " + updatedAt.UTC().Format(time.RFC3339Nano) + " " + strings.Join(counts, " ")
}

// gradientPNG renders a small deterministic gradient; seed shifts its colours so each seed
// yields different bytes.
func gradientPNG(t *testing.T, seed uint8) []byte {
	t.Helper()
	const side = 8
	canvas := image.NewRGBA(image.Rect(0, 0, side, side))
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			canvas.SetRGBA(x, y, color.RGBA{R: uint8(x*32) + seed, G: uint8(y * 32), B: 0x80 + seed, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		t.Fatalf("encode gradient: %v", err)
	}
	return buf.Bytes()
}

// replaceObject overwrites a stored object, which the store keeps read-only.
func replaceObject(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove stored object: %v", err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("replace stored object: %v", err)
	}
}
