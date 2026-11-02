package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the Agent Run's workspace facts: freezing the repository binding when the
// Agent Run starts, handing a workspace Tool the frozen view under the claim, refusing a
// write outside the frozen allowed paths before any Attempt exists, and recording the
// cumulative patch a Tool result carries in the result transaction. The workspace is the
// frozen base commit plus the recorded patch sequence; no execution environment is ever
// persisted, so every Tool rebuilds one from these facts and nothing else.

// codeWorkspaceNotConfigured fails a call of a workspace Tool on an Agent Run that froze
// no workspace. It is a configuration defect of the Definition, not a refusal the Agent
// could act on, so the Agent Run ends instead of receiving a tool message.
const codeWorkspaceNotConfigured = "WORKSPACE_NOT_CONFIGURED"

// codeWorkspacePatchInvalid fails a Tool result whose workspace patch the Runtime will not
// record: its digest, touched paths or size disagree with the patch text, or a touched
// path lies outside the frozen allowed paths. The result is treated as an invalid Tool
// result: nothing of it is persisted and the Attempt fails.
const codeWorkspacePatchInvalid = "WORKSPACE_PATCH_INVALID"

// maxTraceWorkspacePatches bounds the patch summaries one Agent Trace response carries.
const maxTraceWorkspacePatches = 256

// errWorkspacePatchRejected carries the reason a result's workspace patch was refused out
// of the result transaction, which rolls back, so the caller can commit the shared Tool
// failure in its place.
type errWorkspacePatchRejected struct {
	reason string
}

func (e *errWorkspacePatchRejected) Error() string {
	return "workspace patch rejected: " + e.reason
}

// freezeAgentWorkspaceLocked writes the workspace an Agent node's configuration binds, in
// the Agent Run initialisation transaction the caller is inside. The allowed paths are
// stored in their normalised form, the one every later scope check compares against.
func freezeAgentWorkspaceLocked(ctx context.Context, tx store.Tx, agentRunID string, cfg runtime.AgentWorkspaceConfig, now time.Time) error {
	allowedPaths, err := runtime.NormaliseAllowedPaths(cfg.AllowedPaths)
	if err != nil {
		return fmt.Errorf("execution: freeze workspace of agent run %s: %w", agentRunID, err)
	}
	workspace := domain.AgentWorkspace{
		AgentRunID:   agentRunID,
		RepositoryID: cfg.RepositoryID,
		BaseCommit:   cfg.BaseCommit,
		AllowedPaths: allowedPaths,
		CreatedAt:    now,
	}
	if cfg.DeployTarget != nil {
		workspace.DeployTarget = &domain.WorkspaceDeployTarget{Service: cfg.DeployTarget.Service, Environment: cfg.DeployTarget.Environment}
	}
	return tx.AgentWorkspaces().Create(ctx, workspace)
}

// workspaceViewLocked builds the view a workspace Tool receives: the frozen binding and
// the newest recorded patch, read under the Run aggregate lock the caller holds so the
// view names exactly the workspace version the call runs against. It returns
// domain.ErrNotFound when the Agent Run froze no workspace.
func workspaceViewLocked(ctx context.Context, tx store.Tx, agentRunID string) (*registry.WorkspaceView, error) {
	workspace, err := tx.AgentWorkspaces().GetByAgentRunID(ctx, agentRunID)
	if err != nil {
		return nil, err
	}
	latest, err := tx.WorkspacePatches().Latest(ctx, agentRunID)
	if err != nil {
		return nil, err
	}
	view := &registry.WorkspaceView{
		RepositoryID: workspace.RepositoryID,
		BaseCommit:   workspace.BaseCommit,
		PatchDigest:  domain.WorkspacePatchDigest(""),
		AllowedPaths: slices.Clone(workspace.AllowedPaths),
	}
	if latest != nil {
		view.Patch, view.PatchDigest, view.PatchSeq = latest.Patch, latest.PatchDigest, latest.Seq
	}
	return view, nil
}

// workspaceWriteScopeRefusal decides, before any Attempt exists, whether the path a call
// of a workspace-writing Tool names lies inside the frozen allowed paths. It returns the
// refusal, or nil when the write may proceed or the Tool declares no written path.
func workspaceWriteScopeRefusal(metadata domain.ToolMetadata, arguments json.RawMessage, allowedPaths []string) *runtime.WriteScopeError {
	if metadata.WorkspaceWritePath == "" {
		return nil
	}
	filePath, err := runtime.ResolveWorkspaceWritePath(arguments, metadata.WorkspaceWritePath)
	if err == nil {
		_, err = runtime.CheckWriteScope(allowedPaths, filePath)
	}
	var scope *runtime.WriteScopeError
	if errors.As(err, &scope) {
		return scope
	}
	return nil
}

