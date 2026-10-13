//go:build integration

// Package integration: this file covers the Run list read path (QueryService.ListRuns and
// the Definitions list's last-Run failure fields) against PostgreSQL: the keyset order and
// its tie-breaker, paging in both directions, stability under concurrent inserts, the
// status counts and filters, and the failed or waiting step read from committed facts.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// listDefinition is a two-step Definition whose node names the failed and waiting steps
// are read from.
func listDefinition(workflowID string, version int, renderName string) domain.Definition {
	def := newDefinition(workflowID, version, "List "+workflowID)
	def.Nodes = []domain.Node{
		{ID: "node_draft", Type: "text_input", Name: "Draft caption", Config: json.RawMessage(`{}`)},
		{ID: "node_render", Type: "text_input", Name: renderName, Config: json.RawMessage(`{}`)},
	}
	return def
}

type listNode struct {
	nodeRunID string
	nodeID    string
	status    domain.NodeRunStatus
	waitingAt *time.Time
}

type listRunSpec struct {
	id        string
	workflow  string
	version   int
	status    domain.RunStatus
	startedAt time.Time
	errorCode string
	nodes     []listNode
	// failedNodeRuns are appended as NODE_FAILED Events in this order.
	failedNodeRuns []string
}

func seedListRun(ctx context.Context, t *testing.T, uow store.UnitOfWork, spec listRunSpec) {
	t.Helper()
	if spec.version == 0 {
		spec.version = 1
	}
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		run := domain.Run{
			ID:                spec.id,
			WorkflowID:        spec.workflow,
			DefinitionVersion: spec.version,
			Status:            spec.status,
			Input:             json.RawMessage(`{}`),
			StartedAt:         spec.startedAt,
			UpdatedAt:         spec.startedAt,
		}
		if spec.status.IsTerminal() {
			done := spec.startedAt.Add(time.Second)
			run.CompletedAt = &done
		}
		if spec.errorCode != "" {
			run.Error = &domain.ExecutionError{Code: spec.errorCode, Message: "secret detail that a list must not carry"}
		}
		if err := tx.Runs().Create(ctx, run); err != nil {
			return err
		}
		for _, n := range spec.nodes {
			nr := newNodeRun(n.nodeRunID, spec.id, n.nodeID)
			nr.Status = n.status
			nr.WaitingAt = n.waitingAt
			if err := tx.NodeRuns().Create(ctx, nr); err != nil {
				return err
			}
		}
		if len(spec.failedNodeRuns) == 0 {
			return nil
		}
		lock, err := tx.Runs().LockForUpdate(ctx, spec.id)
		if err != nil {
			return err
		}
		for _, nodeRunID := range spec.failedNodeRuns {
			ev := newEvent(spec.id, domain.EventNodeFailed)
			id := nodeRunID
			ev.NodeRunID = &id
			if _, err := tx.Events().Append(ctx, lock, ev); err != nil {
				return err
			}
		}
		return tx.Runs().UpdateAggregate(ctx, lock, spec.status, spec.startedAt.Add(time.Second))
	})
	if err != nil {
		t.Fatalf("seed list run %s: %v", spec.id, err)
	}
}

func listRuns(ctx context.Context, t *testing.T, svc *service.QueryService, req service.ListRunsRequest) service.RunListPage {
	t.Helper()
	page, err := svc.ListRuns(ctx, req)
	if err != nil {
		t.Fatalf("ListRuns(%+v): %v", req, err)
	}
	return page
}

func pageIDs(page service.RunListPage) []string {
	ids := make([]string, len(page.Items))
	for i, item := range page.Items {
		ids[i] = item.ID
	}
	return ids
}

