package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the change request as an execution fact: checking, in the claim
// transaction, that every decision basis item a change-request call cites exists in the
// Context Manifest of the Turn it names; re-verifying and recording, in the result
// transaction, the change request a Tool result carries; and the Trace projection of the
// requests an Agent Run recorded. Which Tools take part is decided by the CHANGE_REQUEST
// record kind their registration declares, never by Tool Name.

// codeDecisionBasisInvalid fails a change-request call whose decision basis cites a Turn
// that does not exist or a Manifest item the named Turn's Manifest does not hold. The
// committed Decision is defective, so the Agent Run ends as an invalid Action before any
// Attempt exists; the model is not asked again.
const codeDecisionBasisInvalid = "DECISION_BASIS_INVALID"

// codeChangeRequestInvalid fails a Tool result whose change request the Runtime will not
// record: its content digest disagrees with the content Artifact it names, its patch digest
// is not the workspace's current patch, its verification fact is not a passing fact about
// that patch in this Run, or its decision basis differs from the one the claim checked. The
// result is an invalid Tool result: nothing of it is persisted and the Attempt fails.
const codeChangeRequestInvalid = "CHANGE_REQUEST_INVALID"

// maxTraceChangeRequests bounds the change requests one Agent Trace response carries.
const maxTraceChangeRequests = 64

// decisionBasisArgument is the top-level argument a change-request call carries its
// decision basis in. It is part of the CHANGE_REQUEST record contract: a Tool that declares
// the record names its basis here, so the Runtime can check it against the Manifests.
const decisionBasisArgument = "decisionBasis"

// errDecisionBasisInvalid is the reason a call's decision basis cannot be accepted: it is
// malformed, or cites a Turn or Manifest item the Agent Run does not have. It is distinct
// from a storage error so the claim can tell a defective Decision from a failed read.
type errDecisionBasisInvalid struct {
	reason string
}

func (e *errDecisionBasisInvalid) Error() string {
	return e.reason
}

// errChangeRequestRejected carries the reason a result's change request was refused out of
// the result transaction, which rolls back, so the caller can commit the shared Tool
// failure in its place.
type errChangeRequestRejected struct {
	reason string
}

func (e *errChangeRequestRejected) Error() string {
	return "change request rejected: " + e.reason
}

// decisionBasisOfArguments reads the decision basis of a call of a Tool that records a
// change request. It returns nil for a Tool that declares no CHANGE_REQUEST record, and for
// arguments without the field; a field that is present but malformed is an error.
func decisionBasisOfArguments(metadata domain.ToolMetadata, arguments json.RawMessage) ([]domain.ChangeRequestBasis, error) {
	if !metadata.DeclaresRecord(domain.ToolRecordChangeRequest) {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &fields); err != nil {
		return nil, &errDecisionBasisInvalid{reason: "arguments are not an object"}
	}
	raw, present := fields[decisionBasisArgument]
	if !present || string(raw) == "null" {
		return nil, nil
	}
	basis, err := domain.DecodeChangeRequestBasis(raw)
	if err != nil {
		return nil, &errDecisionBasisInvalid{reason: err.Error()}
	}
	return basis, nil
}

// checkDecisionBasisLocked verifies, inside a transaction that holds the Run aggregate
// lock, that every basis item cites an existing Turn of the Agent Run whose Context
// Manifest holds an item with the cited reference. The Manifest is written with the Turn's
// claim and never changes, so a reference that resolves here resolves for every later
// reader of the request. A nil error means every item resolved; an item that does not
// resolve is reported as *errDecisionBasisInvalid, any other error is a failed read.
func checkDecisionBasisLocked(ctx context.Context, tx store.Tx, agentRunID string, basis []domain.ChangeRequestBasis) error {
	manifests := make(map[int]domain.AgentTurnManifest, len(basis))
	for i, item := range basis {
		manifest, loaded := manifests[item.TurnNo]
		if !loaded {
			turn, err := tx.AgentTurns().GetByRunAndTurnNo(ctx, agentRunID, item.TurnNo)
			if errors.Is(err, domain.ErrNotFound) {
				return &errDecisionBasisInvalid{reason: fmt.Sprintf("decision basis item %d cites turn %d, which the agent run does not have", i, item.TurnNo)}
			}
			if err != nil {
				return err
			}
			manifest, err = tx.AgentTurnManifests().GetByTurnID(ctx, turn.ID)
			if errors.Is(err, domain.ErrNotFound) {
				return &errDecisionBasisInvalid{reason: fmt.Sprintf("decision basis item %d cites turn %d, which has no context manifest", i, item.TurnNo)}
			}
			if err != nil {
				return err
			}
			manifests[item.TurnNo] = manifest
		}
		if !manifestHolds(manifest, item.ManifestRef) {
			return &errDecisionBasisInvalid{reason: fmt.Sprintf("decision basis item %d cites manifest item %q, which the manifest of turn %d does not hold", i, item.ManifestRef, item.TurnNo)}
		}
	}
	return nil
}