// scopeDeniedMessage tells the Agent which path was refused and why. It names the path the
// Agent asked for and the frozen prefixes, never the file content.
func scopeDeniedMessage(refusal *runtime.WriteScopeError, allowedPaths []string) string {
	return fmt.Sprintf("The write was refused before it ran: %s. Writes are allowed only under %v.", refusal.Error(), allowedPaths)
}

// validateWorkspacePatch re-derives everything a recorded patch claims from the patch text
// itself, so an executor defect or a tampered result cannot record a patch the Runtime
// did not verify: the text stays within the stored bound, the digest is the one the shared
// digest function yields, the touched paths are exactly the headers of the text, and
// every touched path lies inside the frozen allowed paths.
func validateWorkspacePatch(patch registry.WorkspacePatch, allowedPaths []string) error {
	if len(patch.Patch) > domain.MaxWorkspacePatchBytes {
		return fmt.Errorf("patch is %d bytes, over the %d-byte bound", len(patch.Patch), domain.MaxWorkspacePatchBytes)
	}
	if want := domain.WorkspacePatchDigest(patch.Patch); patch.PatchDigest != want {
		return errors.New("patch digest does not match the patch text")
	}
	derived := domain.WorkspacePatchTouchedPaths(patch.Patch)
	declared := slices.Clone(patch.TouchedPaths)
	slices.Sort(declared)
	declared = slices.Compact(declared)
	if !slices.Equal(derived, declared) {
		return errors.New("touched paths do not match the patch headers")
	}
	for _, touched := range derived {
		if _, err := runtime.CheckWriteScope(allowedPaths, touched); err != nil {
			return fmt.Errorf("touched path is outside the allowed paths: %w", err)
		}
	}
	return nil
}

// recordWorkspacePatchLocked validates and appends the patch a Tool result carries, inside
// the result transaction and under the Run aggregate lock, which is what makes the Seq
// allocation (latest plus one) race-free: Actions of one Agent Run are serial and the lock
// serialises any duplicate delivery, and the UNIQUE constraints still decide if two
// transactions ever reach the database with the same Seq. A patch that fails validation
// returns errWorkspacePatchRejected so the whole transaction rolls back.
func (s *ExecutionService) recordWorkspacePatchLocked(ctx context.Context, tx store.Tx, call agentToolCall, patch registry.WorkspacePatch, now time.Time) error {
	workspace, err := tx.AgentWorkspaces().GetByAgentRunID(ctx, call.agentRunID)
	if errors.Is(err, domain.ErrNotFound) {
		return &errWorkspacePatchRejected{reason: "the agent run froze no workspace"}
	}
	if err != nil {
		return err
	}
	if err := validateWorkspacePatch(patch, workspace.AllowedPaths); err != nil {
		return &errWorkspacePatchRejected{reason: err.Error()}
	}
	latest, err := tx.WorkspacePatches().Latest(ctx, call.agentRunID)
	if err != nil {
		return err
	}
	seq := 1
	if latest != nil {
		seq = latest.Seq + 1
	}
	touched := domain.WorkspacePatchTouchedPaths(patch.Patch)
	if err := tx.WorkspacePatches().Append(ctx, domain.WorkspacePatch{
		ID:            s.deps.IDs.NewID(domain.IDPrefixWorkspacePatch),
		AgentRunID:    call.agentRunID,
		Seq:           seq,
		Patch:         patch.Patch,
		PatchDigest:   patch.PatchDigest,
		TouchedPaths:  touched,
		ToolAttemptID: call.attemptID,
		CreatedAt:     now,
	}); err != nil {
		return fmt.Errorf("execution: record workspace patch %d of agent run %s: %w", seq, call.agentRunID, err)
	}
	return nil
}

// AgentTraceWorkspace is the frozen workspace of one Agent Run with the summaries of its
// recorded patches in Seq order, for Trace projection. PatchesTruncated reports that the
// Agent Run holds more patches than Patches shows; the stored rows are never cut. The
// patch text is deliberately not read: Trace shows the sequence, digest and touched paths.
type AgentTraceWorkspace struct {
	Workspace        domain.AgentWorkspace
	Patches          []domain.WorkspacePatch
	PatchesTruncated bool
}

// agentTraceWorkspace reads the workspace projection of agentRunID inside the caller's
// read transaction, or nil when the Agent Run froze none.
func agentTraceWorkspace(ctx context.Context, tx store.Tx, agentRunID string) (*AgentTraceWorkspace, error) {
	workspace, err := tx.AgentWorkspaces().GetByAgentRunID(ctx, agentRunID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	patches, err := tx.WorkspacePatches().ListSummariesByAgentRun(ctx, agentRunID, maxTraceWorkspacePatches+1)
	if err != nil {
		return nil, err
	}
	out := &AgentTraceWorkspace{Workspace: workspace, Patches: patches}
	if len(patches) > maxTraceWorkspacePatches {
		out.Patches, out.PatchesTruncated = patches[:maxTraceWorkspacePatches], true
	}
	return out, nil
}
