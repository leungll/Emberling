package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// Run list page sizes: the server owns the default and the upper bound.
const (
	defaultRunListLimit = 20
	maxRunListLimit     = 100
)

// Run short codes are the leading characters of the Run ID after its "run_" prefix.
const (
	minRunCodeLength = 8
	maxRunCodeLength = 64
)

// ListRunsRequest is one Run list query. Zero-valued filters do not filter; Limit 0 means
// the default page size.
type ListRunsRequest struct {
	WorkflowID   string
	Status       string
	StartedAfter *time.Time
	Code         string
	Cursor       string
	Limit        int
}

// RunListPage is one page of the Run list, newest first. NextCursor continues to older
// Runs and PrevCursor to newer ones; each is nil when nothing lies on that side. Counts
// cover every Run matching the request's workflow, time and code filters, regardless of
// its status filter and of paging.
type RunListPage struct {
	Items      []RunListItem
	NextCursor *string
	PrevCursor *string
	Counts     RunStatusCounts
}

// RunStatusCounts is the number of Runs in each Run status; All is their sum.
type RunStatusCounts struct {
	All       int
	Running   int
	Paused    int
	Completed int
	Failed    int
}

// RunListItem is one Run list row. ErrorCode and FailedStep are set only for a FAILED
// Run, WaitingSteps only for a PAUSED one. No input, output or error message is carried.
type RunListItem struct {
	ID                string
	WorkflowID        string
	WorkflowName      string
	DefinitionVersion int
	LatestVersion     int
	Status            domain.RunStatus
	StartedAt         time.Time
	CompletedAt       *time.Time
	ErrorCode         string
	FailedStep        *RunStep
	WaitingSteps      []RunStep
}

// RunStep names one node of the Definition version a Run is bound to. WaitingAt is set
// only for a step waiting on an external result.
type RunStep struct {
	NodeID    string
	NodeName  string
	WaitingAt *time.Time
}

// RunListQueryError reports a Run list query the service cannot answer as asked: an
// unknown status, an out-of-range page size, a malformed short code, or a cursor that was
// not issued for the same filters. It is a request-content error, never a server fault.
type RunListQueryError struct {
	Message string
}

func (e *RunListQueryError) Error() string { return e.Message }

// ListRuns reads one page of the Run list together with its status counts. The page, the
// counts and every item's failed or waiting step come from one consistent read-only
// snapshot, so the counts never disagree with the rows they accompany. It changes no
// execution state and writes no Event.
func (s *QueryService) ListRuns(ctx context.Context, req ListRunsRequest) (RunListPage, error) {
	filter, limit, err := runListFilterOf(req)
	if err != nil {
		return RunListPage{}, err
	}
	digest := runListFilterDigest(filter)

	var (
		after     *store.RunPosition
		direction = store.RunsOlder
	)
	if req.Cursor != "" {
		pos, dir, err := decodeRunListCursor(req.Cursor, digest)
		if err != nil {
			return RunListPage{}, err
		}
		after, direction = &pos, dir
	}

	var page RunListPage
	err = s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		repo := tx.RunList()
		// One row beyond the page tells whether the read direction continues; the other
		// direction is answered by an existence check from the page's opposite edge.
		summaries, err := repo.ListSummaries(ctx, filter, after, direction, limit+1)
		if err != nil {
			return err
		}
		more := len(summaries) > limit
		if more {
			summaries = summaries[:limit]
		}
		if direction == store.RunsNewer {
			slices.Reverse(summaries)
		}

		hasOlder, hasNewer := more, false
		if direction == store.RunsNewer {
			hasOlder, hasNewer = false, more
		}
		if len(summaries) > 0 {
			if direction == store.RunsNewer {
				if hasOlder, err = repo.ExistsBeyond(ctx, filter, summaries[len(summaries)-1].Position(), store.RunsOlder); err != nil {
					return err
				}
			} else if after != nil {
				if hasNewer, err = repo.ExistsBeyond(ctx, filter, summaries[0].Position(), store.RunsNewer); err != nil {
					return err
				}
			}
			if hasOlder {
				c := encodeRunListCursor(summaries[len(summaries)-1].Position(), store.RunsOlder, digest)
				page.NextCursor = &c
			}
			if hasNewer {
				c := encodeRunListCursor(summaries[0].Position(), store.RunsNewer, digest)
				page.PrevCursor = &c
			}
		}

		countFilter := filter
		countFilter.Status = ""
		counts, err := repo.CountByStatus(ctx, countFilter)
		if err != nil {
			return err
		}
		page.Counts = runStatusCountsOf(counts)

		page.Items, err = runListItemsOf(ctx, repo, summaries)
		return err
	})
	if err != nil {
		return RunListPage{}, fmt.Errorf("service query.ListRuns: %w", err)
	}
	return page, nil
}

