package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// workspaceDocument is the part of one Workspace document in AX_WORKSPACES_YAML that the
// supervisor reads. Field names follow the platform's lower-camel-case encoding.
type workspaceDocument struct {
	Kind string `yaml:"kind"`
	Spec struct {
		Files []workspaceFile `yaml:"files"`
	} `yaml:"spec"`
}

// workspaceFile is one inline file: a path relative to the workspace root and its full
// content.
type workspaceFile struct {
	Path    string `yaml:"path"`
	Content string `yaml:"content"`
}

// parseWorkspace decodes the multi-document stream and returns the files of its only
// Workspace document. The stream must hold exactly one Workspace document, and every file
// path must stay inside the workspace root: non-empty, relative, already clean, free of
// ".." segments and unique. The platform itself checks only that a path is non-empty, so
// the supervisor refuses anything else rather than writing outside the tree the runner
// sent.
func parseWorkspace(stream string) ([]workspaceFile, error) {
	decoder := yaml.NewDecoder(strings.NewReader(stream))
	var workspaces []workspaceDocument
	for {
		var doc workspaceDocument
		err := decoder.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode workspace stream: %w", err)
		}
		if doc.Kind == "Workspace" {
			workspaces = append(workspaces, doc)
		}
	}
	if len(workspaces) != 1 {
		return nil, fmt.Errorf("workspace stream holds %d Workspace documents, want exactly one", len(workspaces))
	}
	files := workspaces[0].Spec.Files
	seen := make(map[string]bool, len(files))
	for i, file := range files {
		if err := checkWorkspacePath(file.Path); err != nil {
			return nil, fmt.Errorf("workspace file %d: %w", i, err)
		}
		if seen[file.Path] {
			return nil, fmt.Errorf("workspace file %d: path repeats an earlier file", i)
		}
		seen[file.Path] = true
	}
	return files, nil
}

// checkWorkspacePath rejects a file path that could name anything outside the workspace
// root or that names the same file in more than one spelling.
func checkWorkspacePath(name string) error {
	switch {
	case name == "":
		return errors.New("path is empty")
	case strings.HasPrefix(name, "/"):
		return errors.New("path is absolute")
	case path.Clean(name) != name:
		return errors.New("path is not clean")
	case slices.Contains(strings.Split(name, "/"), ".."):
		return errors.New("path has a parent segment")
	}
	return nil
}

// writeWorkspace writes files under dir (directories 0755, files 0644), reads every file
// back, creates the .gotmp directory the fixed environment names as GOTMPDIR, and returns
// the tree as read back, which is what the digest covers. Writes go
// through an os.Root so that nothing, including a symbolic link already in dir, can lead
// outside it.
func writeWorkspace(dir string, files []workspaceFile) (map[string][]byte, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: workspace directories are 0755 by contract, matching the platform's own workspace setup
		return nil, fmt.Errorf("create work directory: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open work directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	for i, file := range files {
		if parent := path.Dir(file.Path); parent != "." {
			if err := root.MkdirAll(parent, 0o755); err != nil {
				return nil, fmt.Errorf("workspace file %d: create directory: %w", i, err)
			}
		}
		if err := root.WriteFile(file.Path, []byte(file.Content), 0o644); err != nil {
			return nil, fmt.Errorf("workspace file %d: write: %w", i, err)
		}
	}
	written := make(map[string][]byte, len(files))
	for i, file := range files {
		content, err := root.ReadFile(file.Path)
		if err != nil {
			return nil, fmt.Errorf("workspace file %d: read back: %w", i, err)
		}
		written[file.Path] = content
	}
	if err := root.MkdirAll(".gotmp", 0o755); err != nil {
		return nil, fmt.Errorf("create .gotmp: %w", err)
	}
	return written, nil
}
