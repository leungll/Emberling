//go:build integration

// Package integration: this file covers the execution_artifacts table. Its deduplication
// guarantee is PRIMARY KEY (artifact_id) with ON CONFLICT DO NOTHING, so it can only be
// demonstrated against a real PostgreSQL.
package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

func artifactRecordFixture(t *testing.T, sha string) store.ArtifactRecord {
	t.Helper()
	id, err := domain.ArtifactIDForSHA256(sha)
	if err != nil {
		t.Fatalf("ArtifactIDForSHA256: %v", err)
	}
	return store.ArtifactRecord{
		Artifact: domain.Artifact{
			ArtifactID: id,
			MediaType:  "image/png",
			SizeBytes:  2048,
			SHA256:     sha,
			CreatedAt:  fixtureTime,
		},
		StorageKey: "artifacts/" + sha[:2] + "/" + sha,
	}
}

func createArtifact(ctx context.Context, uow *postgres.UnitOfWork, record store.ArtifactRecord) error {
	return uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Artifacts().CreateIfAbsent(ctx, record)
	})
}

func countArtifactRows(ctx context.Context, t *testing.T, uow *postgres.UnitOfWork, artifactID string) int {
	t.Helper()
	var n int
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.Artifacts().Get(ctx, artifactID)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err == nil {
			n = 1
		}
		return err
	}); err != nil {
		t.Fatalf("Get %s: %v", artifactID, err)
	}
	return n
}

func TestArtifactRepository_CreateIfAbsent_Absent_InsertsTheRow(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	want := artifactRecordFixture(t, strings.Repeat("a1", 32))

	if err := createArtifact(ctx, uow, want); err != nil {
		t.Fatalf("CreateIfAbsent: %v", err)
	}

	var got store.ArtifactRecord
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.Artifacts().Get(ctx, want.Artifact.ArtifactID)
		return err
	}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Artifact.Ref() != want.Artifact.Ref() || !got.Artifact.CreatedAt.Equal(want.Artifact.CreatedAt) {
		t.Errorf("metadata: got %+v, want %+v", got.Artifact, want.Artifact)
	}
	if got.StorageKey != want.StorageKey {
		t.Errorf("storage key: got %q, want %q", got.StorageKey, want.StorageKey)
	}
}

func TestArtifactRepository_CreateIfAbsent_IdenticalPresent_KeepsTheFirstRow(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	first := artifactRecordFixture(t, strings.Repeat("b2", 32))
	if err := createArtifact(ctx, uow, first); err != nil {
		t.Fatalf("first CreateIfAbsent: %v", err)
	}

	second := first
	second.Artifact.CreatedAt = fixtureTime.Add(time.Hour)
	if err := createArtifact(ctx, uow, second); err != nil {
		t.Fatalf("second CreateIfAbsent: %v", err)
	}

	var got store.ArtifactRecord
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.Artifacts().Get(ctx, first.Artifact.ArtifactID)
		return err
	}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Artifact.CreatedAt.Equal(first.Artifact.CreatedAt) {
		t.Errorf("createdAt: got %s, want the first row's %s", got.Artifact.CreatedAt, first.Artifact.CreatedAt)
	}
	if n := countArtifactRows(ctx, t, uow, first.Artifact.ArtifactID); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}

func TestArtifactRepository_CreateIfAbsent_MismatchingPresent_Conflicts(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	first := artifactRecordFixture(t, strings.Repeat("c3", 32))
	if err := createArtifact(ctx, uow, first); err != nil {
		t.Fatalf("first CreateIfAbsent: %v", err)
	}

	mutations := map[string]func(*store.ArtifactRecord){
		"size":       func(r *store.ArtifactRecord) { r.Artifact.SizeBytes = 4096 },
		"media type": func(r *store.ArtifactRecord) { r.Artifact.MediaType = "image/webp" },
		"sha256":     func(r *store.ArtifactRecord) { r.Artifact.SHA256 = strings.Repeat("c3", 16) + strings.Repeat("ff", 16) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			second := first
			mutate(&second)
			err := createArtifact(ctx, uow, second)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("CreateIfAbsent error = %v, want ErrConflict", err)
			}
			if strings.Contains(err.Error(), first.StorageKey) {
				t.Fatalf("conflict error leaks the storage key: %v", err)
			}
		})
	}

	var got store.ArtifactRecord
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.Artifacts().Get(ctx, first.Artifact.ArtifactID)
		return err
	}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Artifact.Ref() != first.Artifact.Ref() {
		t.Errorf("the committed row changed: got %+v, want %+v", got.Artifact, first.Artifact)
	}
}
