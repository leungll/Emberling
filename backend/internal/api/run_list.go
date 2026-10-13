package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

// runListResponse mirrors Studio's RunListResponse. Both cursors are always present and
// null when nothing lies on that side.
type runListResponse struct {
	Items      []runListItemDTO   `json:"items"`
	NextCursor *string            `json:"nextCursor"`
	PrevCursor *string            `json:"prevCursor"`
	Counts     runStatusCountsDTO `json:"counts"`
}

// runStatusCountsDTO mirrors Studio's RunStatusCounts: one count per Run status, plus
// their sum, computed by the server.
type runStatusCountsDTO struct {
	All       int `json:"all"`
	Running   int `json:"running"`
	Paused    int `json:"paused"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

// runErrorCodeDTO is the only part of a Run error summary a list projects: the stable
// code, never the message or details.
type runErrorCodeDTO struct {
	Code string `json:"code"`
}

// runStepDTO mirrors Studio's RunStepRef.
type runStepDTO struct {
	NodeID   string `json:"nodeId"`
	NodeName string `json:"nodeName"`
}

// runWaitingStepDTO mirrors Studio's RunWaitingStep.
type runWaitingStepDTO struct {
	NodeID    string     `json:"nodeId"`
	NodeName  string     `json:"nodeName"`
	WaitingAt *time.Time `json:"waitingAt"`
}

// runListItemDTO mirrors Studio's RunListItem. createdAt repeats startedAt: a Run has no
// separate creation time, and it starts in the transaction that creates it. The
// nullable keys are always present.
type runListItemDTO struct {
	ID                string              `json:"id"`
	WorkflowID        string              `json:"workflowId"`
	WorkflowName      string              `json:"workflowName"`
	DefinitionVersion int                 `json:"definitionVersion"`
	LatestVersion     int                 `json:"latestVersion"`
	Status            domain.RunStatus    `json:"status"`
	CreatedAt         time.Time           `json:"createdAt"`
	StartedAt         time.Time           `json:"startedAt"`
	CompletedAt       *time.Time          `json:"completedAt"`
	Error             *runErrorCodeDTO    `json:"error"`
	FailedStep        *runStepDTO         `json:"failedStep"`
	WaitingSteps      []runWaitingStepDTO `json:"waitingSteps"`
}

func toRunErrorCodeDTO(status domain.RunStatus, code string) *runErrorCodeDTO {
	if status != domain.RunFailed || code == "" {
		return nil
	}
	return &runErrorCodeDTO{Code: code}
}

func toRunStepDTO(step *service.RunStep) *runStepDTO {
	if step == nil {
		return nil
	}
	return &runStepDTO{NodeID: step.NodeID, NodeName: step.NodeName}
}

func toRunListItemDTO(item service.RunListItem) runListItemDTO {
	waiting := make([]runWaitingStepDTO, len(item.WaitingSteps))
	for i, step := range item.WaitingSteps {
		waiting[i] = runWaitingStepDTO{NodeID: step.NodeID, NodeName: step.NodeName, WaitingAt: step.WaitingAt}
	}
	return runListItemDTO{
		ID:                item.ID,
		WorkflowID:        item.WorkflowID,
		WorkflowName:      item.WorkflowName,
		DefinitionVersion: item.DefinitionVersion,
		LatestVersion:     item.LatestVersion,
		Status:            item.Status,
		CreatedAt:         item.StartedAt,
		StartedAt:         item.StartedAt,
		CompletedAt:       item.CompletedAt,
		Error:             toRunErrorCodeDTO(item.Status, item.ErrorCode),
		FailedStep:        toRunStepDTO(item.FailedStep),
		WaitingSteps:      waiting,
	}
}

// listRuns serves GET /runs, a read-only query: it parses the query string, and the
// service validates the values and reads the page.
func (d Deps) listRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := service.ListRunsRequest{
		WorkflowID: q.Get("workflowId"),
		Status:     q.Get("status"),
		Code:       q.Get("code"),
		Cursor:     q.Get("cursor"),
	}
	if raw := q.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 {
			writeError(w, d.Logger, &badRequestError{message: "limit must be a positive integer"}, "")
			return
		}
		req.Limit = limit
	}
	if raw := q.Get("startedAfter"); raw != "" {
		startedAfter, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(w, d.Logger, &badRequestError{message: "startedAfter must be an RFC 3339 timestamp"}, "")
			return
		}
		req.StartedAfter = &startedAfter
	}

	page, err := d.Query.ListRuns(r.Context(), req)
	if err != nil {
		writeError(w, d.Logger, err, "")
		return
	}
	items := make([]runListItemDTO, len(page.Items))
	for i, item := range page.Items {
		items[i] = toRunListItemDTO(item)
	}
	writeJSON(w, http.StatusOK, runListResponse{
		Items:      items,
		NextCursor: page.NextCursor,
		PrevCursor: page.PrevCursor,
		Counts: runStatusCountsDTO{
			All:       page.Counts.All,
			Running:   page.Counts.Running,
			Paused:    page.Counts.Paused,
			Completed: page.Counts.Completed,
			Failed:    page.Counts.Failed,
		},
	})
}