func manifestHolds(manifest domain.AgentTurnManifest, ref string) bool {
	for _, item := range manifest.Items {
		if item.Ref == ref {
			return true
		}
	}
	return false
}

// recordChangeRequestLocked re-verifies and records the change request a Tool result
// carries, inside the result transaction and under the Run aggregate lock the caller
// holds, after the result's Artifacts were recorded in the same transaction. Nothing the
// Tool reported is trusted on its own: the content digest must be the digest the declared
// content Artifact is addressed by, the patch digest must still be the workspace's current
// patch, the verification fact must be a passing fact of a required type about that patch
// in this Run, and the decision basis must be the one the claim transaction checked against
// the Manifests. A request the Runtime will not record returns errChangeRequestRejected so
// the whole transaction rolls back.
//
// Within one Agent Run the UNIQUE constraint on the content digest makes a repeated request
// converge: the row that already holds the same content is returned with created false, and
// the Attempt that repeated it records nothing new.
func (s *ExecutionService) recordChangeRequestLocked(
	ctx context.Context,
	tx store.Tx,
	call agentToolCall,
	arguments json.RawMessage,
	result registry.ToolResult,
	now time.Time,
) (domain.ChangeRequest, bool, error) {
	request := result.ChangeRequest
	reg, registered := s.deps.Tools.Get(call.toolName)
	if !registered {
		return domain.ChangeRequest{}, false, fmt.Errorf("execution: tool %q of tool attempt %s is not registered when its result commits", call.toolName, call.attemptID)
	}

	if !isSHA256Hex(request.ContentDigest) {
		return domain.ChangeRequest{}, false, &errChangeRequestRejected{reason: "content digest is not a lowercase hex SHA-256"}
	}
	if request.ContentArtifact.SHA256 != request.ContentDigest || request.ContentArtifact.MediaType != domain.ArtifactMediaTypeTextPlain {
		return domain.ChangeRequest{}, false, &errChangeRequestRejected{reason: "content digest does not match the text artifact the request names"}
	}
	if !declaresArtifact(result.Artifacts, request.ContentArtifact) {
		return domain.ChangeRequest{}, false, &errChangeRequestRejected{reason: "the content artifact is not among the artifacts the result declares"}
	}

	view, err := workspaceViewLocked(ctx, tx, call.agentRunID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ChangeRequest{}, false, &errChangeRequestRejected{reason: "the agent run froze no workspace"}
	}
	if err != nil {
		return domain.ChangeRequest{}, false, err
	}
	if request.PatchDigest != view.PatchDigest {
		return domain.ChangeRequest{}, false, &errChangeRequestRejected{reason: "patch digest is not the workspace's current patch"}
	}

	fact, err := tx.ExecutionFacts().Get(ctx, request.VerificationFactID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ChangeRequest{}, false, &errChangeRequestRejected{reason: "the verification fact does not exist"}
	}
	if err != nil {
		return domain.ChangeRequest{}, false, err
	}
	if fact.RunID != call.runID || fact.SubjectRef != request.PatchDigest || fact.Verdict == nil || !*fact.Verdict || !requiresFactType(reg.Metadata, fact.FactType) {
		return domain.ChangeRequest{}, false, &errChangeRequestRejected{reason: "the verification fact is not a passing required fact about the patch in this run"}
	}

	claimed, err := decisionBasisOfArguments(reg.Metadata, arguments)
	if err != nil {
		return domain.ChangeRequest{}, false, &errChangeRequestRejected{reason: "the call's decision basis cannot be read: " + err.Error()}
	}
	recorded, err := domain.DecodeChangeRequestBasis(request.DecisionBasis)
	if err != nil {
		return domain.ChangeRequest{}, false, &errChangeRequestRejected{reason: err.Error()}
	}
	if !sameBasis(claimed, recorded) {
		return domain.ChangeRequest{}, false, &errChangeRequestRejected{reason: "decision basis differs from the one the call was claimed with"}
	}
	basis, err := json.Marshal(recorded)
	if err != nil {
		return domain.ChangeRequest{}, false, fmt.Errorf("execution: encode decision basis of tool attempt %s: %w", call.attemptID, err)
	}

	stored, created, err := tx.ChangeRequests().CreateOrGet(ctx, domain.ChangeRequest{
		ID:                 s.deps.IDs.NewID(domain.IDPrefixChangeRequest),
		AgentRunID:         call.agentRunID,
		PatchDigest:        request.PatchDigest,
		VerificationFactID: fact.ID,
		ContentArtifactID:  request.ContentArtifact.ArtifactID,
		ContentDigest:      request.ContentDigest,
		DecisionBasis:      basis,
		ToolAttemptID:      call.attemptID,
		CreatedAt:          now,
	})
	if err != nil {
		return domain.ChangeRequest{}, false, fmt.Errorf("execution: record change request of tool attempt %s: %w", call.attemptID, err)
	}
	return stored, created, nil
}

