package sandboxrunner

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"maps"
	"slices"
)

// MaxTestOutputBytes is the bound on a test's combined output, exported so the sandbox
// supervisor, which keeps its own copy of the limit, can be checked against it.
const MaxTestOutputBytes = maxTestOutputBytes

// WorkspaceDigest returns "sha256:" and the lowercase hex SHA-256 of a file tree keyed by
// slash-separated relative path. It covers regular files' paths and contents only, never
// directories, modes or times. Paths are taken in ascending byte order, and each file
// contributes its path length, path, content length and content, both lengths as 8-byte
// big-endian unsigned integers, so no two different trees produce the same byte stream.
// The empty tree digests the empty stream.
//
// The sandbox supervisor reports this digest for the tree it actually wrote; it keeps its
// own copy of the function, so the rule must not change on one side only.
func WorkspaceDigest(files map[string][]byte) string {
	hash := sha256.New()
	var length [8]byte
	for _, name := range slices.Sorted(maps.Keys(files)) {
		content := files[name]
		binary.BigEndian.PutUint64(length[:], uint64(len(name)))
		hash.Write(length[:])
		hash.Write([]byte(name))
		binary.BigEndian.PutUint64(length[:], uint64(len(content)))
		hash.Write(length[:])
		hash.Write(content)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