// TestRunList_TiedStartTimes_PagesBothWaysWithoutDuplicateOrGap seeds seven Runs, four of
// them started in the same microsecond, and walks the list forward with nextCursor and
// back with prevCursor at a page size that splits the tie.
func TestRunList_TiedStartTimes_PagesBothWaysWithoutDuplicateOrGap(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	saveDefinition(ctx, t, uow, listDefinition("wf_list", 1, "Render image"))
	svc := service.NewQueryService(service.Deps{UoW: uow})

	tie := fixtureTime.Add(time.Hour)
	starts := map[string]time.Time{
		"run_a0": fixtureTime, "run_b1": tie, "run_b2": tie, "run_b3": tie, "run_b4": tie,
		"run_c0": tie.Add(time.Minute), "run_d0": tie.Add(2 * time.Minute),
	}
	for id, at := range starts {
		seedListRun(ctx, t, uow, listRunSpec{id: id, workflow: "wf_list", status: domain.RunCompleted, startedAt: at})
	}
	want := []string{"run_d0", "run_c0", "run_b4", "run_b3", "run_b2", "run_b1", "run_a0"}

	var (
		forward []string
		pages   []service.RunListPage
		cursor  string
	)
	for i := 0; ; i++ {
		page := listRuns(ctx, t, svc, service.ListRunsRequest{Limit: 3, Cursor: cursor})
		if i == 0 && page.PrevCursor != nil {
			t.Errorf("first page prevCursor = %q, want nil", *page.PrevCursor)
		}
		if i > 0 && page.PrevCursor == nil {
			t.Errorf("page %d prevCursor = nil, want a cursor back to newer Runs", i)
		}
		forward = append(forward, pageIDs(page)...)
		pages = append(pages, page)
		if page.NextCursor == nil {
			break
		}
		if i > len(want) {
			t.Fatal("paging forward did not terminate")
		}
		cursor = *page.NextCursor
	}
	if !slices.Equal(forward, want) {
		t.Fatalf("forward walk = %v, want %v", forward, want)
	}
	if len(pages) != 3 {
		t.Fatalf("pages = %d, want 3", len(pages))
	}

	// Walking back from the last page with prevCursor returns the earlier pages exactly.
	cursor = *pages[2].PrevCursor
	for i := 1; i >= 0; i-- {
		page := listRuns(ctx, t, svc, service.ListRunsRequest{Limit: 3, Cursor: cursor})
		if got, wantIDs := pageIDs(page), pageIDs(pages[i]); !slices.Equal(got, wantIDs) {
			t.Fatalf("backward page %d = %v, want %v", i, got, wantIDs)
		}
		if page.NextCursor == nil {
			t.Fatalf("backward page %d nextCursor = nil, want a cursor to older Runs", i)
		}
		if i == 0 {
			if page.PrevCursor != nil {
				t.Errorf("backward first page prevCursor = %q, want nil", *page.PrevCursor)
			}
			break
		}
		cursor = *page.PrevCursor
	}
}

// TestRunList_RunCreatedWhilePaging_IssuedCursorsUnaffected creates Runs after the first
// page was read, including one tied with the page boundary, and proves the issued cursors
// still name the same pages in both directions.
func TestRunList_RunCreatedWhilePaging_IssuedCursorsUnaffected(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	saveDefinition(ctx, t, uow, listDefinition("wf_list", 1, "Render image"))
	svc := service.NewQueryService(service.Deps{UoW: uow})

	for i := 1; i <= 5; i++ {
		seedListRun(ctx, t, uow, listRunSpec{
			id: fmt.Sprintf("run_%02d", i), workflow: "wf_list", status: domain.RunRunning,
			startedAt: fixtureTime.Add(time.Duration(i) * time.Minute),
		})
	}
	first := listRuns(ctx, t, svc, service.ListRunsRequest{Limit: 2})
	if !slices.Equal(pageIDs(first), []string{"run_05", "run_04"}) {
		t.Fatalf("first page = %v", pageIDs(first))
	}

	// A newer Run, and one in the same microsecond as the boundary with a larger ID: both
	// sort before the boundary and must not appear on the following page.
	seedListRun(ctx, t, uow, listRunSpec{id: "run_99", workflow: "wf_list", status: domain.RunRunning, startedAt: fixtureTime.Add(time.Hour)})
	seedListRun(ctx, t, uow, listRunSpec{id: "run_04z", workflow: "wf_list", status: domain.RunRunning, startedAt: fixtureTime.Add(4 * time.Minute)})

	second := listRuns(ctx, t, svc, service.ListRunsRequest{Limit: 2, Cursor: *first.NextCursor})
	if !slices.Equal(pageIDs(second), []string{"run_03", "run_02"}) {
		t.Fatalf("second page after concurrent inserts = %v, want [run_03 run_02]", pageIDs(second))
	}
	back := listRuns(ctx, t, svc, service.ListRunsRequest{Limit: 2, Cursor: *second.PrevCursor})
	if !slices.Equal(pageIDs(back), []string{"run_04z", "run_04"}) {
		t.Fatalf("page before the second = %v, want the two Runs nearest to it [run_04z run_04]", pageIDs(back))
	}
	if back.PrevCursor == nil {
		t.Fatal("page before the second: prevCursor = nil, want newer Runs to remain reachable")
	}
	if second.Counts.All != 7 || second.Counts.Running != 7 {
		t.Errorf("counts = %+v, want all and running 7", second.Counts)
	}
}