// changeRequestResultOutput completes a Tool result with the record the Runtime allocated:
// the change request's identifier and whether the request converged on an earlier record.
// The Tool cannot know either, so the result transaction adds them before the Attempt's
// result is written, and the tool message the Agent reads names the record.
func changeRequestResultOutput(output json.RawMessage, stored domain.ChangeRequest, created bool) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(output, &fields); err != nil {
		return nil, fmt.Errorf("tool result is not an object: %w", err)
	}
	id, err := json.Marshal(stored.ID)
	if err != nil {
		return nil, err
	}
	fields["changeRequestId"] = id
	if created {
		fields["existing"] = json.RawMessage(`false`)
	} else {
		fields["existing"] = json.RawMessage(`true`)
	}
	return json.Marshal(fields)
}

func declaresArtifact(declared []domain.ArtifactRef, ref domain.ArtifactRef) bool {
	for _, candidate := range declared {
		if candidate == ref {
			return true
		}
	}
	return false
}

func requiresFactType(metadata domain.ToolMetadata, factType string) bool {
	for _, requirement := range metadata.Requires {
		if requirement.FactType == factType {
			return true
		}
	}
	return false
}

func sameBasis(a, b []domain.ChangeRequestBasis) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func isSHA256Hex(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	for i := 0; i < len(digest); i++ {
		c := digest[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// AgentTraceChangeRequests is the bounded list of change requests one Agent Run recorded,
// oldest first, for Trace projection. Truncated reports that the Agent Run holds more than
// Requests shows; the stored rows are never cut. The content is never read: Trace names a
// request by its identifiers, digests and Artifact reference.
type AgentTraceChangeRequests struct {
	Requests  []domain.ChangeRequest
	Truncated bool
}

// agentTraceChangeRequests reads the change requests of agentRunID inside the caller's
// read transaction.
func agentTraceChangeRequests(ctx context.Context, tx store.Tx, agentRunID string) (AgentTraceChangeRequests, error) {
	requests, err := tx.ChangeRequests().ListByAgentRun(ctx, agentRunID, maxTraceChangeRequests+1)
	if err != nil {
		return AgentTraceChangeRequests{}, err
	}
	if len(requests) > maxTraceChangeRequests {
		return AgentTraceChangeRequests{Requests: requests[:maxTraceChangeRequests], Truncated: true}, nil
	}
	return AgentTraceChangeRequests{Requests: requests}, nil
}
