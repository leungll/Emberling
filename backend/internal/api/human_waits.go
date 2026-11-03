package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

// humanWaitViewDTO is one OPEN human wait with what a person needs to decide whether to
// stop waiting: where the release sits, the target it holds, the operation, the confirm
// deadline that passed and why production has given no answer. Operation keys, ids,
// statuses and times only: no request bodies, credentials or addresses.
type humanWaitViewDTO struct {
	humanWaitDTO
	ActionID        string                  `json:"actionId"`
	RunID           string                  `json:"runId"`
	AgentRunID      string                  `json:"agentRunId"`
	NodeRunID       string                  `json:"nodeRunId"`
	WorkflowID      string                  `json:"workflowId"`
	ToolName        string                  `json:"toolName"`
	TargetKey       string                  `json:"targetKey"`
	OperationKey    string                  `json:"operationKey"`
	ConfirmDeadline *time.Time              `json:"confirmDeadline"`
	Reason          service.HumanWaitReason `json:"reason"`
}

// stopHumanWaitResponse carries the human wait as it stands after the call; repeated is
// true when the wait had already been stopped and nothing changed.
type stopHumanWaitResponse struct {
	HumanWait humanWaitDTO `json:"humanWait"`
	Repeated  bool         `json:"repeated"`
}

func toHumanWaitDTO(wait domain.HumanWait) humanWaitDTO {
	return humanWaitDTO{
		ID:       wait.ID,
		Status:   wait.Status,
		OpenedAt: utcTime(wait.OpenedAt),
		Deadline: utcTime(wait.Deadline),
		ClosedAt: utcTimePtr(wait.ClosedAt),
		ClosedBy: wait.ClosedBy,
		Outcome:  wait.Outcome,
	}
}

// listHumanWaits serves GET /api/human-waits: the OPEN human waits, earliest deadline
// first.
func (d Deps) listHumanWaits(w http.ResponseWriter, r *http.Request) {
	views, err := d.Query.ListOpenHumanWaits(r.Context())
	if err != nil {
		writeError(w, d.Logger, err, "")
		return
	}
	items := make([]humanWaitViewDTO, 0, len(views))
	for _, view := range views {
		items = append(items, humanWaitViewDTO{
			humanWaitDTO:    toHumanWaitDTO(view.Wait),
			ActionID:        view.Wait.ActionID,
			RunID:           view.Wait.RunID,
			AgentRunID:      view.AgentRunID,
			NodeRunID:       view.NodeRunID,
			WorkflowID:      view.WorkflowID,
			ToolName:        view.ToolName,
			TargetKey:       view.TargetKey,
			OperationKey:    view.OperationKey,
			ConfirmDeadline: utcTimePtr(view.ConfirmDeadline),
			Reason:          view.Reason,
		})
	}
	writeJSON(w, http.StatusOK, listEnvelope[humanWaitViewDTO]{Items: items})
}

// stopHumanWait serves POST /api/human-waits/{humanWaitId}/stop. The authenticated identity
// is the configured operator name; whether the stop takes effect is the service's decision.
func (d Deps) stopHumanWait(w http.ResponseWriter, r *http.Request) {
	outcome, err := d.Execution.StopHumanWait(r.Context(), chi.URLParam(r, "humanWaitId"), d.Operator.Name)
	if err != nil {
		writeError(w, d.Logger, err, codeHumanWaitNotFound)
		return
	}
	writeJSON(w, http.StatusOK, stopHumanWaitResponse{HumanWait: toHumanWaitDTO(outcome.Wait), Repeated: outcome.Repeated})
}
