// Package mockcontrol is the demo and test control surface shared by the HTTP fixtures that
// stand in for external services: an external barrier that records a request's arrival and
// then holds it unanswered until release, and an append-only JSON-lines record of every
// request the fixture received. Together they let a script prove that the external service
// itself saw a request, independently of what Emberling committed. Request kinds and any
// fields a line carries beyond seq, at, event and kind are supplied by the fixture.
package mockcontrol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Record event names the barrier writes. Each names one fact the fixture observed about one
// request it received, so a script can tell "the service saw the request" apart from
// "Emberling committed that it sent it".
const (
	// EventArrived is written as soon as a request has been decoded, before any barrier
	// hold and before any response byte is written.
	EventArrived = "arrived"
	// EventReleased is written when a request held by the barrier is let through.
	EventReleased = "released"
	// EventRejected is written when a held request is answered with an error instead of
	// being let through, because the fixture is shutting down.
	EventRejected = "rejected"
	// EventAbandoned is written when the caller disconnected while its request was held.
	EventAbandoned = "abandoned"
	// EventResponded is written by the fixture immediately before the response status is
	// sent.
	EventResponded = "responded"
)

// maxRecordResponseBytes bounds one GET of the record so a long-lived record file cannot
// make a single read unbounded. A caller pages forward with ?after=<seq>.
const maxRecordResponseBytes = 4 << 20

// maxRecordLineBytes bounds one record line when the file is read back. Every line a
// fixture writes holds only identifiers and short summaries, so it stays far below this.
const maxRecordLineBytes = 64 << 10

// Line is one record line before the Record numbers and timestamps it. It deliberately
// has no field that could carry a callback token, an authorization header, a prompt or a
// callback payload; a fixture keeps its Detail to identifiers and bounded summaries too.
type Line struct {
	Event string
	Kind  string
	// Detail is the fixture's own fields, encoded after kind in the same JSON object. It
	// must encode as a JSON object; nil adds nothing.
	Detail any
}

// lineHead is the part of every line the Record owns, in its fixed leading order.
type lineHead struct {
	Seq   int64  `json:"seq"`
	At    string `json:"at"`
	Event string `json:"event"`
	Kind  string `json:"kind"`
}

// Record is the append-only, file-backed request record. Every event is encoded as one
// JSON line and written with a single unbuffered write under mu, so concurrent requests
// never interleave inside a line and a line the process wrote survives the process being
// killed. Opening an existing file continues its sequence numbers instead of rewriting it.
type Record struct {
	now func() time.Time

	mu   sync.Mutex
	file *os.File
	path string
	seq  int64
}

// OpenRecord opens path for appending, creating it when absent, and continues sequence
// numbering after the last line already present.
func OpenRecord(path string) (*Record, error) {
	if path == "" {
		return nil, errors.New("mockcontrol: record path is empty")
	}
	lastSeq, err := lastRecordSeq(path)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("mockcontrol: open record: %w", err)
	}
	return &Record{
		now:  time.Now,
		file: file,
		path: path,
		seq:  lastSeq,
	}, nil
}

// lastRecordSeq returns the highest seq in an existing record file, or 0 when the file
// does not exist yet.
func lastRecordSeq(path string) (int64, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("mockcontrol: read record: %w", err)
	}
	defer file.Close()

	var last int64
	err = scanRecord(file, func(seq int64, _ []byte) bool {
		if seq > last {
			last = seq
		}
		return true
	})
	if err != nil {
		return 0, err
	}
	return last, nil
}

// scanRecord calls visit for every well-formed line in r, stopping early when visit
// returns false. A line that does not decode is skipped: a torn final line left by a
// killed process must not make the rest of the record unreadable.
func scanRecord(r io.Reader, visit func(seq int64, line []byte) bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), maxRecordLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		var head struct {
			Seq int64 `json:"seq"`
		}
		if err := json.Unmarshal(line, &head); err != nil {
			continue
		}
		if !visit(head.Seq, line) {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("mockcontrol: scan record: %w", err)
	}
	return nil
}

// Append assigns the next seq and timestamp to line, writes it as one line and returns
// the seq. A write failure is returned so a caller can surface it; the in-memory sequence
// still advances so seq values stay unique.
func (r *Record) Append(line Line) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	head, err := json.Marshal(lineHead{Seq: r.seq, At: r.now().UTC().Format(time.RFC3339Nano), Event: line.Event, Kind: line.Kind})
	if err != nil {
		return 0, fmt.Errorf("mockcontrol: encode record line: %w", err)
	}
	encoded, err := joinObjects(head, line.Detail)
	if err != nil {
		return 0, fmt.Errorf("mockcontrol: encode record line: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := r.file.Write(encoded); err != nil {
		return 0, fmt.Errorf("mockcontrol: write record line: %w", err)
	}
	return r.seq, nil
}

// readAfter writes every line with seq greater than after to w, stopping before the
// response would exceed maxRecordResponseBytes. It reports whether lines were left out.
func (r *Record) readAfter(w io.Writer, after int64) (bool, error) {
	file, err := os.Open(r.path)
	if err != nil {
		return false, fmt.Errorf("mockcontrol: read record: %w", err)
	}
	defer file.Close()

	var (
		buf       bytes.Buffer
		truncated bool
	)
	err = scanRecord(file, func(seq int64, line []byte) bool {
		if seq <= after {
			return true
		}
		if buf.Len()+len(line)+1 > maxRecordResponseBytes {
			truncated = true
			return false
		}
		buf.Write(line)
		buf.WriteByte('\n')
		return true
	})
	if err != nil {
		return false, err
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return false, fmt.Errorf("mockcontrol: write record response: %w", err)
	}
	return truncated, nil
}

// Close closes the record file. Lines appended afterwards fail instead of being lost
// silently.
func (r *Record) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.file.Close(); err != nil {
		return fmt.Errorf("mockcontrol: close record: %w", err)
	}
	return nil
}

// joinObjects encodes one JSON object holding head's fields followed by every field of
// each part, in order. head is already-encoded JSON; each part is encoded first, and a
// part that is nil or encodes as null adds nothing. Splicing the encoded objects keeps
// field order exactly as each part declares it, which is what keeps a fixture's record
// lines byte-stable.
func joinObjects(head []byte, parts ...any) ([]byte, error) {
	out := append([]byte(nil), head[:len(head)-1]...)
	empty := len(out) == 1
	for _, part := range parts {
		if part == nil {
			continue
		}
		encoded, err := json.Marshal(part)
		if err != nil {
			return nil, err
		}
		if string(encoded) == "null" {
			continue
		}
		if len(encoded) < 2 || encoded[0] != '{' || encoded[len(encoded)-1] != '}' {
			return nil, fmt.Errorf("detail %T does not encode as a JSON object", part)
		}
		inner := encoded[1 : len(encoded)-1]
		if len(inner) == 0 {
			continue
		}
		if !empty {
			out = append(out, ',')
		}
		out = append(out, inner...)
		empty = false
	}
	return append(out, '}'), nil
}
