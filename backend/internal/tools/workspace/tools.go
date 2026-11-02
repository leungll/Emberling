package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// Stable Registry names of the three Tools.
const (
	ReadFileToolName  = "read_file"
	ListFilesToolName = "list_files"
	WriteFileToolName = "write_file"
)

// MaxReadBytes bounds the content one read_file call returns; longer files are cut and
// marked truncated.
const MaxReadBytes = 64 << 10

// MaxListEntries bounds the entries one list_files call returns.
const MaxListEntries = 500

// MaxWriteBytes bounds the content one write_file call accepts, matching the patch bound
// the Runtime records: a file larger than the whole patch may hold could never be recorded.
const MaxWriteBytes = domain.MaxWorkspacePatchBytes

// Entry kinds read_file and list_files report.
const (
	kindFile      = "file"
	kindDirectory = "directory"
	kindSymlink   = "symlink"
	kindMissing   = "missing"
)

// noSideEffect is the policy of all three Tools. read_file and list_files only read a
// throwaway checkout. write_file declares the same policy on purpose: its executor computes
// the new cumulative patch in a temporary directory that is removed before it returns, and
// the workspace itself changes only when the Runtime records that patch in the result
// transaction. A write_file call whose process stops mid-way therefore left nothing
// behind and is simply safe to repeat, which is what lets an orphaned Attempt be handed
// back to the Agent as interrupted rather than uncertain.
var noSideEffect = domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe}

const readFileInputSchema = `{
  "type": "object",
  "properties": {"path": {"type": "string", "minLength": 1, "maxLength": 1024}},
  "required": ["path"],
  "additionalProperties": false
}`

const readFileOutputSchema = `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "minLength": 1},
    "patchSeq": {"type": "integer", "minimum": 0},
    "kind": {"type": "string", "enum": ["file", "directory", "symlink", "missing"]},
    "content": {"type": "string"},
    "sizeBytes": {"type": "integer", "minimum": 0},
    "truncated": {"type": "boolean"}
  },
  "required": ["path", "patchSeq", "kind", "content", "sizeBytes", "truncated"],
  "additionalProperties": false
}`

const listFilesInputSchema = `{
  "type": "object",
  "properties": {"dir": {"type": "string", "maxLength": 1024}},
  "required": ["dir"],
  "additionalProperties": false
}`

const listFilesOutputSchema = `{
  "type": "object",
  "properties": {
    "dir": {"type": "string"},
    "patchSeq": {"type": "integer", "minimum": 0},
    "kind": {"type": "string", "enum": ["directory", "file", "symlink", "missing"]},
    "entries": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "name": {"type": "string", "minLength": 1},
          "kind": {"type": "string", "enum": ["file", "directory", "symlink"]},
          "sizeBytes": {"type": "integer", "minimum": 0}
        },
        "required": ["name", "kind", "sizeBytes"],
        "additionalProperties": false
      }
    },
    "truncated": {"type": "boolean"}
  },
  "required": ["dir", "patchSeq", "kind", "entries", "truncated"],
  "additionalProperties": false
}`

const writeFileInputSchema = `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "minLength": 1, "maxLength": 1024},
    "content": {"type": "string", "maxLength": 262144}
  },
  "required": ["path", "content"],
  "additionalProperties": false
}`

const writeFileOutputSchema = `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "minLength": 1},
    "bytesWritten": {"type": "integer", "minimum": 0},
    "patchDigest": {"type": "string", "minLength": 64, "maxLength": 64},
    "touchedPaths": {"type": "array", "items": {"type": "string", "minLength": 1}}
  },
  "required": ["path", "bytesWritten", "patchDigest", "touchedPaths"],
  "additionalProperties": false
}`

// Registrations returns the three workspace Tools over repos, in a fixed order.
func Registrations(repos Repositories) []registry.ToolRegistration {
	return []registry.ToolRegistration{
		ReadFileRegistration(repos),
		ListFilesRegistration(repos),
		WriteFileRegistration(repos),
	}
}

// ReadFileRegistration returns the `read_file` Tool: the content of one file of the
// workspace as it is after the current patch, bounded to MaxReadBytes. Reading is not
// limited to the allowed paths; the result names the path and the workspace version it
// was read at.
func ReadFileRegistration(repos Repositories) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          ReadFileToolName,
			Description:   "Read one file of the workspace as it is after the current patch; long files are truncated",
			InputSchema:   json.RawMessage(readFileInputSchema),
			OutputSchema:  json.RawMessage(readFileOutputSchema),
			SideEffect:    noSideEffect,
			ExecutionKind: domain.ToolExecutionSync,
			Requires:      []domain.FactRequirement{},
			Records:       []domain.ToolRecordKind{},
			UsesWorkspace: true,
		},
		Executor: &readFileExecutor{repos: repos},
	}
}