// runListFilterOf validates req and turns it into the store filter and page size.
func runListFilterOf(req ListRunsRequest) (store.RunListFilter, int, error) {
	limit := req.Limit
	switch {
	case limit == 0:
		limit = defaultRunListLimit
	case limit < 1 || limit > maxRunListLimit:
		return store.RunListFilter{}, 0, &RunListQueryError{
			Message: fmt.Sprintf("limit must be between 1 and %d", maxRunListLimit),
		}
	}

	filter := store.RunListFilter{WorkflowID: req.WorkflowID}
	if req.Status != "" {
		status := domain.RunStatus(req.Status)
		if !status.IsValid() {
			return store.RunListFilter{}, 0, &RunListQueryError{
				Message: "status must be one of RUNNING, PAUSED, COMPLETED, FAILED",
			}
		}
		filter.Status = status
	}
	if req.StartedAfter != nil {
		from := req.StartedAfter.UTC().Truncate(time.Microsecond)
		filter.StartedFrom = &from
	}
	if req.Code != "" {
		code, err := normalizeRunCode(req.Code)
		if err != nil {
			return store.RunListFilter{}, 0, err
		}
		filter.IDPrefix = domain.IDPrefixRun + "_" + code
	}
	return filter, limit, nil
}

// normalizeRunCode accepts 8 to 64 ASCII letters or digits. Run IDs are lowercase, so
// the code is lowercased and a short code typed in capitals still finds its Run.
func normalizeRunCode(code string) (string, error) {
	invalid := &RunListQueryError{
		Message: fmt.Sprintf("code must be %d to %d letters or digits", minRunCodeLength, maxRunCodeLength),
	}
	if len(code) < minRunCodeLength || len(code) > maxRunCodeLength {
		return "", invalid
	}
	for _, c := range code {
		isDigit := c >= '0' && c <= '9'
		isLetter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !isDigit && !isLetter {
			return "", invalid
		}
	}
	return strings.ToLower(code), nil
}

func runStatusCountsOf(counts map[domain.RunStatus]int) RunStatusCounts {
	c := RunStatusCounts{
		Running:   counts[domain.RunRunning],
		Paused:    counts[domain.RunPaused],
		Completed: counts[domain.RunCompleted],
		Failed:    counts[domain.RunFailed],
	}
	c.All = c.Running + c.Paused + c.Completed + c.Failed
	return c
}

// runListItemsOf completes each summary with its failed step (FAILED Runs) or waiting
// steps (PAUSED Runs), reading both in one batch per kind.
func runListItemsOf(ctx context.Context, repo store.RunListRepository, summaries []store.RunSummary) ([]RunListItem, error) {
	var failedIDs, pausedIDs []string
	for _, s := range summaries {
		if s.Status == domain.RunFailed {
			failedIDs = append(failedIDs, s.ID)
		}
		if s.Status == domain.RunPaused {
			pausedIDs = append(pausedIDs, s.ID)
		}
	}
	failed, err := failedStepsOf(ctx, repo, failedIDs)
	if err != nil {
		return nil, err
	}
	waiting := map[string][]store.RunStep{}
	if len(pausedIDs) > 0 {
		if waiting, err = repo.WaitingSteps(ctx, pausedIDs); err != nil {
			return nil, err
		}
	}

	items := make([]RunListItem, len(summaries))
	for i, s := range summaries {
		item := RunListItem{
			ID:                s.ID,
			WorkflowID:        s.WorkflowID,
			WorkflowName:      s.WorkflowName,
			DefinitionVersion: s.DefinitionVersion,
			LatestVersion:     s.LatestVersion,
			Status:            s.Status,
			StartedAt:         s.StartedAt,
			CompletedAt:       s.CompletedAt,
		}
		if s.Status == domain.RunFailed {
			item.ErrorCode = s.ErrorCode
			item.FailedStep = failed[s.ID]
		}
		if s.Status == domain.RunPaused {
			for _, step := range waiting[s.ID] {
				item.WaitingSteps = append(item.WaitingSteps, RunStep(step))
			}
		}
		items[i] = item
	}
	return items, nil
}

// failedStepsOf reads the failed step of each given FAILED Run. It issues no query when
// there is none, so a list without failures never touches the Event log.
func failedStepsOf(ctx context.Context, repo store.RunListRepository, runIDs []string) (map[string]*RunStep, error) {
	out := make(map[string]*RunStep, len(runIDs))
	if len(runIDs) == 0 {
		return out, nil
	}
	steps, err := repo.FailedSteps(ctx, runIDs)
	if err != nil {
		return nil, err
	}
	for id, step := range steps {
		s := RunStep{NodeID: step.NodeID, NodeName: step.NodeName}
		out[id] = &s
	}
	return out, nil
}
