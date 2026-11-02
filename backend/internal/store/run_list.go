package store

import (
	"context"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// RunListRepository reads the Run list projection: Run summaries in the total order
// (started_at DESC, id DESC), status counts, and the failed or waiting step of a Run. It
// only reads committed facts; it has no write method.
type RunListRepository interface {
	// ListSummaries returns at most limit Runs matching filter. With after nil it starts
	// from the newest Run. Otherwise it returns the Runs strictly beyond after in
	// direction: RunsOlder pages descend from after, RunsNewer pages ascend from it, so
	// the caller always receives the Runs nearest to the boundary first.
	ListSummaries(ctx context.Context, filter RunListFilter, after *RunPosition, direction RunListDirection, limit int) ([]RunSummary, error)

	// ExistsBeyond reports whether any Run matching filter lies strictly beyond pos in
	// direction.
	ExistsBeyond(ctx context.Context, filter RunListFilter, pos RunPosition, direction RunListDirection) (bool, error)

	// CountByStatus counts the Runs matching filter per Run status. A status with no Run
	// is absent from the map.
	CountByStatus(ctx context.Context, filter RunListFilter) (map[domain.RunStatus]int, error)

	// FailedSteps returns, per Run ID, the NodeRun named by the Run's highest-seq
	// NODE_FAILED Event: every node failure overwrites the Run error summary in the same
	// transaction that appends that Event, so this is the step the summary describes.
	// Runs without such an Event are absent from the map.
	FailedSteps(ctx context.Context, runIDs []string) (map[string]RunStep, error)

	// WaitingSteps returns, per Run ID, the NodeRuns currently in WAITING_CALLBACK,
	// ordered by waiting_at then node ID.
	WaitingSteps(ctx context.Context, runIDs []string) (map[string][]RunStep, error)

	// FinishedCalls returns, per Run ID, one entry for every finished call the Run made:
	// each Node Attempt and Tool Attempt in SUCCEEDED or FAILED and each Agent Turn in
	// COMPLETED or FAILED, with the Cost recorded beside its result (nil when none was).
	// Which of them count as external calls is decided by the caller from the
	// registered extension metadata; this read only reports the persisted facts. Runs
	// without a finished call are absent from the map.
	FinishedCalls(ctx context.Context, runIDs []string) (map[string][]FinishedCall, error)
}

// FinishedCallKind names the table a FinishedCall came from.
type FinishedCallKind string

const (
	FinishedNodeAttempt FinishedCallKind = "NODE_ATTEMPT"
	FinishedToolAttempt FinishedCallKind = "TOOL_ATTEMPT"
	FinishedAgentTurn   FinishedCallKind = "AGENT_TURN"
)

// FinishedCall is one finished Node Attempt, Tool Attempt or Agent Turn of a Run, reduced
// to what the cost read needs. ToolName is set for a Tool Attempt only. Dispatched
// reports that a Node Attempt handed a task to a Provider; Uncertain that an Attempt
// failed without knowing whether the Provider acted. Cost is the charge recorded with the
// result, nil when none was recorded.
type FinishedCall struct {
	Kind       FinishedCallKind
	ToolName   string
	Dispatched bool
	Uncertain  bool
	Cost       *domain.Cost
}

// RunListFilter narrows the Run list. A zero-valued field does not filter.
type RunListFilter struct {
	WorkflowID string
	Status     domain.RunStatus
	// StartedFrom keeps Runs whose started_at is at or after it.
	StartedFrom *time.Time
	// IDPrefix keeps Runs whose ID starts with it, compared byte-wise.
	IDPrefix string
}

// RunListDirection is the side of a page boundary a read continues on.
type RunListDirection int

const (
	// RunsOlder continues towards earlier (started_at, id) positions.
	RunsOlder RunListDirection = iota
	// RunsNewer continues towards later (started_at, id) positions.
	RunsNewer
)

// RunPosition is one point in the Run list order.
type RunPosition struct {
	StartedAt time.Time
	ID        string
}

// RunSummary is one Run list row: persisted Run facts plus the current name and latest
// version of its Workflow. ErrorCode carries only the code of the Run error summary, and
// is empty when the Run has none.
type RunSummary struct {
	ID                string
	WorkflowID        string
	WorkflowName      string
	DefinitionVersion int
	LatestVersion     int
	Status            domain.RunStatus
	StartedAt         time.Time
	CompletedAt       *time.Time
	ErrorCode         string
}

// Position reports the summary's place in the list order.
func (s RunSummary) Position() RunPosition {
	return RunPosition{StartedAt: s.StartedAt, ID: s.ID}
}

// RunStep names one node of the Definition version a Run is bound to. WaitingAt is set
// only for a waiting step.
type RunStep struct {
	NodeID    string
	NodeName  string
	WaitingAt *time.Time
}
