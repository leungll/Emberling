package workspace

import (
	"context"
	"strings"
	"testing"
)

// The context reader serves a review Tool the repository's own text beside the patch: the
// named files and the regular files of the named directories, each bounded, read from a
// rebuilt workspace. Missing entries and symbolic links contribute nothing, and the read
// never leaves the checkout.

func TestContextReader_ReadFiles_ReadsNamedFilesAndDirectoryEntriesInOrder(t *testing.T) {
	repo := newFixtureRepo(t)
	reader := NewContextReader(repo.repos)
	reader.MaxFileBytes = 64

	files, err := reader.ReadFiles(context.Background(), repo.view("", 0), []string{"README.md", "missing.md", "internal/auth", "internal"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var paths []string
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	// internal/auth holds token.go and ignored.txt as regular files; testdata is a
	// directory and escape a symbolic link, neither is read. internal holds only a
	// directory and a symbolic link, so it contributes nothing.
	if want := "README.md,internal/auth/ignored.txt,internal/auth/token.go"; strings.Join(paths, ",") != want {
		t.Fatalf("paths = %v, want %s", paths, want)
	}
	if files[0].Content != "# fixture\n" || files[0].Truncated {
		t.Errorf("README = %+v, want its content untruncated", files[0])
	}
	if files[2].Content != fixtureTokenSource || files[2].Truncated {
		t.Errorf("token.go = %+v, want the base commit's source", files[2])
	}
}

func TestContextReader_ReadFiles_SeesThePatchAndBoundsEachFile(t *testing.T) {
	repo := newFixtureRepo(t)
	patch := repo.patchWriting(t, "internal/auth/token.go", strings.Repeat("y", 100)+"\n")
	reader := NewContextReader(repo.repos)
	reader.MaxFileBytes = 40

	files, err := reader.ReadFiles(context.Background(), repo.view(patch, 1), []string{"internal/auth/token.go"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(files) != 1 || !files[0].Truncated || files[0].Content != strings.Repeat("y", 40) {
		t.Fatalf("files = %+v, want the patched content cut to 40 bytes and flagged", files)
	}
}

func TestContextReader_ReadFiles_StopsAtMaxFilesAndRefusesEscapes(t *testing.T) {
	repo := newFixtureRepo(t)
	reader := NewContextReader(repo.repos)
	reader.MaxFiles = 1

	files, err := reader.ReadFiles(context.Background(), repo.view("", 0), []string{"internal/auth", "README.md"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(files) != 1 || files[0].Path != "internal/auth/ignored.txt" {
		t.Fatalf("files = %+v, want only the first entry", files)
	}
	if _, err := reader.ReadFiles(context.Background(), repo.view("", 0), []string{"../outside"}); err == nil {
		t.Fatal("a path outside the checkout was read, want a refusal")
	}
	if _, err := reader.ReadFiles(context.Background(), nil, []string{"README.md"}); err == nil {
		t.Fatal("a nil view was read, want an error")
	}
}

// patchWriting returns the cumulative patch the write Tool produces for writing content to
// rel on the base commit.
func (f *fixtureRepo) patchWriting(t *testing.T, rel, content string) string {
	t.Helper()
	result, err := call(t, WriteFileRegistration(f.repos), f.view("", 0), mustJSON(map[string]string{"path": rel, "content": content}))
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkspacePatch == nil {
		t.Fatal("write_file returned no workspace patch")
	}
	return result.WorkspacePatch.Patch
}
