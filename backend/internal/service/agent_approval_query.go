package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// ApprovalView is one approval with what a person needs to decide it: the Tool the Action
// calls, the committed arguments the approval covers, and where the Action lives. It is a
// read-only projection; the approval row stays the authority.
type ApprovalView struct {
	Approval   domain.Approval
	ToolName   string
	Arguments  json.RawMessage
	WorkflowID string
	NodeRunID  string
}

// maxApprovalListItems bounds the approval list read.
const maxApprovalListItems = 200

// ListApprovals returns the approvals in one status, oldest request first, each with its
// Tool and arguments, in a single consistent read.
func (s *QueryService) ListApprovals(ctx context.Context, status domain.ApprovalStatus) ([]ApprovalView, error) {
	if !status.IsValid() {
		return nil, &RunListQueryError{Message: fmt.Sprintf("status must be one of PENDING, APPROVED, REJECTED or EXPIRED, got %q", status)}
	}
	var views []ApprovalView
	err := s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		approvals, err := tx.Approvals().ListByStatus(ctx, status, maxApprovalListItems)
		if err != nil {
			return err
		}
		views = make([]ApprovalView, 0, len(approvals))
		for _, approval := range approvals {
			view, err := approvalView(ctx, tx, approval)
			if err != nil {
				return err
			}
			views = append(views, view)
		}
		return nil
	})
	return views, err
}

// GetApproval returns one approval with its Tool and arguments, or domain.ErrNotFound.
func (s *QueryService) GetApproval(ctx context.Context, approvalID string) (ApprovalView, error) {
	var view ApprovalView
	err := s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		approval, err := tx.Approvals().Get(ctx, approvalID)
		if err != nil {
			return err
		}
		view, err = approvalView(ctx, tx, approval)
		return err
	})
	return view, err
}

func approvalView(ctx context.Context, tx store.Tx, approval domain.Approval) (ApprovalView, error) {
	action, err := tx.AgentActions().Get(ctx, approval.ActionID)
	if err != nil {
		return ApprovalView{}, err
	}
	decision, err := tx.AgentDecisions().Get(ctx, action.DecisionID)
	if err != nil {
		return ApprovalView{}, err
	}
	run, err := tx.Runs().Get(ctx, approval.RunID)
	if err != nil {
		return ApprovalView{}, err
	}
	agentRun, err := tx.AgentRuns().Get(ctx, approval.AgentRunID)
	if err != nil {
		return ApprovalView{}, err
	}
	view := ApprovalView{Approval: approval, Arguments: decision.Arguments, WorkflowID: run.WorkflowID, NodeRunID: agentRun.NodeRunID}
	if decision.ToolName != nil {
		view.ToolName = *decision.ToolName
	}
	return view, nil
}

// ApprovalDetail is one approval with the material a person decides on: the view, the
// change request the call names, if any, and the full text of the workspace patch that
// request is about. Both additions are read-only projections of committed facts; a call
// that names no change request, or names one this Agent Run never recorded, carries
// neither.
type ApprovalDetail struct {
	ApprovalView
	ChangeRequest *ApprovalChangeRequest
	Patch         *domain.WorkspacePatch
}

// ApprovalChangeRequest is a recorded change request with its rendered Markdown and its
// decision basis resolved against the Context Manifests it cites.
type ApprovalChangeRequest struct {
	Request domain.ChangeRequest
	// Content is the Markdown of the request's content Artifact, verified against the
	// Artifact's committed size and digest before it is served.
	Content string
	Basis   []ApprovalDecisionBasis
}

// ApprovalDecisionBasis is one cited item of a change request's decision basis, with the
// Turn it names and the Manifest item the reference resolves to. Item is nil when the Turn
// or the item cannot be resolved any more; the claim transaction checked every reference
// when the request was made, so a nil Item is a projection gap, never a hidden refusal.
type ApprovalDecisionBasis struct {
	domain.ChangeRequestBasis
	TurnID string
	Item   *domain.ManifestItem
}

// maxApprovalContentBytes bounds the change request content the detail serves. The content
// Artifact is bounded at write time by the artifact store; the read bound protects the
// response against a store configured larger than the detail is willing to carry.
const maxApprovalContentBytes = 1 << 20

