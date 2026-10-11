package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"maps"
	"slices"
)

// workspaceDigest is the supervisor's copy of the runner's file tree digest. It lets the
// runner check that the tree written in the sandbox is the patched tree it sent; it is not
// a trust anchor, since it is computed inside the sandbox.
//
// The rule is shared with the runner and must not drift from it: the files' relative,
// slash-separated paths are taken in ascending byte order, and for each file the stream
// receives the path length as an 8-byte big-endian unsigned integer, the path bytes, the
// content length the same way, and the content bytes. The digest is "sha256:" followed by
// the lowercase hex SHA-256 of that stream; an empty tree hashes the empty stream. Only
// regular files count: directories, modes and times do not.
//
// The supervisor keeps its own standard-library copy rather than importing the runner,
// which would link the runner's callback and fixture code into a binary that runs next to
// untrusted test code.
func workspaceDigest(files map[string][]byte) string {
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