// ListFilesRegistration returns the `list_files` Tool: the bounded directory listing of
// one workspace directory after the current patch.
func ListFilesRegistration(repos Repositories) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          ListFilesToolName,
			Description:   "List the entries of one workspace directory as it is after the current patch",
			InputSchema:   json.RawMessage(listFilesInputSchema),
			OutputSchema:  json.RawMessage(listFilesOutputSchema),
			SideEffect:    noSideEffect,
			ExecutionKind: domain.ToolExecutionSync,
			Requires:      []domain.FactRequirement{},
			Records:       []domain.ToolRecordKind{},
			UsesWorkspace: true,
		},
		Executor: &listFilesExecutor{repos: repos},
	}
}

// WriteFileRegistration returns the `write_file` Tool. It records a WORKSPACE_PATCH and
// declares the written path at /path, so the Runtime refuses a path outside the Agent
// Run's allowed paths before the executor is called. See noSideEffect for why a Tool that
// changes the workspace is registered with no side effect.
func WriteFileRegistration(repos Repositories) registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:               WriteFileToolName,
			Description:        "Write one file of the workspace inside the allowed paths; the result is the new cumulative patch",
			InputSchema:        json.RawMessage(writeFileInputSchema),
			OutputSchema:       json.RawMessage(writeFileOutputSchema),
			SideEffect:         noSideEffect,
			ExecutionKind:      domain.ToolExecutionSync,
			Requires:           []domain.FactRequirement{},
			Records:            []domain.ToolRecordKind{domain.ToolRecordWorkspacePatch},
			UsesWorkspace:      true,
			WorkspaceWritePath: "/path",
		},
		Executor: &writeFileExecutor{repos: repos},
	}
}

type readFileExecutor struct{ repos Repositories }

type readFileArguments struct {
	Path string `json:"path"`
}

type readFileResult struct {
	Path      string `json:"path"`
	PatchSeq  int    `json:"patchSeq"`
	Kind      string `json:"kind"`
	Content   string `json:"content"`
	SizeBytes int64  `json:"sizeBytes"`
	Truncated bool   `json:"truncated"`
}

// Execute rebuilds the workspace and reads one file. A missing path or a directory is
// reported by kind rather than as an error: asking for a file that does not exist is an
// ordinary answer the Agent reads, not a failed call.
func (e *readFileExecutor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	var args readFileArguments
	if err := json.Unmarshal(action.Arguments, &args); err != nil || args.Path == "" {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: arguments carry no path", ReadFileToolName)
	}
	c, err := rebuild(ctx, e.repos, action.Workspace)
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: %w", ReadFileToolName, err)
	}
	defer c.close()

	normalised, absolute, err := c.resolve(args.Path)
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: %w", ReadFileToolName, err)
	}
	result := readFileResult{Path: normalised, PatchSeq: action.Workspace.PatchSeq, Kind: kindMissing}
	info, err := os.Lstat(absolute)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: stat %q: %w", ReadFileToolName, normalised, err)
	case info.IsDir():
		result.Kind = kindDirectory
	case info.Mode()&os.ModeSymlink != 0:
		result.Kind = kindSymlink
	default:
		result.Kind = kindFile
		result.SizeBytes = info.Size()
		content, err := os.ReadFile(absolute)
		if err != nil {
			return registry.ToolExecutionResult{}, fmt.Errorf("%s: read %q: %w", ReadFileToolName, normalised, err)
		}
		if len(content) > MaxReadBytes {
			content, result.Truncated = content[:MaxReadBytes], true
		}
		result.Content = string(content)
	}
	return completed(result)
}

type listFilesExecutor struct{ repos Repositories }

type listFilesArguments struct {
	Dir string `json:"dir"`
}

