package main

import (
	"testing"

	"github.com/leungll/Emberling/backend/internal/fixture/sandboxrunner"
)

// The pinned value was computed once from this fixed tree; the runner pins the same tree
// to the same value, so a change to either digest implementation breaks one of the two.
const fixedTreeDigest = "sha256:363b4390e56b928d1ee27994b533faeb100acb1cbf37b036e455fdb8619ebf40"

func fixedTree() map[string][]byte {
	return map[string][]byte{
		"go.mod":            []byte("module example.com/fixture\n\ngo 1.27\n"),
		"main.go":           []byte("package main\n"),
		"pkg/empty.txt":     []byte(""),
		"pkg/sub/data.json": []byte("{\"k\":\"v\"}\n"),
	}
}

func TestWorkspaceDigest_FixedTree_MatchesPinnedValue(t *testing.T) {
	if got := workspaceDigest(fixedTree()); got != fixedTreeDigest {
		t.Fatalf("workspaceDigest(fixed tree) = %s, want %s", got, fixedTreeDigest)
	}
}

func TestWorkspaceDigest_EmptyTree_IsDigestOfEmptyStream(t *testing.T) {
	const emptyStream = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := workspaceDigest(map[string][]byte{}); got != emptyStream {
		t.Fatalf("workspaceDigest(empty tree) = %s, want %s", got, emptyStream)
	}
}

// The supervisor and the runner each keep a standard-library-only digest implementation
// and their own output limit, because the supervisor binary must not link runner code.
// This test runs both digests over the same trees and compares the limits, so the two
// copies cannot drift apart without a failing test.
func TestWorkspaceDigest_MatchesRunnerDigestAndOutputLimit(t *testing.T) {
	if maxTestOutputBytes != sandboxrunner.MaxTestOutputBytes {
		t.Fatalf("supervisor output limit = %d, runner output limit = %d", maxTestOutputBytes, sandboxrunner.MaxTestOutputBytes)
	}

	trees := []struct {
		name  string
		files map[string][]byte
	}{
		{name: "empty tree", files: map[string][]byte{}},
		{name: "fixed pinned tree", files: fixedTree()},
		{name: "empty file and nested directories", files: map[string][]byte{
			"empty":           {},
			"x/y/z/empty.txt": {},
			"x/y/z/deep.go":   []byte("package z\n"),
			"x/top.txt":       []byte("top"),
		}},
		{name: "path prefix of another path", files: map[string][]byte{
			"a":   []byte("file a"),
			"a/b": []byte("file a/b"),
			"ab":  []byte("file ab"),
		}},
		{name: "concatenation split one way", files: map[string][]byte{"ab": []byte("cd")}},
		{name: "concatenation split another way", files: map[string][]byte{"a": []byte("bcd")}},
	}

	seen := make(map[string]string, len(trees))
	for _, tree := range trees {
		got := workspaceDigest(tree.files)
		want := sandboxrunner.WorkspaceDigest(tree.files)
		if got != want {
			t.Errorf("%s: supervisor digest = %s, runner digest = %s", tree.name, got, want)
		}
		if other, ok := seen[got]; ok {
			t.Errorf("%s and %s share digest %s", tree.name, other, got)
		}
		seen[got] = tree.name
	}
}
