package main

import (
	"context"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/reviewpatch"
	"github.com/leungll/Emberling/backend/internal/tools/workspace"
)

// workspaceContextReader bridges review_patch's workspace port to the workspace package's
// bounded context reader, so a review reads the repository's rules and advisories from the
// same read-only rebuild the workspace Tools use.
type workspaceContextReader struct {
	reader *workspace.ContextReader
}

func (r workspaceContextReader) ReadFiles(ctx context.Context, view *registry.WorkspaceView, names []string) ([]reviewpatch.ContextFile, error) {
	files, err := r.reader.ReadFiles(ctx, view, names)
	if err != nil {
		return nil, err
	}
	out := make([]reviewpatch.ContextFile, len(files))
	for i, file := range files {
		out[i] = reviewpatch.ContextFile{Path: file.Path, Content: file.Content, Truncated: file.Truncated}
	}
	return out, nil
}

// registerReviewPatchTool registers the review_patch Tool when a verification runner is
// configured, under the same condition as create_change_request: the review only has a
// place in the repair workflow that runner serves, and an Agent that does not enable the
// review simply leaves the Tool out of its allowlist. The Tool resolves the Agent Run's
// frozen review model through the Model Registry, reads the repository through the
// registered workspace repositories and keeps each opinion in artifacts.
func registerReviewPatchTool(tools *registry.ToolRegistry, cfg config.VerifyRunner, models *registry.ModelRegistry, artifacts reviewpatch.ArtifactTextStore, repositories map[string]string) error {
	if !cfg.Enabled() {
		return nil
	}
	ports := reviewpatch.Ports{
		Models:    models,
		Artifacts: artifacts,
		Workspace: workspaceContextReader{reader: workspace.NewContextReader(workspace.NewLocalGit(repositories))},
	}
	if err := tools.Register(reviewpatch.Registration(ports)); err != nil {
		return fmt.Errorf("register tool %s: %w", reviewpatch.ToolName, err)
	}
	return nil
}
