package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"
)

func TestWorkspacePatchDigest_EmptyPatch_IsSHA256OfEmptyBytes(t *testing.T) {
	sum := sha256.Sum256(nil)
	if got, want := WorkspacePatchDigest(""), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("digest of the empty patch = %s, want %s", got, want)
	}
}

func TestWorkspacePatchDigest_CRLF_NormalisesToLFOnly(t *testing.T) {
	lf := "diff --git a/go.mod b/go.mod\n--- a/go.mod\n+++ b/go.mod\n@@ -1 +1 @@\n-a\n+b\n"
	crlf := "diff --git a/go.mod b/go.mod\r\n--- a/go.mod\r\n+++ b/go.mod\r\n@@ -1 +1 @@\r\n-a\r\n+b\r\n"
	if WorkspacePatchDigest(lf) != WorkspacePatchDigest(crlf) {
		t.Errorf("CRLF and LF spellings of the same patch digest differently")
	}
	// Only CRLF folds: a lone CR, trailing whitespace and a missing final newline are
	// different bytes and therefore different patches.
	for name, other := range map[string]string{
		"lone CR":             "diff --git a/go.mod b/go.mod\r--- a/go.mod\n",
		"trailing space":      lf + " ",
		"no final newline":    lf[:len(lf)-1],
		"different file name": "diff --git a/go.sum b/go.sum\n--- a/go.sum\n+++ b/go.sum\n@@ -1 +1 @@\n-a\n+b\n",
	} {
		if WorkspacePatchDigest(other) == WorkspacePatchDigest(lf) {
			t.Errorf("%s: a different patch has the same digest", name)
		}
	}
	if got := WorkspacePatchDigest(lf); len(got) != 64 || got != hexLower(got) {
		t.Errorf("digest %q is not 64 lowercase hexadecimal digits", got)
	}
}

func hexLower(s string) string {
	out := []byte(s)
	for i, c := range out {
		if c >= 'A' && c <= 'F' {
			out[i] = c + ('a' - 'A')
		}
	}
	return string(out)
}

func TestWorkspacePatchTouchedPaths_DeduplicatesAndSorts(t *testing.T) {
	patch := "diff --git a/internal/auth/token.go b/internal/auth/token.go\n" +
		"--- a/internal/auth/token.go\n+++ b/internal/auth/token.go\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/go.mod b/go.mod\n--- a/go.mod\n+++ b/go.mod\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/docs/new.md b/docs/new.md\nnew file mode 100644\n--- /dev/null\n+++ b/docs/new.md\n@@ -0,0 +1 @@\n+hello\n" +
		"diff --git a/old.txt b/old.txt\ndeleted file mode 100644\n--- a/old.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-bye\n"
	want := []string{"docs/new.md", "go.mod", "internal/auth/token.go", "old.txt"}
	if got := WorkspacePatchTouchedPaths(patch); !reflect.DeepEqual(got, want) {
		t.Errorf("touched paths = %v, want %v", got, want)
	}
	if got := WorkspacePatchTouchedPaths(""); len(got) != 0 {
		t.Errorf("touched paths of the empty patch = %v, want none", got)
	}
	crlf := "--- a/x.go\r\n+++ b/x.go\r\n"
	if got := WorkspacePatchTouchedPaths(crlf); !reflect.DeepEqual(got, []string{"x.go"}) {
		t.Errorf("touched paths of a CRLF patch = %v, want [x.go]", got)
	}
	// A content line inside a hunk that starts like a header names no path.
	hunkLine := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1,2 @@\n a\n+++ b/injected.go\n"
	if got := WorkspacePatchTouchedPaths(hunkLine); !reflect.DeepEqual(got, []string{"x.go"}) {
		t.Errorf("touched paths with a header-like hunk line = %v, want [x.go]", got)
	}
}
