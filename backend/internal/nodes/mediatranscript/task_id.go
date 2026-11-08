package mediatranscript

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// TaskIDPrefix marks every external task id this node computes. The id is stored in the
// Callback Binding, which is unique across all Providers; the prefix differs from the one
// the external Tools use, so a node task and a Tool task can never share an id even when
// their digests would.
const TaskIDPrefix = "extn_"

// taskIDDigestHex is how many hex characters of the digest an id keeps: 128 bits, enough
// that two Attempts never share an id in practice while the id stays short in Trace.
const taskIDDigestHex = 32

// TaskID is the external task id of one Node Attempt, computed before dispatch from the
// NodeRun id and the Attempt number alone. The same Attempt always yields the same id, so
// a dispatch whose answer was lost can be matched to the task the service holds, and a
// retry Attempt yields a different id, so it never replays the previous task. The id is a
// digest rather than the NodeRun id itself so the service learns nothing about
// Emberling's identifiers.
func TaskID(nodeRunID string, attemptNo int) string {
	sum := sha256.Sum256([]byte(nodeRunID + "\n" + strconv.Itoa(attemptNo)))
	return TaskIDPrefix + hex.EncodeToString(sum[:])[:taskIDDigestHex]
}