// GetApprovalDetail returns one approval with its Tool and arguments plus the change
// request and patch the call names, or domain.ErrNotFound. The rows are read in one
// consistent read transaction; the change request content is read from artifact storage
// after it, by the storage key the transaction resolved, and verified against the
// committed metadata before it is served.
func (s *QueryService) GetApprovalDetail(ctx context.Context, approvalID string) (ApprovalDetail, error) {
	var detail ApprovalDetail
	var content *store.ArtifactRecord
	err := s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		approval, err := tx.Approvals().Get(ctx, approvalID)
		if err != nil {
			return err
		}
		detail.ApprovalView, err = approvalView(ctx, tx, approval)
		if err != nil {
			return err
		}
		request, err := namedChangeRequest(ctx, tx, approval.AgentRunID, detail.Arguments)
		if err != nil || request == nil {
			return err
		}
		basis, err := resolveDecisionBasis(ctx, tx, *request)
		if err != nil {
			return err
		}
		detail.ChangeRequest = &ApprovalChangeRequest{Request: *request, Basis: basis}
		record, err := tx.Artifacts().GetForRun(ctx, approval.RunID, request.ContentArtifactID)
		if err != nil {
			return fmt.Errorf("approval %s: change request %s content artifact: %w", approvalID, request.ID, err)
		}
		content = &record
		detail.Patch, err = tx.WorkspacePatches().GetByDigest(ctx, approval.AgentRunID, request.PatchDigest)
		return err
	})
	if err != nil {
		return ApprovalDetail{}, err
	}
	if content != nil {
		text, err := s.readArtifactText(ctx, *content)
		if err != nil {
			return ApprovalDetail{}, fmt.Errorf("approval %s: change request %s: %w", approvalID, detail.ChangeRequest.Request.ID, err)
		}
		detail.ChangeRequest.Content = text
	}
	return detail, nil
}

// namedChangeRequest returns the change request the call arguments name through their
// top-level changeRequestId, the one argument a release call carries, or nil when they name
// none or name a request of another Agent Run. A request of another Agent Run is treated as
// absent rather than shown: an approval shows only its own Agent Run's facts.
func namedChangeRequest(ctx context.Context, tx store.Tx, agentRunID string, arguments json.RawMessage) (*domain.ChangeRequest, error) {
	if len(arguments) == 0 {
		return nil, nil
	}
	var named struct {
		ChangeRequestID string `json:"changeRequestId"`
	}
	if err := json.Unmarshal(arguments, &named); err != nil || named.ChangeRequestID == "" {
		return nil, nil
	}
	request, err := tx.ChangeRequests().Get(ctx, named.ChangeRequestID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, err
	case request.AgentRunID != agentRunID:
		return nil, nil
	}
	return &request, nil
}

// resolveDecisionBasis decodes the request's recorded basis and resolves each item against
// the Manifest of the Turn it cites, reading every Turn and Manifest at most once.
func resolveDecisionBasis(ctx context.Context, tx store.Tx, request domain.ChangeRequest) ([]ApprovalDecisionBasis, error) {
	items, err := domain.DecodeChangeRequestBasis(request.DecisionBasis)
	if err != nil {
		return nil, fmt.Errorf("change request %s: recorded decision basis: %w", request.ID, err)
	}
	type turnManifest struct {
		turnID   string
		manifest *domain.AgentTurnManifest
	}
	resolved := make(map[int]turnManifest, len(items))
	out := make([]ApprovalDecisionBasis, 0, len(items))
	for _, item := range items {
		entry, seen := resolved[item.TurnNo]
		if !seen {
			turn, err := tx.AgentTurns().GetByRunAndTurnNo(ctx, request.AgentRunID, item.TurnNo)
			switch {
			case errors.Is(err, domain.ErrNotFound):
			case err != nil:
				return nil, err
			default:
				entry.turnID = turn.ID
				manifest, err := tx.AgentTurnManifests().GetByTurnID(ctx, turn.ID)
				switch {
				case errors.Is(err, domain.ErrNotFound):
				case err != nil:
					return nil, err
				default:
					entry.manifest = &manifest
				}
			}
			resolved[item.TurnNo] = entry
		}
		basis := ApprovalDecisionBasis{ChangeRequestBasis: item, TurnID: entry.turnID}
		if entry.manifest != nil {
			for i := range entry.manifest.Items {
				if entry.manifest.Items[i].Ref == item.ManifestRef {
					found := entry.manifest.Items[i]
					basis.Item = &found
					break
				}
			}
		}
		out = append(out, basis)
	}
	return out, nil
}

// readArtifactText reads one text Artifact's content in full, verifies it against the
// committed size and digest, and returns it as a string. The storage key never reaches the
// caller or an error.
func (s *QueryService) readArtifactText(ctx context.Context, record store.ArtifactRecord) (string, error) {
	if s.deps.ArtifactContent == nil {
		return "", fmt.Errorf("artifact %s: artifact content storage is not configured", record.Artifact.ArtifactID)
	}
	if record.Artifact.MediaType != domain.ArtifactMediaTypeTextPlain || record.Artifact.SizeBytes > maxApprovalContentBytes {
		return "", fmt.Errorf("artifact %s: %w", record.Artifact.ArtifactID, errArtifactContentUnavailable)
	}
	content, err := s.deps.ArtifactContent.Open(ctx, record.StorageKey)
	if err != nil {
		return "", fmt.Errorf("artifact %s: %w", record.Artifact.ArtifactID, errArtifactContentUnavailable)
	}
	defer func() { _ = content.Close() }()
	if err := verifyArtifactContent(content, record.Artifact); err != nil {
		return "", fmt.Errorf("artifact %s: %w", record.Artifact.ArtifactID, err)
	}
	text, err := io.ReadAll(io.LimitReader(content, record.Artifact.SizeBytes))
	if err != nil {
		return "", fmt.Errorf("artifact %s: %w", record.Artifact.ArtifactID, errArtifactContentUnavailable)
	}
	return string(text), nil
}
