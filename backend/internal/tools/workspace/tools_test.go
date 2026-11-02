package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// fixtureRepo is a git repository created for one test: a Go module with an allowed
// internal package, a large file, a symbolic link that points inside the tree and one
// that points outside it.
type fixtureRepo struct {
	path   string
	commit string
	repos  *LocalGit
}

const (
	fixtureRepositoryID = "fixture"
	fixtureTokenSource  = "package auth\n\nconst Version = 3\n"
	fixtureLargeBytes   = MaxReadBytes + 4096
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitCommand(context.Background(), dir, args...)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, stderr.String())
	}
	return strings.TrimSpace(out.String())
}

func newFixtureRepo(t *testing.T) *fixtureRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is required: %v", err)
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	write := func(rel, content string) {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/fixture\n\ngo 1.22\n")
	write("README.md", "# fixture\n")
	write("internal/auth/token.go", fixtureTokenSource)
	write("internal/auth/testdata/large.txt", strings.Repeat("x", fixtureLargeBytes))
	write(".gitignore", "ignored.txt\n")
	write("internal/auth/ignored.txt", "ignored but committed\n")
	if err := os.Symlink("../README.md", filepath.Join(dir, "internal", "readme-link.md")); err != nil {
		t.Fatal(err)
	}
	// escape points at the parent of the checkout root wherever the tree is rebuilt.
	if err := os.Symlink("../../..", filepath.Join(dir, "internal", "auth", "escape")); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A", "-f")
	runGit(t, dir, "commit", "-q", "-m", "base")
	commit := runGit(t, dir, "rev-parse", "HEAD")
	return &fixtureRepo{path: dir, commit: commit, repos: NewLocalGit(map[string]string{fixtureRepositoryID: dir})}
}

func (f *fixtureRepo) view(patch string, seq int) *registry.WorkspaceView {
	return &registry.WorkspaceView{
		RepositoryID: fixtureRepositoryID, BaseCommit: f.commit,
		Patch: patch, PatchDigest: domain.WorkspacePatchDigest(patch), PatchSeq: seq,
		AllowedPaths: []string{"go.mod", "internal/auth"},
	}
}

func call(t *testing.T, reg registry.ToolRegistration, view *registry.WorkspaceView, args string) (registry.ToolResult, error) {
	t.Helper()
	result, err := reg.Executor.Execute(context.Background(), registry.ToolAction{
		ToolName: reg.Metadata.Name, Arguments: json.RawMessage(args), Workspace: view,
	})
	if err != nil {
		return registry.ToolResult{}, err
	}
	if result.Kind != registry.ToolResultCompleted || result.Result == nil {
		t.Fatalf("%s returned %+v, want a COMPLETED result", reg.Metadata.Name, result)
	}
	return *result.Result, nil
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return out
}