type listEntry struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"sizeBytes"`
}

type listFilesResult struct {
	Dir       string      `json:"dir"`
	PatchSeq  int         `json:"patchSeq"`
	Kind      string      `json:"kind"`
	Entries   []listEntry `json:"entries"`
	Truncated bool        `json:"truncated"`
}

// Execute rebuilds the workspace and lists one directory, sorted by name and bounded to
// MaxListEntries. The checkout's own `.git` directory is never listed.
func (e *listFilesExecutor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	var args listFilesArguments
	if err := json.Unmarshal(action.Arguments, &args); err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: arguments are not an object", ListFilesToolName)
	}
	c, err := rebuild(ctx, e.repos, action.Workspace)
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: %w", ListFilesToolName, err)
	}
	defer c.close()

	normalised, absolute, err := c.resolve(args.Dir)
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: %w", ListFilesToolName, err)
	}
	result := listFilesResult{Dir: normalised, PatchSeq: action.Workspace.PatchSeq, Kind: kindMissing, Entries: []listEntry{}}
	info, err := os.Lstat(absolute)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return completed(result)
	case err != nil:
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: stat %q: %w", ListFilesToolName, normalised, err)
	case info.Mode()&os.ModeSymlink != 0:
		result.Kind = kindSymlink
		return completed(result)
	case !info.IsDir():
		result.Kind = kindFile
		return completed(result)
	}
	result.Kind = kindDirectory
	entries, err := os.ReadDir(absolute)
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: read directory %q: %w", ListFilesToolName, normalised, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if normalised == "." && entry.Name() == ".git" {
			continue
		}
		if len(result.Entries) == MaxListEntries {
			result.Truncated = true
			break
		}
		item := listEntry{Name: entry.Name(), Kind: kindFile}
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			item.Kind = kindSymlink
		case entry.IsDir():
			item.Kind = kindDirectory
		default:
			if info, err := entry.Info(); err == nil {
				item.SizeBytes = info.Size()
			}
		}
		result.Entries = append(result.Entries, item)
	}
	return completed(result)
}

type writeFileExecutor struct{ repos Repositories }

type writeFileArguments struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type writeFileResult struct {
	Path         string   `json:"path"`
	BytesWritten int      `json:"bytesWritten"`
	PatchDigest  string   `json:"patchDigest"`
	TouchedPaths []string `json:"touchedPaths"`
}

// Execute rebuilds the workspace, writes the file and returns the new cumulative patch
// with its digest and touched paths. The checkout is removed before it returns: the
// executor changes no workspace fact itself. A path that resolves outside the checkout,
// through a symbolic link the repository carries, is refused and nothing is written.
func (e *writeFileExecutor) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	var args writeFileArguments
	if err := json.Unmarshal(action.Arguments, &args); err != nil || args.Path == "" {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: arguments carry no path", WriteFileToolName)
	}
	if len(args.Content) > MaxWriteBytes {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: content is %d bytes, over the %d-byte bound", WriteFileToolName, len(args.Content), MaxWriteBytes)
	}
	c, err := rebuild(ctx, e.repos, action.Workspace)
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: %w", WriteFileToolName, err)
	}
	defer c.close()

	normalised, absolute, err := c.resolve(args.Path)
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: %w", WriteFileToolName, err)
	}
	if info, err := os.Lstat(absolute); err == nil && !info.Mode().IsRegular() {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: %q is not a regular file", WriteFileToolName, normalised)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), checkoutDirMode); err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: create directory for %q: %w", WriteFileToolName, normalised, err)
	}
	if err := os.WriteFile(absolute, []byte(args.Content), checkoutFileMode); err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: write %q: %w", WriteFileToolName, normalised, err)
	}
	patch, err := c.cumulativePatch(ctx)
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: %w", WriteFileToolName, err)
	}
	if len(patch) > domain.MaxWorkspacePatchBytes {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: the cumulative patch is %d bytes, over the %d-byte bound", WriteFileToolName, len(patch), domain.MaxWorkspacePatchBytes)
	}
	record := registry.WorkspacePatch{
		Patch:        patch,
		PatchDigest:  domain.WorkspacePatchDigest(patch),
		TouchedPaths: domain.WorkspacePatchTouchedPaths(patch),
	}
	output, err := json.Marshal(writeFileResult{
		Path: normalised, BytesWritten: len(args.Content), PatchDigest: record.PatchDigest, TouchedPaths: record.TouchedPaths,
	})
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: encode result: %w", WriteFileToolName, err)
	}
	return registry.ToolExecutionResult{
		Kind:   registry.ToolResultCompleted,
		Result: &registry.ToolResult{Output: output, WorkspacePatch: &record},
	}, nil
}

// completed encodes one read result as a COMPLETED Tool result.
func completed(result any) (registry.ToolExecutionResult, error) {
	output, err := json.Marshal(result)
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("workspace: encode result: %w", err)
	}
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{Output: output}}, nil
}