// TestRunList_FiltersAndCounts covers every filter, the counts' independence from the
// status filter, and the empty result of an unknown Workflow.
func TestRunList_FiltersAndCounts(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	saveDefinition(ctx, t, uow, listDefinition("wf_list", 1, "Render image"))
	saveDefinition(ctx, t, uow, listDefinition("wf_other", 1, "Render image"))
	svc := service.NewQueryService(service.Deps{UoW: uow})

	at := func(m int) time.Time { return fixtureTime.Add(time.Duration(m) * time.Minute) }
	for _, spec := range []listRunSpec{
		{id: "run_aaaaaaaa01", workflow: "wf_list", status: domain.RunRunning, startedAt: at(1)},
		{id: "run_aaaaaaaa02", workflow: "wf_list", status: domain.RunPaused, startedAt: at(2)},
		{id: "run_aaaaaaab03", workflow: "wf_list", status: domain.RunCompleted, startedAt: at(3)},
		{id: "run_bbbbbbbb04", workflow: "wf_list", status: domain.RunFailed, startedAt: at(4), errorCode: "TOOL_ERROR"},
		{id: "run_bbbbbbbb05", workflow: "wf_list", status: domain.RunCompleted, startedAt: at(5)},
		{id: "run_cccccccc06", workflow: "wf_other", status: domain.RunCompleted, startedAt: at(6)},
		// A Run whose ID shares the prefix only through a LIKE wildcard must never match.
		{id: "runxaaaaaaaa07", workflow: "wf_list", status: domain.RunCompleted, startedAt: at(7)},
	} {
		seedListRun(ctx, t, uow, spec)
	}

	all := listRuns(ctx, t, svc, service.ListRunsRequest{})
	if want := (service.RunStatusCounts{All: 7, Running: 1, Paused: 1, Completed: 4, Failed: 1}); all.Counts != want {
		t.Errorf("unfiltered counts = %+v, want %+v", all.Counts, want)
	}

	failedOnly := listRuns(ctx, t, svc, service.ListRunsRequest{WorkflowID: "wf_list", Status: "FAILED"})
	if !slices.Equal(pageIDs(failedOnly), []string{"run_bbbbbbbb04"}) {
		t.Errorf("status=FAILED items = %v", pageIDs(failedOnly))
	}
	if want := (service.RunStatusCounts{All: 6, Running: 1, Paused: 1, Completed: 3, Failed: 1}); failedOnly.Counts != want {
		t.Errorf("status filter changed the counts: %+v, want %+v", failedOnly.Counts, want)
	}

	from := at(4)
	recent := listRuns(ctx, t, svc, service.ListRunsRequest{WorkflowID: "wf_list", StartedAfter: &from})
	if !slices.Equal(pageIDs(recent), []string{"runxaaaaaaaa07", "run_bbbbbbbb05", "run_bbbbbbbb04"}) {
		t.Errorf("startedAfter (inclusive) items = %v", pageIDs(recent))
	}
	if recent.Counts.All != 3 {
		t.Errorf("startedAfter counts = %+v, want all 3", recent.Counts)
	}

	byCode := listRuns(ctx, t, svc, service.ListRunsRequest{Code: "AAAAAAAA"})
	if !slices.Equal(pageIDs(byCode), []string{"run_aaaaaaaa02", "run_aaaaaaaa01"}) {
		t.Errorf("code=AAAAAAAA items = %v", pageIDs(byCode))
	}
	exact := listRuns(ctx, t, svc, service.ListRunsRequest{Code: "aaaaaaab03"})
	if !slices.Equal(pageIDs(exact), []string{"run_aaaaaaab03"}) || exact.Counts.All != 1 {
		t.Errorf("code=aaaaaaab03 items = %v counts = %+v", pageIDs(exact), exact.Counts)
	}
	none := listRuns(ctx, t, svc, service.ListRunsRequest{Code: "dddddddd"})
	if len(none.Items) != 0 || none.Counts.All != 0 {
		t.Errorf("unmatched code: items = %v counts = %+v", pageIDs(none), none.Counts)
	}

	unknown := listRuns(ctx, t, svc, service.ListRunsRequest{WorkflowID: "wf_missing"})
	if len(unknown.Items) != 0 || unknown.Counts != (service.RunStatusCounts{}) || unknown.NextCursor != nil || unknown.PrevCursor != nil {
		t.Errorf("unknown workflow page = %+v, want empty with zero counts and no cursors", unknown)
	}

	// A cursor issued under one filter set is refused under another.
	paged := listRuns(ctx, t, svc, service.ListRunsRequest{WorkflowID: "wf_list", Limit: 1})
	_, err := svc.ListRuns(ctx, service.ListRunsRequest{WorkflowID: "wf_other", Limit: 1, Cursor: *paged.NextCursor})
	var qErr *service.RunListQueryError
	if !errors.As(err, &qErr) {
		t.Errorf("cursor reused under other filters: err = %v, want *service.RunListQueryError", err)
	}
}

