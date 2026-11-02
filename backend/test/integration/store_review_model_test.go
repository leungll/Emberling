//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// TestAgentRunStore_ReviewModelID_RoundTripsNullAndValue covers the frozen review model
// column: an Agent Run without one reads back nil, and a selected one reads back verbatim.
func TestAgentRunStore_ReviewModelID_RoundTripsNullAndValue(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	createNodeRun(ctx, t, uow, newNodeRun("nr_reviewed", f.runID, "node_reviewed"))

	createAgentRun(ctx, t, uow, newAgentRun("ar_mock_review", f.nodeRunID))
	model := "vision-review-1.0"
	reviewed := newAgentRun("ar_model_review", "nr_reviewed")
	reviewed.ReviewModelID = &model
	createAgentRun(ctx, t, uow, reviewed)

	if got := getAgentRun(ctx, t, uow, "ar_mock_review").ReviewModelID; got != nil {
		t.Fatalf("no review model selected: want nil, got %q", *got)
	}
	if got := getAgentRun(ctx, t, uow, "ar_model_review").ReviewModelID; got == nil || *got != model {
		t.Fatalf("frozen review model: want %q, got %v", model, got)
	}
}
