//go:build integration

// Artifact sweep tests: the operator sweeper run as its built binary against the same
// PostgreSQL facts and storage root a Backend used. An object no execution_artifacts row
// references is reported once it is older than the grace period and removed only with
// --delete; a referenced object and anything within the grace period are never touched.
package integration

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// buildArtifactSweep builds the sweeper binary into a test directory.
func buildArtifactSweep(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "artifactsweep")
	build := exec.Command("go", "build", "-o", binary, "github.com/leungll/Emberling/backend/cmd/artifactsweep")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build artifactsweep: %v\n%s", err, out)
	}
	return binary
}

// runArtifactSweep runs the sweeper against h's database and root and returns its
// standard output, failing the test on a non-zero exit.
func runArtifactSweep(t *testing.T, h *agentHarness, binary, root string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(h.ctx, binary, args...)
	cmd.Env = append(os.Environ(),
		config.KeyDatabaseURL+"="+h.pool.Config().ConnString(),
		config.KeyAssetStorageRoot+"="+root,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("artifactsweep %v: %v\nstderr: %s", args, err, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "artifacts/") || strings.Contains(out, root) {
		t.Fatalf("artifactsweep output exposes a storage key or path:\n%s", out)
	}
	return out
}

func ageFile(t *testing.T, path string, age time.Duration) {
	t.Helper()
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("age %s: %v", filepath.Base(path), err)
	}
}

func assertSweepLine(t *testing.T, out, line string) {
	t.Helper()
	for _, got := range strings.Split(out, "\n") {
		if got == line {
			return
		}
	}
	t.Errorf("sweep output lacks %q:\n%s", line, out)
}

// TestArtifactSweep_OrphanFromRolledBackResult_ReportedThenDeletedPastGrace covers the
// whole life of an orphan: a generation whose result transaction rolled back leaves its
// saved object without a row. The sweeper ignores it within the grace period, reports it
// in a dry run once it is older, and removes it only with --delete, while an equally old
// object that a committed row references survives every pass. An old staging leftover is
// treated the same way.
func TestArtifactSweep_OrphanFromRolledBackResult_ReportedThenDeletedPastGrace(t *testing.T) {
	h, executor, orphan := orphanArtifactByResultRollback(t, "wf-artifact-sweep")
	root := executor.artifactRoot
	binary := buildArtifactSweep(t)

	referenced, err := executor.artifacts.Write(h.ctx, bytes.NewReader([]byte("\x89PNG\r\n\x1a\nreferenced artifact")))
	if err != nil {
		t.Fatalf("save the referenced artifact: %v", err)
	}
	referencedKey, _ := asset.ArtifactStorageKey(referenced.SHA256)
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Artifacts().CreateIfAbsent(ctx, store.ArtifactRecord{
			Artifact: domain.Artifact{
				ArtifactID: referenced.ArtifactID, MediaType: referenced.MediaType,
				SizeBytes: referenced.SizeBytes, SHA256: referenced.SHA256, CreatedAt: h.clock.Now(),
			},
			StorageKey: referencedKey,
		})
	}); err != nil {
		t.Fatalf("commit the referenced artifact row: %v", err)
	}
	leftover := filepath.Join(root, "artifacts", ".staging", "artifact-interrupted")
	if err := os.WriteFile(leftover, []byte("partial"), 0o600); err != nil {
		t.Fatalf("write staged leftover: %v", err)
	}

	out := runArtifactSweep(t, h, binary, root)
	assertSweepLine(t, out, "mode: dry-run")
	assertSweepLine(t, out, "objects within grace: 2")
	assertSweepLine(t, out, "orphaned objects: 0")
	assertSweepLine(t, out, "staged leftovers past grace: 0")

	orphanPath := filepath.Join(root, filepath.FromSlash(orphan.StorageKey))
	referencedPath := filepath.Join(root, filepath.FromSlash(referencedKey))
	for _, path := range []string{orphanPath, referencedPath, leftover} {
		ageFile(t, path, 48*time.Hour)
	}

	out = runArtifactSweep(t, h, binary, root)
	assertSweepLine(t, out, "objects referenced: 1")
	assertSweepLine(t, out, "orphaned objects: 1")
	assertSweepLine(t, out, "orphan: "+orphan.ArtifactID)
	assertSweepLine(t, out, "objects deleted: 0")
	assertSweepLine(t, out, "staged leftovers past grace: 1")
	for _, path := range []string{orphanPath, referencedPath, leftover} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("dry run removed %s: %v", filepath.Base(path), err)
		}
	}

	out = runArtifactSweep(t, h, binary, root, "--delete")
	assertSweepLine(t, out, "mode: delete")
	assertSweepLine(t, out, "deleted: "+orphan.ArtifactID)
	assertSweepLine(t, out, "objects deleted: 1")
	assertSweepLine(t, out, "staged leftovers deleted: 1")
	for _, path := range []string{orphanPath, leftover} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s survived --delete: %v", filepath.Base(path), err)
		}
	}
	if _, err := os.Stat(referencedPath); err != nil {
		t.Errorf("the referenced object was removed: %v", err)
	}
	if got := countArtifactRowsByID(t, h, referenced.ArtifactID); got != 1 {
		t.Errorf("rows for the referenced artifact = %d, want 1", got)
	}

	out = runArtifactSweep(t, h, binary, root, "--delete")
	assertSweepLine(t, out, "objects scanned: 1")
	assertSweepLine(t, out, "objects deleted: 0")
}