func TestReadFile_BaseCommit_ReturnsContentWithPathAndPatchSeqZero(t *testing.T) {
	f := newFixtureRepo(t)
	result, err := call(t, ReadFileRegistration(f.repos), f.view("", 0), `{"path":"internal/auth/token.go"}`)
	if err != nil {
		t.Fatal(err)
	}
	got := decode[readFileResult](t, result.Output)
	if got.Path != "internal/auth/token.go" || got.PatchSeq != 0 || got.Kind != kindFile || got.Content != fixtureTokenSource || got.Truncated {
		t.Errorf("read_file = %+v, want the base content at patchSeq 0", got)
	}
	// A read is not limited to the allowed paths, and an ignored-but-committed file is
	// part of the workspace.
	for _, p := range []string{"README.md", "internal/auth/ignored.txt"} {
		result, err := call(t, ReadFileRegistration(f.repos), f.view("", 0), `{"path":"`+p+`"}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := decode[readFileResult](t, result.Output); got.Kind != kindFile || got.Content == "" {
			t.Errorf("read_file(%s) = %+v, want the file content", p, got)
		}
	}
	missing, err := call(t, ReadFileRegistration(f.repos), f.view("", 0), `{"path":"internal/auth/nope.go"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := decode[readFileResult](t, missing.Output); got.Kind != kindMissing || got.Content != "" {
		t.Errorf("read_file(missing) = %+v, want kind missing", got)
	}
}

func TestReadFile_LargeFile_TruncatedAndFlagged(t *testing.T) {
	f := newFixtureRepo(t)
	result, err := call(t, ReadFileRegistration(f.repos), f.view("", 0), `{"path":"internal/auth/testdata/large.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	got := decode[readFileResult](t, result.Output)
	if !got.Truncated || len(got.Content) != MaxReadBytes || got.SizeBytes != fixtureLargeBytes {
		t.Errorf("read_file(large) = truncated %v, %d content bytes, size %d; want truncated at %d of %d",
			got.Truncated, len(got.Content), got.SizeBytes, MaxReadBytes, fixtureLargeBytes)
	}
}

func TestReadFile_PathOutsideCheckout_Rejected(t *testing.T) {
	f := newFixtureRepo(t)
	for _, p := range []string{"/etc/hosts", "../outside", "internal/../../x", "internal/auth/escape/etc/hosts", ".git/config"} {
		if _, err := call(t, ReadFileRegistration(f.repos), f.view("", 0), `{"path":"`+p+`"}`); !errors.Is(err, errPathOutsideCheckout) {
			t.Errorf("read_file(%q) = %v, want %v", p, err, errPathOutsideCheckout)
		}
	}
	// A link that resolves inside the tree is reported as a link, not followed as a file.
	result, err := call(t, ReadFileRegistration(f.repos), f.view("", 0), `{"path":"internal/readme-link.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := decode[readFileResult](t, result.Output); got.Kind != kindSymlink {
		t.Errorf("read_file(link inside) = %+v, want kind symlink", got)
	}
}

func TestListFiles_Directory_ListsSortedEntriesWithoutGitDir(t *testing.T) {
	f := newFixtureRepo(t)
	result, err := call(t, ListFilesRegistration(f.repos), f.view("", 0), `{"dir":""}`)
	if err != nil {
		t.Fatal(err)
	}
	got := decode[listFilesResult](t, result.Output)
	var names []string
	for _, e := range got.Entries {
		names = append(names, e.Name+":"+e.Kind)
	}
	want := []string{".gitignore:file", "README.md:file", "go.mod:file", "internal:directory"}
	if got.Dir != "." || got.Kind != kindDirectory || strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("list_files(root) = %+v, want entries %v", got, want)
	}
	nested, err := call(t, ListFilesRegistration(f.repos), f.view("", 0), `{"dir":"internal"}`)
	if err != nil {
		t.Fatal(err)
	}
	names = nil
	for _, e := range decode[listFilesResult](t, nested.Output).Entries {
		names = append(names, e.Name+":"+e.Kind)
	}
	if want := "auth:directory,readme-link.md:symlink"; strings.Join(names, ",") != want {
		t.Errorf("list_files(internal) = %v, want %s", names, want)
	}
	missing, err := call(t, ListFilesRegistration(f.repos), f.view("", 0), `{"dir":"nope"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := decode[listFilesResult](t, missing.Output); got.Kind != kindMissing || len(got.Entries) != 0 {
		t.Errorf("list_files(missing) = %+v, want kind missing", got)
	}
}

func TestWriteFile_SymlinkEscape_RejectedWithoutPatch(t *testing.T) {
	f := newFixtureRepo(t)
	outside := filepath.Join(filepath.Dir(f.path), "escaped-"+filepath.Base(f.path))
	_, err := call(t, WriteFileRegistration(f.repos), f.view("", 0), `{"path":"internal/auth/escape/`+filepath.Base(outside)+`","content":"pwned"}`)
	if !errors.Is(err, errPathOutsideCheckout) {
		t.Fatalf("write_file through an escaping link = %v, want %v", err, errPathOutsideCheckout)
	}
	// The path lies under an allowed prefix as written, so only the link resolution could
	// have refused it; nothing may have been written anywhere.
	if _, statErr := os.Stat(outside); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("a file was written outside the checkout: %v", statErr)
	}
	for _, p := range []string{"/abs.go", "internal/auth/../../x.go"} {
		if _, err := call(t, WriteFileRegistration(f.repos), f.view("", 0), `{"path":"`+p+`","content":"x"}`); !errors.Is(err, errPathOutsideCheckout) {
			t.Errorf("write_file(%q) = %v, want %v", p, err, errPathOutsideCheckout)
		}
	}
}

func TestWriteFile_ThenReadFile_ReturnsNewContentAtNextPatchSeq(t *testing.T) {
	f := newFixtureRepo(t)
	write := WriteFileRegistration(f.repos)
	read := ReadFileRegistration(f.repos)

	first, err := call(t, write, f.view("", 0), `{"path":"internal/auth/token.go","content":"package auth\n\nconst Version = 4\n"}`)
	if err != nil {
		t.Fatal(err)
	}
	if first.WorkspacePatch == nil {
		t.Fatalf("write_file returned no workspace patch")
	}
	patch1 := *first.WorkspacePatch
	if patch1.PatchDigest != domain.WorkspacePatchDigest(patch1.Patch) {
		t.Errorf("patch digest %s does not match the patch text", patch1.PatchDigest)
	}
	if got := strings.Join(patch1.TouchedPaths, ","); got != "internal/auth/token.go" {
		t.Errorf("touched paths = %s, want internal/auth/token.go", got)
	}
	out1 := decode[writeFileResult](t, first.Output)
	if out1.Path != "internal/auth/token.go" || out1.PatchDigest != patch1.PatchDigest || out1.BytesWritten != len("package auth\n\nconst Version = 4\n") {
		t.Errorf("write_file output = %+v", out1)
	}

	// The second write rebuilds from the first patch and adds a new file: the cumulative
	// patch covers both.
	second, err := call(t, write, f.view(patch1.Patch, 1), `{"path":"internal/auth/claims.go","content":"package auth\n"}`)
	if err != nil {
		t.Fatal(err)
	}
	patch2 := *second.WorkspacePatch
	if got := strings.Join(patch2.TouchedPaths, ","); got != "internal/auth/claims.go,internal/auth/token.go" {
		t.Errorf("cumulative touched paths = %s", got)
	}

	// Reading at the second patch sees both changes and reports the version read.
	for p, want := range map[string]string{
		"internal/auth/token.go":  "package auth\n\nconst Version = 4\n",
		"internal/auth/claims.go": "package auth\n",
	} {
		result, err := call(t, read, f.view(patch2.Patch, 2), `{"path":"`+p+`"}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := decode[readFileResult](t, result.Output); got.Content != want || got.PatchSeq != 2 || got.Kind != kindFile {
			t.Errorf("read_file(%s) after two writes = %+v, want %q at patchSeq 2", p, got, want)
		}
	}
	// The base repository itself is untouched.
	if content, _ := os.ReadFile(filepath.Join(f.path, "internal", "auth", "token.go")); string(content) != fixtureTokenSource {
		t.Errorf("the registered repository was modified")
	}
	if _, err := os.Stat(filepath.Join(f.path, "internal", "auth", "claims.go")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the registered repository gained a file")
	}
}

func TestWriteFile_UnchangedContent_YieldsEmptyPatch(t *testing.T) {
	f := newFixtureRepo(t)
	result, err := call(t, WriteFileRegistration(f.repos), f.view("", 0), `{"path":"internal/auth/token.go","content":`+mustJSON(fixtureTokenSource)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if p := result.WorkspacePatch; p == nil || p.Patch != "" || p.PatchDigest != domain.WorkspacePatchDigest("") || len(p.TouchedPaths) != 0 {
		t.Errorf("unchanged write patch = %+v, want the empty patch", result.WorkspacePatch)
	}
}

func TestLocalGit_UnknownRepositoryOrCommit_Fails(t *testing.T) {
	f := newFixtureRepo(t)
	view := f.view("", 0)
	view.RepositoryID = "other"
	if _, err := call(t, ReadFileRegistration(f.repos), view, `{"path":"go.mod"}`); !errors.Is(err, ErrRepositoryNotRegistered) {
		t.Errorf("unknown repository = %v, want %v", err, ErrRepositoryNotRegistered)
	}
	view = f.view("", 0)
	view.BaseCommit = strings.Repeat("0", 40)
	if _, err := call(t, ReadFileRegistration(f.repos), view, `{"path":"go.mod"}`); err == nil {
		t.Errorf("unknown commit was exported")
	}
}

func TestRegistrations_MetadataValidates(t *testing.T) {
	for _, reg := range Registrations(NewLocalGit(nil)) {
		if err := reg.Metadata.Validate(); err != nil {
			t.Errorf("%s: %v", reg.Metadata.Name, err)
		}
		if !reg.Metadata.UsesWorkspace || reg.Metadata.SideEffect != noSideEffect || reg.Metadata.ExecutionKind != domain.ToolExecutionSync {
			t.Errorf("%s metadata = %+v", reg.Metadata.Name, reg.Metadata)
		}
	}
	write := WriteFileRegistration(NewLocalGit(nil)).Metadata
	if !write.DeclaresRecord(domain.ToolRecordWorkspacePatch) || write.WorkspaceWritePath != "/path" {
		t.Errorf("write_file metadata = %+v, want the WORKSPACE_PATCH record and the /path pointer", write)
	}
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
