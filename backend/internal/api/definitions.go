package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

// createDefinitionRequest is the wire shape of POST /definitions and (embedded in
// saveDefinitionRequest) PUT /definitions/{workflowId}, matching Studio's
// CreateDefinitionRequest (studio/src/api/types.ts).
type createDefinitionRequest struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Nodes       []domain.Node `json:"nodes"`
	Edges       []domain.Edge `json:"edges"`
}

// saveDefinitionRequest additionally carries baseVersion, matching Studio's
// SaveDefinitionRequest.
type saveDefinitionRequest struct {
	createDefinitionRequest
	BaseVersion int `json:"baseVersion"`
}

// lastRunDTO is DefinitionListItem.lastRun's shape (docs/08-interface-spec.md §3.1): only
// id/status/createdAt, never the full Run. createdAt is sourced from domain.Run.StartedAt:
// domain.Run has no CreatedAt field of its own, and StartedAt is set the moment CreateRun
// commits, so it is the only fact this field can mean.
type lastRunDTO struct {
	ID        string           `json:"id"`
	Status    domain.RunStatus `json:"status"`
	CreatedAt time.Time        `json:"createdAt"`
}

// definitionListItemDTO is one row of GET /definitions. LastRun is a pointer with no
// omitempty so a Definition that has never run still serialises "lastRun": null, matching
// Studio's DefinitionListItem.lastRun: DefinitionLastRun | null.
type definitionListItemDTO struct {
	WorkflowID    string      `json:"workflowId"`
	Name          string      `json:"name"`
	Description   string      `json:"description"`
	LatestVersion int         `json:"latestVersion"`
	UpdatedAt     time.Time   `json:"updatedAt"`
	LastRun       *lastRunDTO `json:"lastRun"`
}

func toDefinitionListItemDTO(item service.DefinitionListItem) definitionListItemDTO {
	dto := definitionListItemDTO{
		WorkflowID:    item.Workflow.WorkflowID,
		Name:          item.Workflow.Name,
		Description:   item.Description,
		LatestVersion: item.Workflow.LatestVersion,
		UpdatedAt:     item.Workflow.UpdatedAt,
	}
	if item.LastRun != nil {
		dto.LastRun = &lastRunDTO{ID: item.LastRun.ID, Status: item.LastRun.Status, CreatedAt: item.LastRun.StartedAt}
	}
	return dto
}

// listDefinitions serves GET /definitions.
func (d Deps) listDefinitions(w http.ResponseWriter, r *http.Request) {
	items, err := d.Definitions.List(r.Context())
	if err != nil {
		writeError(w, d.Logger, err, "")
		return
	}
	dtos := make([]definitionListItemDTO, len(items))
	for i, item := range items {
		dtos[i] = toDefinitionListItemDTO(item)
	}
	writeJSON(w, http.StatusOK, listEnvelope[definitionListItemDTO]{Items: dtos})
}

// createDefinition serves POST /definitions: it validates and creates version 1. A
// compile failure creates nothing and is mapped by writeError, not answered here.
func (d Deps) createDefinition(w http.ResponseWriter, r *http.Request) {
	var req createDefinitionRequest
	if !decodeJSON(w, d.Logger, r, &req) {
		return
	}
	def, err := d.Definitions.Create(r.Context(), service.CreateDefinition{
		Name: req.Name, Description: req.Description, Nodes: req.Nodes, Edges: req.Edges,
	})
	if err != nil {
		writeError(w, d.Logger, err, "")
		return
	}
	writeJSON(w, http.StatusCreated, def)
}

// getDefinition serves GET /definitions/{workflowId}: the latest saved version.
func (d Deps) getDefinition(w http.ResponseWriter, r *http.Request) {
	workflowID := chi.URLParam(r, "workflowId")
	def, err := d.Definitions.Get(r.Context(), workflowID)
	if err != nil {
		writeError(w, d.Logger, err, codeDefinitionNotFound)
		return
	}
	writeJSON(w, http.StatusOK, def)
}

// getDefinitionVersion serves GET /definitions/{workflowId}/versions/{version}.
func (d Deps) getDefinitionVersion(w http.ResponseWriter, r *http.Request) {
	workflowID := chi.URLParam(r, "workflowId")
	version, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil {
		writeError(w, d.Logger, &badRequestError{message: "version must be an integer"}, "")
		return
	}
	def, err := d.Definitions.GetVersion(r.Context(), workflowID, version)
	if err != nil {
		writeError(w, d.Logger, err, codeDefinitionNotFound)
		return
	}
	writeJSON(w, http.StatusOK, def)
}

// saveDefinition serves PUT /definitions/{workflowId}: it validates and creates a new
// version derived from baseVersion, never overwriting an existing one. A stale
// baseVersion or a compile failure creates nothing; both are mapped by writeError.
func (d Deps) saveDefinition(w http.ResponseWriter, r *http.Request) {
	workflowID := chi.URLParam(r, "workflowId")
	var req saveDefinitionRequest
	if !decodeJSON(w, d.Logger, r, &req) {
		return
	}
	def, err := d.Definitions.Save(r.Context(), service.SaveDefinition{
		WorkflowID:  workflowID,
		BaseVersion: req.BaseVersion,
		Name:        req.Name,
		Description: req.Description,
		Nodes:       req.Nodes,
		Edges:       req.Edges,
	})
	if err != nil {
		writeError(w, d.Logger, err, codeDefinitionNotFound)
		return
	}
	writeJSON(w, http.StatusOK, def)
}

// validateResponse is the discriminated wire shape of POST /definitions/validate, mirroring
// Studio's ValidateResponse union exactly: only the fields relevant to the outcome are
// present (an invalid result carries no runInputSchema key at all, and vice versa).
type validateResponse struct {
	Valid          bool                 `json:"valid"`
	RunInputSchema json.RawMessage      `json:"runInputSchema,omitempty"`
	Errors         []validationErrorDTO `json:"errors,omitempty"`
}

// validateDefinition serves POST /definitions/validate. Unlike createDefinition and
// saveDefinition, an invalid Definition is not an error here (docs/08-interface-spec.md
// §3.1: "校验接口返回确定性的结构化结果"): the endpoint always answers 200 with a result,
// never the error envelope, since nothing was ever meant to be persisted.
func (d Deps) validateDefinition(w http.ResponseWriter, r *http.Request) {
	var req createDefinitionRequest
	if !decodeJSON(w, d.Logger, r, &req) {
		return
	}
	result, err := d.Definitions.Validate(r.Context(), req.Nodes, req.Edges)
	if err != nil {
		writeError(w, d.Logger, err, "")
		return
	}
	if result.Valid {
		writeJSON(w, http.StatusOK, validateResponse{Valid: true, RunInputSchema: result.RunInputSchema})
		return
	}
	writeJSON(w, http.StatusOK, validateResponse{Valid: false, Errors: toValidationErrorDTOs(result.Errors)})
}
