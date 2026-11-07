package api

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

// operatorTokenHeader carries the operator credential every approval route requires, the
// read-only ones included, because an approval's detail shows the Action's arguments. The
// header is compared once, in constant time, and never logged or echoed back.
const operatorTokenHeader = "X-Emberling-Operator-Token"

// OperatorConfig is the approval routes' authentication: the shared credential and the
// fixed identity name recorded as the deciding person.
type OperatorConfig struct {
	Token string
	Name  string
}

// approvalDTO is the wire shape of one approval. decidedBy is the operator's identity name,
// never a credential.
type approvalDTO struct {
	ID          string                `json:"id"`
	ActionID    string                `json:"actionId"`
	RunID       string                `json:"runId"`
	AgentRunID  string                `json:"agentRunId"`
	Status      domain.ApprovalStatus `json:"status"`
	RequestedAt time.Time             `json:"requestedAt"`
	Deadline    time.Time             `json:"deadline"`
	DecidedAt   *time.Time            `json:"decidedAt"`
	DecidedBy   *string               `json:"decidedBy"`
	Reason      *string               `json:"reason"`
}

// approvalViewDTO is an approval with what a person needs to decide it.
type approvalViewDTO struct {
	approvalDTO
	ToolName   string          `json:"toolName"`
	Arguments  json.RawMessage `json:"arguments"`
	WorkflowID string          `json:"workflowId"`
	NodeRunID  string          `json:"nodeRunId"`
}

// approvalDetailDTO is the approval view with the material a person decides on: the change
// request the call names, with its Markdown and resolved decision basis, and the full text
// of the workspace patch that request is about. Both are null for a call that names no
// change request of this Agent Run. The list keeps the view shape; only the detail carries
// content.
type approvalDetailDTO struct {
	approvalViewDTO
	ChangeRequest *approvalChangeRequestDTO `json:"changeRequest"`
	Patch         *approvalPatchDTO         `json:"patch"`
}

// approvalChangeRequestDTO is the Trace projection of a change request plus what the Trace
// never carries: the rendered Markdown and the decision basis with its Manifest links.
type approvalChangeRequestDTO struct {
	changeRequestDTO
	Content       string             `json:"content"`
	DecisionBasis []decisionBasisDTO `json:"decisionBasis"`
}

// decisionBasisDTO is one cited item: the statement, the Turn it names, and the Manifest
// item the reference resolves to. turnId is empty and item null when the Turn or item is
// no longer resolvable.
type decisionBasisDTO struct {
	Statement   string           `json:"statement"`
	TurnNo      int              `json:"turnNo"`
	TurnID      string           `json:"turnId"`
	ManifestRef string           `json:"manifestRef"`
	Item        *manifestItemDTO `json:"item"`
}

// approvalPatchDTO is a workspace patch summary with the cumulative patch text, which the
// Trace never carries.
type approvalPatchDTO struct {
	workspacePatchDTO
	Patch string `json:"patch"`
}

// approvalDecisionRequest is the body of POST /api/approvals/{approvalId}/decision.
type approvalDecisionRequest struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// approvalDecisionResponse carries the approval as it stands after the call; repeated is
// true when the same decision was already recorded and nothing changed.
type approvalDecisionResponse struct {
	Approval approvalDTO `json:"approval"`
	Repeated bool        `json:"repeated"`
}

func toApprovalDTO(a domain.Approval) approvalDTO {
	return approvalDTO{
		ID: a.ID, ActionID: a.ActionID, RunID: a.RunID, AgentRunID: a.AgentRunID, Status: a.Status,
		RequestedAt: utcTime(a.RequestedAt), Deadline: utcTime(a.Deadline),
		DecidedAt: utcTimePtr(a.DecidedAt), DecidedBy: a.DecidedBy, Reason: a.Reason,
	}
}

func toApprovalDTOPtr(a *domain.Approval) *approvalDTO {
	if a == nil {
		return nil
	}
	dto := toApprovalDTO(*a)
	return &dto
}

