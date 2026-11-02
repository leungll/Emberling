// Package workspace implements the three workspace Tools an Agent reads and writes code
// with: `read_file`, `list_files` and `write_file`. The workspace itself is never a
// directory the Tools keep: it is the Agent Run's frozen base commit plus the cumulative
// patch the Runtime recorded, both handed to every call in its WorkspaceView. Each call
// rebuilds a throwaway checkout in a temporary directory from those two facts, performs
// its one operation there and removes the directory again, so a crash leaves nothing
// behind and a restarted process rebuilds the same workspace from the same facts.
//
// The Tools perform exactly one registered operation each. They own no retry, timeout,
// state transition or Event: `write_file` only computes the new cumulative patch and
// returns it; whether the patch is recorded is the Runtime's decision in the result
// transaction. The local repository is read through one read-only port that never
// contacts a remote.
package workspace

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Repositories is the read-only port the three Tools share: it exports the tree of one
// commit of a registered repository into a directory. Implementations read a local
// repository and never fetch.
type Repositories interface {
	Export(ctx context.Context, repositoryID, commit, dir string) error
}

// ErrRepositoryNotRegistered reports a repository identifier the port does not know.
var ErrRepositoryNotRegistered = errors.New("workspace: repository is not registered")

// LocalGit exports commits from local git repositories registered by identifier, through
// `git archive` on the repository's object store. It runs no network operation.
type LocalGit struct {
	paths map[string]string
}

// NewLocalGit returns a port over the registered repository paths, keyed by identifier.
func NewLocalGit(paths map[string]string) *LocalGit {
	copied := make(map[string]string, len(paths))
	for id, p := range paths {
		copied[id] = p
	}
	return &LocalGit{paths: copied}
}

// maxGitStderrBytes bounds the git diagnostics an error carries.
const maxGitStderrBytes = 256

// maxExportEntryBytes bounds one file of an exported tree. A source repository a patch
// applies to has no business holding a larger file, and the bound keeps a hostile archive
// from filling the disk.
const maxExportEntryBytes = 64 << 20

// Checkout permissions: the checkout belongs to this process alone and is removed when the
// call returns, so nothing wider than the owner is ever granted. git records only the
// executable bit of a file, which is what the exported mode carries over.
const (
	checkoutDirMode        = 0o700
	checkoutFileMode       = 0o600
	checkoutExecutableMode = 0o700
)

// Export writes the tree at commit into dir, which must exist and be empty. Regular files,
// directories and symbolic links are recreated; every other entry kind is skipped. Entry
// names are checked to stay inside dir, so a hostile archive cannot escape it.
func (g *LocalGit) Export(ctx context.Context, repositoryID, commit, dir string) error {
	repoPath, ok := g.paths[repositoryID]
	if !ok {
		return fmt.Errorf("%w: %q", ErrRepositoryNotRegistered, repositoryID)
	}
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd := gitCommand(ctx, repoPath, "-C", repoPath, "archive", "--format=tar", commit)
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("workspace: export %s@%s: git archive: %w: %s", repositoryID, commit, err, boundedStderr(stderr.Bytes()))
	}
	return extractTar(&out, dir)
}

// extractTar unpacks a tar stream into dir.
func extractTar(r io.Reader, dir string) error {
	reader := tar.NewReader(r)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("workspace: read archive: %w", err)
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if name == "." || name == "" {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("workspace: archive entry %q escapes the checkout", header.Name)
		}
		target := filepath.Join(dir, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, checkoutDirMode); err != nil {
				return fmt.Errorf("workspace: create directory: %w", err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), checkoutDirMode); err != nil {
				return fmt.Errorf("workspace: create directory: %w", err)
			}
			mode := os.FileMode(checkoutFileMode)
			if header.Mode&0o111 != 0 {
				mode = checkoutExecutableMode
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return fmt.Errorf("workspace: create file: %w", err)
			}
			written, err := io.Copy(f, io.LimitReader(reader, maxExportEntryBytes+1))
			if err != nil {
				_ = f.Close()
				return fmt.Errorf("workspace: write file: %w", err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("workspace: close file: %w", err)
			}
			if written > maxExportEntryBytes {
				return fmt.Errorf("workspace: archive entry %q is over the %d-byte bound", header.Name, maxExportEntryBytes)
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), checkoutDirMode); err != nil {
				return fmt.Errorf("workspace: create directory: %w", err)
			}
			// The link target is recreated as the repository recorded it. Whether it
			// resolves inside the checkout is decided when a path through it is used.
			if err := os.Symlink(header.Linkname, target); err != nil {
				return fmt.Errorf("workspace: create symlink: %w", err)
			}
		default:
			// Hard links, devices and other entry kinds are not part of a source tree a
			// patch applies to.
		}
	}
}

// gitCommand builds a git invocation isolated from the host's user and system
// configuration, hooks and credential prompts. dir is the directory the command runs in.
func gitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	base := []string{
		"-c", "user.name=emberling", "-c", "user.email=emberling@localhost",
		"-c", "commit.gpgsign=false", "-c", "core.autocrlf=false", "-c", "core.hooksPath=/dev/null",
		"-c", "init.defaultBranch=main", "-c", "diff.renames=false",
	}
	// The executable is the fixed name "git" and every argument is built by this package
	// from registered configuration, a frozen commit or a fixed verb; no argument comes
	// from a model or a request.
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...) //nolint:gosec // G204: fixed executable, arguments assembled by this package
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C",
	)
	return cmd
}

// boundedStderr renders git diagnostics for an error, cut to maxGitStderrBytes.
func boundedStderr(stderr []byte) string {
	trimmed := strings.TrimSpace(string(stderr))
	if len(trimmed) > maxGitStderrBytes {
		trimmed = trimmed[:maxGitStderrBytes]
	}
	return trimmed
}
