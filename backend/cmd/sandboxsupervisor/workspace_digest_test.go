package main

import "testing"

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
