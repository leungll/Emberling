//go:build integration

// Package integration: this file covers the node_attempt_artifacts table, the link by
// which a Node Attempt's committed output makes an Execution Artifact readable through
// its Run. Its guarantees are a PRIMARY KEY, two foreign keys and the SUCCEEDED condition
// of the Run-scoped read, which can only be demonstrated against a real PostgreSQL.
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

// nodeArtifactSHA is the digest of the text material these tests declare.
const nodeArtifactSHA = "3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a"

// nodeArtifactFixture is a text artifact record, as the transcript node declares one.
func nodeArtifactFixture(t *testing.T) store.ArtifactRecord {
	t.Helper()
	record := artifactRecordFixture(t, nodeArtifactSHA)
	record.Artifact.MediaType = domain.ArtifactMediaTypeTextPlain
	return record
}

// linkNodeArtifact commits the artifact's metadata row and its link to attemptID in one
// transaction, as the node result transaction does.
func linkNodeArtifact(ctx context.Context, uow store.UnitOfWork, record store.ArtifactRecord, attemptID, runID string) error {
	return uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.Artifacts().CreateIfAbsent(ctx, record); err != nil {
			return err
		}
		return tx.Artifacts().LinkToNodeAttempt(ctx, store.NodeAttemptArtifact{
			NodeAttemptID: attemptID, ArtifactID: record.Artifact.ArtifactID, RunID: runID, CreatedAt: fixtureTime,
		})
	})
}

func readArtifactForRun(ctx context.Context, uow store.UnitOfWork, runID, artifactID string) (store.ArtifactRecord, error) {
	var record store.ArtifactRecord
	err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		record, err = tx.Artifacts().GetForRun(ctx, runID, artifactID)
		return err
	})
	return record, err
}

func markNodeAttempt(ctx context.Context, t *testing.T, uow store.UnitOfWork, attemptID string, status domain.NodeAttemptStatus) {
	t.Helper()
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		switch status {
		case domain.NodeAttemptSucceeded:
			return tx.NodeAttempts().MarkSucceeded(ctx, attemptID, domain.NodeAttemptStarted, fixtureTime, []byte(`{}`), nil)
		case domain.NodeAttemptFailed:
			return tx.NodeAttempts().MarkFailed(ctx, attemptID, domain.NodeAttemptStarted, fixtureTime,
				domain.ExecutionError{Code: "TEST", Message: "failed"}, false)
		default:
			return errors.New("unsupported status for the fixture")
		}
	}); err != nil {
		t.Fatalf("mark attempt %s %s: %v", attemptID, status, err)
	}
}

// TestArtifactRepository_GetForRun_SucceededNodeAttemptLink_ReturnsTheArtifact: a link
// written by a Node Attempt makes the artifact readable through its Run once the Attempt
// SUCCEEDED, exactly as a Tool Attempt link does.
func TestArtifactRepository_GetForRun_SucceededNodeAttemptLink_ReturnsTheArtifact(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	_, attemptID := seedAttempt(ctx, t, uow, f, "transcript", domain.NodeRunRunning, false)
	record := nodeArtifactFixture(t)

	if err := linkNodeArtifact(ctx, uow, record, attemptID, f.runID); err != nil {
		t.Fatalf("link node artifact: %v", err)
	}
	markNodeAttempt(ctx, t, uow, attemptID, domain.NodeAttemptSucceeded)

	got, err := readArtifactForRun(ctx, uow, f.runID, record.Artifact.ArtifactID)
	if err != nil {
		t.Fatalf("GetForRun: %v", err)
	}
	if got.Artifact.SHA256 != record.Artifact.SHA256 || got.StorageKey != record.StorageKey ||
		got.Artifact.MediaType != domain.ArtifactMediaTypeTextPlain {
		t.Fatalf("GetForRun = %+v, want the linked text artifact %+v", got, record)
	}
}

