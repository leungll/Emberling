package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// This file owns the pure decision of whether a workspace write stays inside the Agent
// Run's frozen allowed paths. It reads only the path the Agent asked to write and the
// frozen prefixes; it never touches a filesystem, so a symbolic link that resolves outside
// the checkout is the Tool executor's check, not this one.

// WriteScopeReason says why a write was refused.
type WriteScopeReason string

const (
	// WriteScopeEmpty: no path was given.
	WriteScopeEmpty WriteScopeReason = "EMPTY_PATH"
	// WriteScopeAbsolute: the path starts at the filesystem root instead of the repository.
	WriteScopeAbsolute WriteScopeReason = "ABSOLUTE_PATH"
	// WriteScopeParentSegment: the path contains a "." or ".." segment, or a backslash.
	WriteScopeParentSegment WriteScopeReason = "PARENT_SEGMENT"
	// WriteScopeOutsideAllowed: the normalised path is under none of the allowed prefixes.
	WriteScopeOutsideAllowed WriteScopeReason = "OUTSIDE_ALLOWED_PATHS"
)

// WriteScopeError is the refusal of one workspace write. Path is the normalised path when
// one could be formed; the message never repeats the allowed prefixes' full list beyond
// their count, so a refusal stays bounded however many prefixes the Definition names.
type WriteScopeError struct {
	Reason WriteScopeReason
	Path   string
}

func (e *WriteScopeError) Error() string {
	switch e.Reason {
	case WriteScopeEmpty:
		return "write scope: the path is empty"
	case WriteScopeAbsolute:
		return fmt.Sprintf("write scope: %q is absolute; paths are relative to the repository root", e.Path)
	case WriteScopeParentSegment:
		return fmt.Sprintf("write scope: %q contains a \".\" or \"..\" segment or a backslash", e.Path)
	default:
		return fmt.Sprintf("write scope: %q is outside the allowed paths", e.Path)
	}
}

// CheckWriteScope decides whether writing filePath is inside one of allowedPaths, the
// Agent Run's frozen prefixes in their normalised form. It refuses an empty or absolute
// path, a path with a "." or ".." segment or a backslash, and a normalised path that is
// neither one of the prefixes nor under one of them. It returns the normalised path on
// success, so the caller records the same spelling a patch header will carry.
func CheckWriteScope(allowedPaths []string, filePath string) (string, error) {
	normalised, err := normaliseWorkspacePath(filePath)
	if err != nil {
		return "", err
	}
	for _, allowed := range allowedPaths {
		if normalised == allowed || strings.HasPrefix(normalised, allowed+"/") {
			return normalised, nil
		}
	}
	return "", &WriteScopeError{Reason: WriteScopeOutsideAllowed, Path: normalised}
}

// NormaliseAllowedPaths returns the canonical form of the allowed path prefixes a
// Definition declared, in the author's order, so the frozen workspace carries exactly the
// prefixes CheckWriteScope compares against. The same rules the Definition's agent config
// enforces apply; a prefix that fails them is a defect of a config that should not have
// compiled, reported rather than repaired.
func NormaliseAllowedPaths(allowedPaths []string) ([]string, error) {
	out := make([]string, 0, len(allowedPaths))
	for i, allowed := range allowedPaths {
		normalised, err := normaliseAllowedPath(allowed)
		if err != nil {
			return nil, fmt.Errorf("allowedPaths[%d]: %w", i, err)
		}
		out = append(out, normalised)
	}
	return out, nil
}

// normaliseWorkspacePath checks one path the Agent named and returns its canonical
// relative, slash-separated form.
func normaliseWorkspacePath(filePath string) (string, error) {
	switch {
	case filePath == "":
		return "", &WriteScopeError{Reason: WriteScopeEmpty}
	case strings.HasPrefix(filePath, "/"):
		return "", &WriteScopeError{Reason: WriteScopeAbsolute, Path: filePath}
	case strings.ContainsRune(filePath, '\\'):
		return "", &WriteScopeError{Reason: WriteScopeParentSegment, Path: filePath}
	}
	for _, segment := range strings.Split(filePath, "/") {
		if segment == "." || segment == ".." {
			return "", &WriteScopeError{Reason: WriteScopeParentSegment, Path: filePath}
		}
	}
	cleaned := path.Clean(filePath)
	if cleaned == "." || cleaned == "" {
		return "", &WriteScopeError{Reason: WriteScopeEmpty}
	}
	return cleaned, nil
}

// IsWriteScopeError reports whether err is a write scope refusal.
func IsWriteScopeError(err error) bool {
	var scope *WriteScopeError
	return errors.As(err, &scope)
}

// ResolveWorkspaceWritePath reads the repository path a Tool call writes from its
// arguments, at the JSON Pointer the Tool's registration declares. A pointer that does not
// resolve to a non-empty string is reported as an empty-path refusal: the arguments
// already passed the Tool's InputSchema, so a missing value is a defect of the declaration
// and refusing the write is the safe answer.
func ResolveWorkspaceWritePath(arguments json.RawMessage, pointer string) (string, error) {
	doc, err := decodeFactDocument(arguments)
	if err != nil {
		return "", &WriteScopeError{Reason: WriteScopeEmpty}
	}
	value, found := lookupJSONPointer(doc, domain.JSONPointerTokens(pointer))
	if !found {
		return "", &WriteScopeError{Reason: WriteScopeEmpty}
	}
	filePath, ok := value.(string)
	if !ok || filePath == "" {
		return "", &WriteScopeError{Reason: WriteScopeEmpty}
	}
	return filePath, nil
}
