package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// KeyWorkspaceRepositories lists the repositories an Agent workspace may bind to, as a
// comma-separated list of `<repository id>=<absolute path>` entries such as
// `vuln-demo=/srv/repos/vuln-demo`. The identifier is what a Definition's agent node names
// in workspace.repositoryId; the path is where the workspace Tools read the repository.
// Empty means no repository is registered, so no workspace compiles. The list is read once
// at startup: the Compiler is handed the identifiers and nothing here reads a repository.
const KeyWorkspaceRepositories = EnvPrefix + "WORKSPACE_REPOSITORIES"

// workspaceRepositoryIDPattern is the allowed shape of a repository identifier: a
// lowercase letter followed by lowercase letters, digits or hyphens.
var workspaceRepositoryIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ParseWorkspaceRepositories parses the repository list. Every entry must name a
// well-formed identifier once and an absolute path; the error names the entry index and
// the rule, never the value.
func ParseWorkspaceRepositories(value string) (map[string]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	entries := strings.Split(value, ",")
	repositories := make(map[string]string, len(entries))
	var problems []string
	for i, entry := range entries {
		entry = strings.TrimSpace(entry)
		id, repoPath, found := strings.Cut(entry, "=")
		id = strings.TrimSpace(id)
		repoPath = strings.TrimSpace(repoPath)
		switch {
		case !found || id == "" || repoPath == "":
			problems = append(problems, fmt.Sprintf("entry %d: must be <repository id>=<absolute path>", i))
			continue
		case !workspaceRepositoryIDPattern.MatchString(id):
			problems = append(problems, fmt.Sprintf("entry %d: repository id must match %s", i, workspaceRepositoryIDPattern))
			continue
		case !filepath.IsAbs(repoPath):
			problems = append(problems, fmt.Sprintf("entry %d: path must be absolute", i))
			continue
		}
		if _, duplicate := repositories[id]; duplicate {
			problems = append(problems, fmt.Sprintf("entry %d: repository id repeats an earlier entry", i))
			continue
		}
		repositories[id] = filepath.Clean(repoPath)
	}
	if len(problems) > 0 {
		return nil, &InvalidConfigError{Key: KeyWorkspaceRepositories, Reason: strings.Join(problems, "; ")}
	}
	return repositories, nil
}

// WorkspaceRepositoryIDs returns the registered repository identifiers in sorted order,
// the set the Compiler validates an agent node's workspace.repositoryId against.
func (c Config) WorkspaceRepositoryIDs() []string {
	ids := make([]string, 0, len(c.WorkspaceRepositories))
	for id := range c.WorkspaceRepositories {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// workspaceRepositories reads the optional repository list.
func (l *loader) workspaceRepositories() map[string]string {
	repositories, err := ParseWorkspaceRepositories(l.env(KeyWorkspaceRepositories))
	if err != nil {
		var invalid *InvalidConfigError
		if errors.As(err, &invalid) {
			l.fail(KeyWorkspaceRepositories, invalid.Reason)
		}
		return nil
	}
	return repositories
}

// validateWorkspaceRepositories re-checks what ParseWorkspaceRepositories enforces, for a
// Config assembled by another path.
func validateWorkspaceRepositories(repositories map[string]string) error {
	for id, repoPath := range repositories {
		if !workspaceRepositoryIDPattern.MatchString(id) {
			return &InvalidConfigError{Key: KeyWorkspaceRepositories, Reason: "repository id must match " + workspaceRepositoryIDPattern.String()}
		}
		if !filepath.IsAbs(repoPath) {
			return &InvalidConfigError{Key: KeyWorkspaceRepositories, Reason: "path must be absolute"}
		}
	}
	return nil
}
