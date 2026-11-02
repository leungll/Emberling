//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// storeIdemRun is one Run row of workflowID carrying the given submission key and digest.
func storeIdemRun(id, workflowID, key, digest string) domain.Run {
	return domain.Run{
		ID:                id,
		WorkflowID:        workflowID,
		DefinitionVersion: 1,
		Status:            domain.RunRunning,
		Input:             json.RawMessage(`{"brief":"a small ember creature"}`),
		StartedAt:         fixtureTime,
		UpdatedAt:         fixtureTime,
		IdempotencyKey:    key,
		IdempotencyDigest: digest,
	}
}

func storeIdemSaveDefinitions(ctx context.Context, t *testing.T, uow store.UnitOfWork, workflowIDs ...string) {
	t.Helper()
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		for _, id := range workflowIDs {
			if err := tx.Definitions().Save(ctx, newDefinition(id, 1, "Idempotency")); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("save definitions: %v", err)
	}
}

func storeIdemCreate(ctx context.Context, uow store.UnitOfWork, run domain.Run) error {
	return uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Runs().Create(ctx, run)
	})
}

func TestRunStore_SameKeySameWorkflow_SecondInsertRejectedAsKeyInUse(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	storeIdemSaveDefinitions(ctx, t, uow, "wf_idem_store")

	if err := storeIdemCreate(ctx, uow, storeIdemRun("run_idem_a", "wf_idem_store", "key-1", "sha256:a")); err != nil {
		t.Fatalf("create first keyed run: %v", err)
	}
	err := storeIdemCreate(ctx, uow, storeIdemRun("run_idem_b", "wf_idem_store", "key-1", "sha256:a"))
	if !errors.Is(err, domain.ErrRunIdempotencyKeyInUse) {
		t.Fatalf("second insert with the same key: want domain.ErrRunIdempotencyKeyInUse, got %v", err)
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second insert with the same key: want it to wrap domain.ErrConflict, got %v", err)
	}

	var got domain.Run
	if txErr := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.Runs().GetByIdempotencyKey(ctx, "wf_idem_store", "key-1")
		return err
	}); txErr != nil {
		t.Fatalf("get by idempotency key: %v", txErr)
	}
	if got.ID != "run_idem_a" || got.IdempotencyKey != "key-1" || got.IdempotencyDigest != "sha256:a" {
		t.Fatalf("get by idempotency key: want run_idem_a with key-1/sha256:a, got id=%s key=%q digest=%q",
			got.ID, got.IdempotencyKey, got.IdempotencyDigest)
	}
	if fetched := getRun(ctx, t, uow, "run_idem_a"); fetched.IdempotencyKey != "key-1" || fetched.IdempotencyDigest != "sha256:a" {
		t.Fatalf("Get: want the stored key and digest mapped back, got key=%q digest=%q",
			fetched.IdempotencyKey, fetched.IdempotencyDigest)
	}
}

func TestRunStore_SameKeyDifferentWorkflows_BothInserted(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	storeIdemSaveDefinitions(ctx, t, uow, "wf_idem_one", "wf_idem_two")

	if err := storeIdemCreate(ctx, uow, storeIdemRun("run_idem_one", "wf_idem_one", "shared", "sha256:a")); err != nil {
		t.Fatalf("create run in first workflow: %v", err)
	}
	if err := storeIdemCreate(ctx, uow, storeIdemRun("run_idem_two", "wf_idem_two", "shared", "sha256:a")); err != nil {
		t.Fatalf("create run in second workflow with the same key: want accepted, got %v", err)
	}

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		for workflowID, wantID := range map[string]string{"wf_idem_one": "run_idem_one", "wf_idem_two": "run_idem_two"} {
			got, err := tx.Runs().GetByIdempotencyKey(ctx, workflowID, "shared")
			if err != nil {
				return err
			}
			if got.ID != wantID {
				t.Errorf("get by key in %s: want %s, got %s", workflowID, wantID, got.ID)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read keyed runs: %v", err)
	}
}

func TestRunStore_UnkeyedRuns_NotConstrainedByKeyIndex(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	storeIdemSaveDefinitions(ctx, t, uow, "wf_idem_unkeyed")

	for _, id := range []string{"run_unkeyed_a", "run_unkeyed_b"} {
		if err := storeIdemCreate(ctx, uow, storeIdemRun(id, "wf_idem_unkeyed", "", "")); err != nil {
			t.Fatalf("create unkeyed run %s: %v", id, err)
		}
	}
	if got := getRun(ctx, t, uow, "run_unkeyed_a"); got.IdempotencyKey != "" || got.IdempotencyDigest != "" {
		t.Fatalf("unkeyed run: want empty key and digest, got key=%q digest=%q", got.IdempotencyKey, got.IdempotencyDigest)
	}
}

func TestRunStore_KeyWithoutDigest_RejectedByPairCheck(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	storeIdemSaveDefinitions(ctx, t, uow, "wf_idem_pair")

	for name, run := range map[string]domain.Run{
		"key without digest": storeIdemRun("run_pair_a", "wf_idem_pair", "key-1", ""),
		"digest without key": storeIdemRun("run_pair_b", "wf_idem_pair", "", "sha256:a"),
	} {
		err := storeIdemCreate(ctx, uow, run)
		if !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("%s: want the pair CHECK to reject the row as domain.ErrConflict, got %v", name, err)
		}
		if errors.Is(err, domain.ErrRunIdempotencyKeyInUse) {
			t.Fatalf("%s: a CHECK failure must not be reported as a key already in use: %v", name, err)
		}
	}
}

func TestRunStore_GetByIdempotencyKey_UnknownKey_NotFound(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	storeIdemSaveDefinitions(ctx, t, uow, "wf_idem_missing")

	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.Runs().GetByIdempotencyKey(ctx, "wf_idem_missing", "never-used")
		return err
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("get by unknown key: want domain.ErrNotFound, got %v", err)
	}
}
