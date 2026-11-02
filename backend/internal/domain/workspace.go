package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// AgentWorkspace is the repository binding an Agent Run was frozen to when it started:
// which registered repository, at which commit, which relative path prefixes the Agent may
// change and, optionally, where a release Tool deploys. It is written with the Agent Run
// and never changes; the workspace content is this base commit plus the WorkspacePatch
// sequence, and no checkout is ever persisted.
type AgentWorkspace struct {
	AgentRunID   string `json:"agentRunId"`
	RepositoryID string `json:"repositoryId"`
	BaseCommit   string `json:"baseCommit"`
	// AllowedPaths are normalised relative path prefixes inside the repository: no leading
	// slash, no "." or ".." segment, no trailing slash. A write outside every prefix is
	// refused before any Tool runs.
	AllowedPaths []string               `json:"allowedPaths"`
	DeployTarget *WorkspaceDeployTarget `json:"deployTarget"`
	CreatedAt    time.Time              `json:"createdAt"`
}

// WorkspaceDeployTarget names the service and environment a release Tool deploys to.
type WorkspaceDeployTarget struct {
	Service     string `json:"service"`
	Environment string `json:"environment"`
}

// WorkspacePatch is one recorded write to an Agent Run's workspace: the whole cumulative
// unified diff relative to the frozen base commit at that point, its digest and the
// repository paths it touches. Seq orders the writes of one Agent Run from 1; the newest
// Seq is the current workspace. Each patch names the Tool Attempt whose result carried it.
type WorkspacePatch struct {
	ID         string `json:"id"`
	AgentRunID string `json:"agentRunId"`
	Seq        int    `json:"seq"`
	// Patch is the cumulative diff text. A summary read for projection leaves it empty;
	// the digest identifies the patch wherever the text is not carried.
	Patch         string    `json:"patch"`
	PatchDigest   string    `json:"patchDigest"`
	TouchedPaths  []string  `json:"touchedPaths"`
	ToolAttemptID string    `json:"toolAttemptId"`
	CreatedAt     time.Time `json:"createdAt"`
}

// MaxWorkspacePatchBytes bounds the cumulative patch text the Runtime records. The
// database enforces the same bound, so a larger patch is refused in the result transaction
// and never stored in part.
const MaxWorkspacePatchBytes = 256 << 10

// WorkspacePatchDigest identifies one cumulative patch: the SHA-256, in lowercase hex, of
// the patch text after every CRLF is folded to LF. Nothing else is changed: trailing
// whitespace is kept and no newline is appended, so the same bytes always give the same
// digest whichever tool produced them. The empty patch, a workspace nobody has changed,
// digests the empty byte string. Every producer and verifier of a patch digest, the write
// Tool, the verification runner and the change request, uses this one function.
func WorkspacePatchDigest(patch string) string {
	normalised := strings.ReplaceAll(patch, "\r\n", "\n")
	sum := sha256.Sum256([]byte(normalised))
	return hex.EncodeToString(sum[:])
}

// WorkspacePatchTouchedPaths lists the repository paths one unified diff touches: every
// path named by a `--- a/<path>` or `+++ b/<path>` header, deduplicated and in
// lexicographic order. `/dev/null`, the side of a created or deleted file that does not
// exist, names no path. A patch with no file header touches nothing.
func WorkspacePatchTouchedPaths(patch string) []string {
	seen := make(map[string]struct{})
	// Headers are read only between a `diff --git` line and that file's first hunk, so an
	// added or removed content line that happens to start with `++ b/` or `-- a/` is never
	// mistaken for a file header. A patch without `diff --git` lines is read as one header.
	inHeader := true
	for _, line := range strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHeader = true
			continue
		case strings.HasPrefix(line, "@@"):
			inHeader = false
			continue
		case !inHeader:
			continue
		}
		var rest string
		switch {
		case strings.HasPrefix(line, "--- a/"):
			rest = strings.TrimPrefix(line, "--- a/")
		case strings.HasPrefix(line, "+++ b/"):
			rest = strings.TrimPrefix(line, "+++ b/")
		default:
			continue
		}
		// A header may carry a tab-separated timestamp after the path; git never writes
		// one, but the parser tolerates it.
		if tab := strings.IndexByte(rest, '\t'); tab >= 0 {
			rest = rest[:tab]
		}
		if rest == "" {
			continue
		}
		seen[rest] = struct{}{}
	}
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}
