package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/leungll/Emberling/backend/internal/registry"
)

// checkout is one throwaway rebuild of the workspace: the base commit exported into a
// temporary directory, turned into a git repository whose single commit is the base, with
// the cumulative patch applied to the working tree. Nothing of it outlives the call.
type checkout struct {
	root string
	// realRoot is root with symbolic links resolved, the prefix every path the call uses
	// must stay under once its own links are resolved.
	realRoot string
}

// errPathOutsideCheckout rejects a path that would leave the checkout: an absolute path,
// a "." or ".." segment, or a symbolic link inside the tree that resolves outside it.
var errPathOutsideCheckout = errors.New("workspace: path resolves outside the checkout")

// rebuild exports view.BaseCommit of view.RepositoryID into a fresh temporary directory,
// commits it as the base and applies view.Patch to the working tree. The caller removes
// the checkout with close whatever happened afterwards.
func rebuild(ctx context.Context, repos Repositories, view *registry.WorkspaceView) (*checkout, error) {
	if view == nil {
		return nil, errors.New("workspace: the call carries no workspace view")
	}
	root, err := os.MkdirTemp("", "emberling-workspace-")
	if err != nil {
		return nil, fmt.Errorf("workspace: create checkout directory: %w", err)
	}
	c := &checkout{root: root}
	if c.realRoot, err = filepath.EvalSymlinks(root); err != nil {
		c.close()
		return nil, fmt.Errorf("workspace: resolve checkout directory: %w", err)
	}
	if err := repos.Export(ctx, view.RepositoryID, view.BaseCommit, root); err != nil {
		c.close()
		return nil, err
	}
	for _, args := range [][]string{
		{"init", "-q"},
		// -f stages files a .gitignore in the tree would skip: the base commit must hold
		// the whole exported tree, or the diff would miss a write to an ignored path.
		{"add", "-A", "-f"},
		{"commit", "-q", "--allow-empty", "-m", "base"},
	} {
		if err := c.git(ctx, nil, args...); err != nil {
			c.close()
			return nil, err
		}
	}
	if view.Patch != "" {
		if err := c.git(ctx, strings.NewReader(view.Patch), "apply", "--whitespace=nowarn", "-"); err != nil {
			c.close()
			return nil, fmt.Errorf("workspace: apply the current patch: %w", err)
		}
	}
	return c, nil
}

// close removes the checkout. Errors are ignored: a leftover temporary directory is a
// housekeeping matter, never a workspace fact.
func (c *checkout) close() {
	_ = os.RemoveAll(c.root)
}

// git runs one git command inside the checkout with stdin as its input, reporting a
// bounded slice of stderr on failure.
func (c *checkout) git(ctx context.Context, stdin *strings.Reader, args ...string) error {
	var stderr bytes.Buffer
	cmd := gitCommand(ctx, c.root, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("workspace: git %s: %w: %s", args[0], err, boundedStderr(stderr.Bytes()))
	}
	return nil
}

// cumulativePatch stages every change of the working tree and returns the unified diff
// against the base commit: the whole cumulative patch, renames reported as a deletion and
// an addition so every touched path appears in a header.
func (c *checkout) cumulativePatch(ctx context.Context) (string, error) {
	if err := c.git(ctx, nil, "add", "-A", "-f"); err != nil {
		return "", err
	}
	var out, stderr bytes.Buffer
	cmd := gitCommand(ctx, c.root, "diff", "--cached", "--no-color", "--no-ext-diff", "--no-renames", "HEAD")
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("workspace: git diff: %w: %s", err, boundedStderr(stderr.Bytes()))
	}
	return out.String(), nil
}

// resolve maps a repository-relative path to its location inside the checkout. It refuses
// an absolute path and a "." or ".." segment outright, and refuses a path whose existing
// prefix resolves, through a symbolic link, to a location outside the checkout. The
// returned normalised path is the spelling a patch header uses.
func (c *checkout) resolve(relPath string) (normalised, absolute string, err error) {
	switch {
	case relPath == "" || relPath == ".":
		return ".", c.root, nil
	case strings.HasPrefix(relPath, "/"), strings.ContainsRune(relPath, '\\'):
		return "", "", errPathOutsideCheckout
	}
	for _, segment := range strings.Split(relPath, "/") {
		if segment == "." || segment == ".." {
			return "", "", errPathOutsideCheckout
		}
	}
	normalised = path.Clean(relPath)
	if normalised == "." || normalised == ".git" || strings.HasPrefix(normalised, ".git/") {
		return "", "", errPathOutsideCheckout
	}
	absolute = filepath.Join(c.root, filepath.FromSlash(normalised))

	// Walk the existing prefix: the longest leading part of the path that exists is
	// resolved through its symbolic links and must still be under the checkout. Missing
	// trailing components are where a write will create new entries.
	existing := absolute
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", "", fmt.Errorf("workspace: resolve %q: %w", normalised, err)
	}
	if resolved != c.realRoot && !strings.HasPrefix(resolved, c.realRoot+string(filepath.Separator)) {
		return "", "", errPathOutsideCheckout
	}
	return normalised, absolute, nil
}
