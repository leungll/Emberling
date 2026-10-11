package sandboxrunner

import "testing"

// pinnedFixedTreeDigest is the digest of fixedDigestTree, computed once by an independent
// implementation of the length-prefixed rule. The sandbox supervisor pins the same tree to
// the same value, so the two copies of the digest cannot drift apart unnoticed.
const pinnedFixedTreeDigest = "sha256:363b4390e56b928d1ee27994b533faeb100acb1cbf37b036e455fdb8619ebf40"

func fixedDigestTree() map[string][]byte {
	return map[string][]byte{
		"go.mod":            []byte("module example.com/fixture\n\ngo 1.27\n"),
		"main.go":           []byte("package main\n"),
		"pkg/empty.txt":     []byte(""),
		"pkg/sub/data.json": []byte("{\"k\":\"v\"}\n"),
	}
}

func TestWorkspaceDigest_FixedTree_MatchesPinnedValue(t *testing.T) {
	if got := WorkspaceDigest(fixedDigestTree()); got != pinnedFixedTreeDigest {
		t.Fatalf("WorkspaceDigest(fixed tree) = %s, want %s", got, pinnedFixedTreeDigest)
	}
	const emptyTree = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := WorkspaceDigest(map[string][]byte{}); got != emptyTree {
		t.Fatalf("WorkspaceDigest(empty tree) = %s, want the digest of no bytes %s", got, emptyTree)
	}
}

func TestWorkspaceDigest_LengthPrefixSeparatesPathFromContent(t *testing.T) {
	// Without length prefixes each pair would hash the same byte stream "abc".
	pairs := [][2]map[string][]byte{
		{{"ab": []byte("c")}, {"a": []byte("bc")}},
		{{"a": []byte(""), "b": []byte("c")}, {"a": []byte("b"), "c": []byte("")}},
		{{"x": []byte("12")}, {"x1": []byte("2")}},
	}
	for _, pair := range pairs {
		if left, right := WorkspaceDigest(pair[0]), WorkspaceDigest(pair[1]); left == right {
			t.Fatalf("WorkspaceDigest(%q) = WorkspaceDigest(%q) = %s, want different digests", pair[0], pair[1], left)
		}
	}
	first, second := WorkspaceDigest(fixedDigestTree()), WorkspaceDigest(fixedDigestTree())
	if first != second {
		t.Fatalf("WorkspaceDigest of the same tree = %s and %s, want one value", first, second)
	}
}