// TestRunList_FailedAndWaitingSteps_ReadFromCommittedFacts proves the failed step is the
// NodeRun of the highest-seq NODE_FAILED Event (the one whose failure last wrote the Run
// error summary), that names come from the Run's own Definition version, and that a PAUSED
// Run lists its WAITING_CALLBACK steps.
func TestRunList_FailedAndWaitingSteps_ReadFromCommittedFacts(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	saveDefinition(ctx, t, uow, listDefinition("wf_list", 1, "Render image"))
	saveDefinition(ctx, t, uow, listDefinition("wf_list", 2, "Render poster"))
	svc := service.NewQueryService(service.Deps{UoW: uow})

	waitDraft := fixtureTime.Add(10 * time.Second)
	waitRender := fixtureTime.Add(5 * time.Second)
	seedListRun(ctx, t, uow, listRunSpec{
		id: "run_failed", workflow: "wf_list", version: 1, status: domain.RunFailed,
		startedAt: fixtureTime.Add(2 * time.Minute), errorCode: "MODEL_ERROR",
		nodes: []listNode{
			{nodeRunID: "nr_f_draft", nodeID: "node_draft", status: domain.NodeRunFailed},
			{nodeRunID: "nr_f_render", nodeID: "node_render", status: domain.NodeRunFailed},
		},
		failedNodeRuns: []string{"nr_f_draft", "nr_f_render"},
	})
	seedListRun(ctx, t, uow, listRunSpec{
		id: "run_paused", workflow: "wf_list", version: 2, status: domain.RunPaused,
		startedAt: fixtureTime.Add(time.Minute),
		nodes: []listNode{
			{nodeRunID: "nr_p_draft", nodeID: "node_draft", status: domain.NodeRunWaitingCallback, waitingAt: &waitDraft},
			{nodeRunID: "nr_p_render", nodeID: "node_render", status: domain.NodeRunWaitingCallback, waitingAt: &waitRender},
		},
	})
	seedListRun(ctx, t, uow, listRunSpec{
		id: "run_running", workflow: "wf_list", version: 2, status: domain.RunRunning, startedAt: fixtureTime,
		nodes: []listNode{{nodeRunID: "nr_r_draft", nodeID: "node_draft", status: domain.NodeRunWaitingCallback, waitingAt: &waitDraft}},
	})

	page := listRuns(ctx, t, svc, service.ListRunsRequest{})
	if !slices.Equal(pageIDs(page), []string{"run_failed", "run_paused", "run_running"}) {
		t.Fatalf("items = %v", pageIDs(page))
	}
	failed, paused, running := page.Items[0], page.Items[1], page.Items[2]

	if failed.ErrorCode != "MODEL_ERROR" {
		t.Errorf("failed Run error code = %q, want MODEL_ERROR", failed.ErrorCode)
	}
	if failed.FailedStep == nil || failed.FailedStep.NodeID != "node_render" || failed.FailedStep.NodeName != "Render image" {
		t.Errorf("failed step = %+v, want node_render named from version 1 (Render image)", failed.FailedStep)
	}
	if failed.DefinitionVersion != 1 || failed.LatestVersion != 2 || failed.WorkflowName != "List wf_list" {
		t.Errorf("failed Run version facts = v%d of latest %d, name %q", failed.DefinitionVersion, failed.LatestVersion, failed.WorkflowName)
	}
	if len(failed.WaitingSteps) != 0 {
		t.Errorf("failed Run waiting steps = %+v, want none", failed.WaitingSteps)
	}

	if paused.FailedStep != nil || paused.ErrorCode != "" {
		t.Errorf("paused Run carries failure fields: %+v", paused)
	}
	if len(paused.WaitingSteps) != 2 ||
		paused.WaitingSteps[0].NodeID != "node_render" || paused.WaitingSteps[0].NodeName != "Render poster" ||
		paused.WaitingSteps[0].WaitingAt == nil || !paused.WaitingSteps[0].WaitingAt.Equal(waitRender) ||
		paused.WaitingSteps[1].NodeID != "node_draft" {
		t.Errorf("paused Run waiting steps = %+v, want node_render then node_draft by waiting time", paused.WaitingSteps)
	}

	if len(running.WaitingSteps) != 0 || running.FailedStep != nil {
		t.Errorf("running Run carries step fields: %+v", running)
	}
}