// TestArtifactRepository_GetForRun_NodeAttemptNotSucceeded_NotFound: a link whose Node
// Attempt is still STARTED, or ended FAILED, does not make the artifact readable.
func TestArtifactRepository_GetForRun_NodeAttemptNotSucceeded_NotFound(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	_, attemptID := seedAttempt(ctx, t, uow, f, "transcript", domain.NodeRunRunning, false)
	record := nodeArtifactFixture(t)
	if err := linkNodeArtifact(ctx, uow, record, attemptID, f.runID); err != nil {
		t.Fatalf("link node artifact: %v", err)
	}

	if _, err := readArtifactForRun(ctx, uow, f.runID, record.Artifact.ArtifactID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetForRun while the Attempt is STARTED: err = %v, want ErrNotFound", err)
	}
	markNodeAttempt(ctx, t, uow, attemptID, domain.NodeAttemptFailed)
	if _, err := readArtifactForRun(ctx, uow, f.runID, record.Artifact.ArtifactID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetForRun after the Attempt FAILED: err = %v, want ErrNotFound", err)
	}
}

// TestArtifactRepository_GetForRun_OtherRun_NotFound: the link is Run-scoped, so another
// Run reads nothing through it even though the artifact row is shared.
func TestArtifactRepository_GetForRun_OtherRun_NotFound(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	_, attemptID := seedAttempt(ctx, t, uow, f, "transcript", domain.NodeRunRunning, false)
	record := nodeArtifactFixture(t)
	if err := linkNodeArtifact(ctx, uow, record, attemptID, f.runID); err != nil {
		t.Fatalf("link node artifact: %v", err)
	}
	markNodeAttempt(ctx, t, uow, attemptID, domain.NodeAttemptSucceeded)

	const otherRunID = "run_other"
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Runs().Create(ctx, domain.Run{
			ID: otherRunID, WorkflowID: f.workflowID, DefinitionVersion: 1, Status: domain.RunRunning,
			Input: []byte(`{}`), StartedAt: fixtureTime, UpdatedAt: fixtureTime,
		})
	}); err != nil {
		t.Fatalf("create other run: %v", err)
	}
	if _, err := readArtifactForRun(ctx, uow, otherRunID, record.Artifact.ArtifactID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetForRun from another Run: err = %v, want ErrNotFound", err)
	}
	if _, err := readArtifactForRun(ctx, uow, "run_missing", record.Artifact.ArtifactID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetForRun from a missing Run: err = %v, want ErrNotFound", err)
	}
}

// TestArtifactRepository_LinkToNodeAttempt_Duplicate_Conflicts: the result transaction
// writes each link once; a second link of the same Attempt and artifact is a defect.
func TestArtifactRepository_LinkToNodeAttempt_Duplicate_Conflicts(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	_, attemptID := seedAttempt(ctx, t, uow, f, "transcript", domain.NodeRunRunning, false)
	record := nodeArtifactFixture(t)
	if err := linkNodeArtifact(ctx, uow, record, attemptID, f.runID); err != nil {
		t.Fatalf("link node artifact: %v", err)
	}

	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Artifacts().LinkToNodeAttempt(ctx, store.NodeAttemptArtifact{
			NodeAttemptID: attemptID, ArtifactID: record.Artifact.ArtifactID, RunID: f.runID, CreatedAt: fixtureTime.Add(time.Second),
		})
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second link: err = %v, want ErrConflict", err)
	}
}

// TestArtifactRepository_LinkToNodeAttempt_MissingArtifactRow_Fails: a link needs the
// artifact's metadata row in the same transaction; the foreign key refuses one without.
func TestArtifactRepository_LinkToNodeAttempt_MissingArtifactRow_Fails(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	_, attemptID := seedAttempt(ctx, t, uow, f, "transcript", domain.NodeRunRunning, false)
	record := nodeArtifactFixture(t)

	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Artifacts().LinkToNodeAttempt(ctx, store.NodeAttemptArtifact{
			NodeAttemptID: attemptID, ArtifactID: record.Artifact.ArtifactID, RunID: f.runID, CreatedAt: fixtureTime,
		})
	})
	if err == nil || !strings.Contains(err.Error(), "LinkToNodeAttempt") {
		t.Fatalf("link without a metadata row: err = %v, want a LinkToNodeAttempt store error", err)
	}
}