func toApprovalViewDTO(view service.ApprovalView) approvalViewDTO {
	arguments := view.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage(`null`)
	}
	return approvalViewDTO{
		approvalDTO: toApprovalDTO(view.Approval),
		ToolName:    view.ToolName,
		Arguments:   arguments,
		WorkflowID:  view.WorkflowID,
		NodeRunID:   view.NodeRunID,
	}
}

func toApprovalDetailDTO(detail service.ApprovalDetail) approvalDetailDTO {
	dto := approvalDetailDTO{approvalViewDTO: toApprovalViewDTO(detail.ApprovalView)}
	if request := detail.ChangeRequest; request != nil {
		basis := make([]decisionBasisDTO, 0, len(request.Basis))
		for _, item := range request.Basis {
			entry := decisionBasisDTO{Statement: item.Statement, TurnNo: item.TurnNo, TurnID: item.TurnID, ManifestRef: item.ManifestRef}
			if item.Item != nil {
				resolved := toManifestItemDTO(*item.Item)
				entry.Item = &resolved
			}
			basis = append(basis, entry)
		}
		dto.ChangeRequest = &approvalChangeRequestDTO{
			changeRequestDTO: toChangeRequestDTO(request.Request),
			Content:          request.Content,
			DecisionBasis:    basis,
		}
	}
	if patch := detail.Patch; patch != nil {
		dto.Patch = &approvalPatchDTO{workspacePatchDTO: toWorkspacePatchDTO(*patch), Patch: patch.Patch}
	}
	return dto
}

// requireOperator answers 401 before any handler runs when the operator credential is
// missing or does not match. The comparison is constant time; the presented value is
// never logged, included in the response or passed on.
func (d Deps) requireOperator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented := r.Header.Get(operatorTokenHeader)
		if presented == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(d.Operator.Token)) != 1 {
			writeErrorEnvelope(w, http.StatusUnauthorized, codeInvalidOperatorCredential,
				"operator credential is missing or invalid", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// listApprovals serves GET /api/approvals?status=PENDING; the status defaults to PENDING.
func (d Deps) listApprovals(w http.ResponseWriter, r *http.Request) {
	status := domain.ApprovalStatus(r.URL.Query().Get("status"))
	if status == "" {
		status = domain.ApprovalPending
	}
	views, err := d.Query.ListApprovals(r.Context(), status)
	if err != nil {
		writeError(w, d.Logger, err, "")
		return
	}
	items := make([]approvalViewDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toApprovalViewDTO(view))
	}
	writeJSON(w, http.StatusOK, listEnvelope[approvalViewDTO]{Items: items})
}

// getApproval serves GET /api/approvals/{approvalId}: the view plus the change request and
// patch the call names.
func (d Deps) getApproval(w http.ResponseWriter, r *http.Request) {
	detail, err := d.Query.GetApprovalDetail(r.Context(), chi.URLParam(r, "approvalId"))
	if err != nil {
		writeError(w, d.Logger, err, codeApprovalNotFound)
		return
	}
	writeJSON(w, http.StatusOK, toApprovalDetailDTO(detail))
}

// decideApproval serves POST /api/approvals/{approvalId}/decision. The authenticated
// identity is the configured operator name; the decision itself is the service's.
func (d Deps) decideApproval(w http.ResponseWriter, r *http.Request) {
	var req approvalDecisionRequest
	if !decodeJSON(w, d.Logger, r, &req) {
		return
	}
	var decision domain.ApprovalStatus
	switch req.Decision {
	case "APPROVE":
		decision = domain.ApprovalApproved
	case "REJECT":
		decision = domain.ApprovalRejected
	default:
		writeError(w, d.Logger, &badRequestError{message: "decision must be APPROVE or REJECT"}, "")
		return
	}
	outcome, err := d.Execution.DecideApproval(r.Context(), service.DecideApproval{
		ApprovalID: chi.URLParam(r, "approvalId"),
		Decision:   decision,
		Reason:     req.Reason,
		DecidedBy:  d.Operator.Name,
	})
	if err != nil {
		writeError(w, d.Logger, err, codeApprovalNotFound)
		return
	}
	writeJSON(w, http.StatusOK, approvalDecisionResponse{Approval: toApprovalDTO(outcome.Approval), Repeated: outcome.Repeated})
}
