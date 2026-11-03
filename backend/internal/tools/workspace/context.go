package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"

	"github.com/leungll/Emberling/backend/internal/registry"
)

// ContextFile is one repository file read for a Tool that needs the repository's own text
// beside the patch, such as a review that reads the project rules and the advisories.
type ContextFile struct {
	// Path is the repository-relative path, in the spelling a patch header uses.
	Path string
	// Content is the file's text at the workspace's current patch, cut to the reader's
	// per-file bound; Truncated says when it was cut.
	Content   string
	Truncated bool
}

// Default bounds of a ContextReader: a handful of short documents, never a tree.
const (
	DefaultContextMaxFiles     = 8
	DefaultContextMaxFileBytes = 32 << 10
)

// ContextReader reads a fixed, bounded set of repository files from a rebuilt workspace. It
// is a read-only port for a Tool that reviews the patch with the repository's own text in
// view; it is not an Agent Tool itself, records nothing and never writes the checkout.
type ContextReader struct {
	repos Repositories
	// MaxFiles bounds how many files one read returns; later entries are left out.
	MaxFiles int
	// MaxFileBytes bounds each file's content; a longer file is cut and flagged.
	MaxFileBytes int
}

// NewContextReader returns a reader over repos with the default bounds.
func NewContextReader(repos Repositories) *ContextReader {
	return &ContextReader{repos: repos, MaxFiles: DefaultContextMaxFiles, MaxFileBytes: DefaultContextMaxFileBytes}
}

// ReadFiles rebuilds view's workspace and reads the named entries in order: a regular file
// verbatim, a directory's regular files sorted by name without descending into its
// subdirectories. A missing entry and a symbolic link contribute nothing, because a review
// must not fail for a repository that keeps no rules file, and a link may point anywhere.
// A path that would leave the checkout is refused. The read stops at MaxFiles.
func (r *ContextReader) ReadFiles(ctx context.Context, view *registry.WorkspaceView, names []string) ([]ContextFile, error) {
	if r == nil || r.repos == nil {
		return nil, errors.New("workspace: the context reader has no repository port")
	}
	c, err := rebuild(ctx, r.repos, view)
	if err != nil {
		return nil, err
	}
	defer c.close()

	var files []ContextFile
	for _, name := range names {
		if len(files) >= r.MaxFiles {
			break
		}
		normalised, absolute, err := c.resolve(name)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(absolute)
		switch {
		case errors.Is(err, os.ErrNotExist):
			continue
		case err != nil:
			return nil, fmt.Errorf("workspace: stat %q: %w", normalised, err)
		case info.Mode().IsRegular():
			file, err := r.readOne(normalised, absolute)
			if err != nil {
				return nil, err
			}
			files = append(files, file)
		case info.IsDir():
			entries, err := os.ReadDir(absolute)
			if err != nil {
				return nil, fmt.Errorf("workspace: list %q: %w", normalised, err)
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
			for _, entry := range entries {
				if len(files) >= r.MaxFiles {
					break
				}
				if !entry.Type().IsRegular() {
					continue
				}
				child := path.Join(normalised, entry.Name())
				_, childAbsolute, err := c.resolve(child)
				if err != nil {
					return nil, err
				}
				file, err := r.readOne(child, childAbsolute)
				if err != nil {
					return nil, err
				}
				files = append(files, file)
			}
		}
	}
	return files, nil
}

func (r *ContextReader) readOne(normalised, absolute string) (ContextFile, error) {
	content, err := os.ReadFile(absolute)
	if err != nil {
		return ContextFile{}, fmt.Errorf("workspace: read %q: %w", normalised, err)
	}
	file := ContextFile{Path: normalised}
	if len(content) > r.MaxFileBytes {
		content, file.Truncated = content[:r.MaxFileBytes], true
	}
	file.Content = string(content)
	return file, nil
}