// TestDefinitionsList_FailedLastRun_CarriesErrorCodeAndFailedStep covers the Definitions
// list's last-Run failure fields; a Workflow that never ran keeps a nil last Run.
func TestDefinitionsList_FailedLastRun_CarriesErrorCodeAndFailedStep(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	saveDefinition(ctx, t, uow, listDefinition("wf_failed", 1, "Render image"))
	saveDefinition(ctx, t, uow, listDefinition("wf_never", 1, "Render image"))
	seedListRun(ctx, t, uow, listRunSpec{
		id: "run_last", workflow: "wf_failed", status: domain.RunFailed, startedAt: fixtureTime, errorCode: "TIMEOUT",
		nodes:          []listNode{{nodeRunID: "nr_last", nodeID: "node_draft", status: domain.NodeRunFailed}},
		failedNodeRuns: []string{"nr_last"},
	})

	items, err := service.NewDefinitionService(service.Deps{UoW: uow}).List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byID := map[string]service.DefinitionListItem{}
	for _, item := range items {
		byID[item.Workflow.WorkflowID] = item
	}
	failed := byID["wf_failed"]
	if failed.LastRun == nil || failed.LastRun.ID != "run_last" {
		t.Fatalf("wf_failed last run = %+v", failed.LastRun)
	}
	if step := failed.LastRunFailedStep; step == nil || step.NodeID != "node_draft" || step.NodeName != "Draft caption" {
		t.Errorf("wf_failed last run failed step = %+v, want node_draft (Draft caption)", step)
	}
	if never := byID["wf_never"]; never.LastRun != nil || never.LastRunFailedStep != nil {
		t.Errorf("wf_never = %+v, want no last run", never)
	}
}
