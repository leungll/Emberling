package service

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/leungll/Emberling/backend/internal/store"
)

// runListCursorVersion lets a later cursor layout be told apart from this one; a cursor of
// any other version is rejected rather than reinterpreted.
const runListCursorVersion = 1

// runListCursor is the decoded form of an opaque Run list cursor: one page boundary, the
// side of it the next read continues on, and a digest of the filters it was issued under.
// The boundary is the keyset (started_at, id), so Runs created while a client pages only
// ever land before the first page and never shift a page an issued cursor names.
type runListCursor struct {
	Version   int    `json:"v"`
	Direction string `json:"d"`
	// StartedAtMicros is started_at in Unix microseconds, PostgreSQL's own precision, so
	// the boundary round-trips exactly.
	StartedAtMicros int64  `json:"t"`
	ID              string `json:"id"`
	Filter          string `json:"f"`
}

const (
	cursorDirectionOlder = "older"
	cursorDirectionNewer = "newer"
)

func encodeRunListCursor(pos store.RunPosition, direction store.RunListDirection, filterDigest string) string {
	d := cursorDirectionOlder
	if direction == store.RunsNewer {
		d = cursorDirectionNewer
	}
	raw, _ := json.Marshal(runListCursor{
		Version:         runListCursorVersion,
		Direction:       d,
		StartedAtMicros: pos.StartedAt.UnixMicro(),
		ID:              pos.ID,
		Filter:          filterDigest,
	})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeRunListCursor rejects anything that is not a cursor this service issued for the
// same filters: undecodable text, an unknown version or direction, a missing boundary, or
// a filter digest that differs from the current request's.
func decodeRunListCursor(encoded, filterDigest string) (store.RunPosition, store.RunListDirection, error) {
	invalid := &RunListQueryError{Message: "cursor is not valid for this query; restart from the first page"}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return store.RunPosition{}, 0, invalid
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var c runListCursor
	if err := dec.Decode(&c); err != nil || dec.More() {
		return store.RunPosition{}, 0, invalid
	}
	if c.Version != runListCursorVersion || c.ID == "" || c.Filter != filterDigest {
		return store.RunPosition{}, 0, invalid
	}
	var direction store.RunListDirection
	switch c.Direction {
	case cursorDirectionOlder:
		direction = store.RunsOlder
	case cursorDirectionNewer:
		direction = store.RunsNewer
	default:
		return store.RunPosition{}, 0, invalid
	}
	return store.RunPosition{StartedAt: time.UnixMicro(c.StartedAtMicros).UTC(), ID: c.ID}, direction, nil
}

// runListFilterDigest binds a cursor to the filters it was issued under. Page size is
// deliberately not part of it: a client may change the page size between pages.
func runListFilterDigest(filter store.RunListFilter) string {
	started := ""
	if filter.StartedFrom != nil {
		started = strconv.FormatInt(filter.StartedFrom.UnixMicro(), 10)
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		filter.WorkflowID, string(filter.Status), started, filter.IDPrefix,
	}, "\x00")))
	return hex.EncodeToString(sum[:8])
}
