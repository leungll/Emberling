package main

import (
	"fmt"

	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/workspace"
)

// registerWorkspaceTools registers read_file, list_files and write_file when at least one
// repository is configured. Without one the Tools are absent, so a Definition that names
// them fails explicitly instead of running against a repository that does not exist. The
// Tools read the repositories through one local, read-only port.
func registerWorkspaceTools(tools *registry.ToolRegistry, repositories map[string]string) error {
	if len(repositories) == 0 {
		return nil
	}
	for _, reg := range workspace.Registrations(workspace.NewLocalGit(repositories)) {
		if err := tools.Register(reg); err != nil {
			return fmt.Errorf("register tool %s: %w", reg.Metadata.Name, err)
		}
	}
	return nil
}
