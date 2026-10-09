package mockprovider

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sync"
	"time"
)

// Record event names. Each names one fact the Mock Provider observed about one request it
// received, so a script can tell "the Provider saw the dispatch" apart from "Emberling
// committed that it dispatched".
const (
	// recordArrived is written as soon as a request has been decoded, before any barrier
	// hold and before any response byte is written.
	recordArrived = "arrived"
	// recordReleased is written when a request held by the barrier is let through.
	recordReleased = "released"
	// recordRejected is written when a held request is answered with an error instead of
	// being let through, because the Provider is shutting down.
	recordRejected = "rejected"
	// recordAbandoned is written when the caller disconnected while its request was held.
	recordAbandoned = "abandoned"
	// recordResponded is written immediately before the response status is sent.
	recordResponded = "responded"
	// recordCallback is written after each callback delivery attempt completes.
	recordCallback = "callback"
)

// Request kinds the record and the barrier distinguish.
const (
	kindTask     = "task"
	kindGenerate = "generate"
)

// maxRecordTasks bounds the per-task delivery counters the record keeps in memory, the
// same way maxIdempotencyKeys bounds the idempotency map.
const maxRecordTasks = 10_000

// maxRecordResponseBytes bounds one GET of the record so a long-lived record file cannot
// make a single read unbounded. A caller pages forward with ?after=<seq>.
const maxRecordResponseBytes = 4 << 20

// maxRecordLineBytes bounds one record line when the file is read back. Every line this
// package writes holds only identifiers and short summaries, so it stays far below this.
const maxRecordLineBytes = 64 << 10

// recordEntry is one line of the dispatch record. It deliberately has no field that could
// carry a callback token, an authorization header, a prompt or a callback payload: the
// record holds identifiers and bounded summaries only.
type recordEntry struct {
	Seq            int64  `json:"seq"`
	At             string `json:"at"`
	Event          string `json:"event"`
	Kind           string `json:"kind"`
	ExternalTaskID string `json:"externalTaskId,omitempty"`
	// CallbackTarget is the callback URL reduced to scheme, host and path. Query string,
	// fragment and user info are dropped because a receiver could carry a credential there.
	CallbackTarget string `json:"callbackTarget,omitempty"`
	// Scenario summarises the scenario fields the request selected, such as "delay=lost"
	// or "scenario=final".
	Scenario string `json:"scenario,omitempty"`
	// Status is the HTTP status this Provider answered with (responded) or the callback
	// receiver answered with (callback).
	Status int `json:"status,omitempty"`
	// Replayed is true when a responded task request reused an idempotency key, so no new
	// task and no new callback were created.
	Replayed bool `json:"replayed,omitempty"`
	// Delivery is the 1-based count of callback attempts for ExternalTaskID within this
	// Provider process.
	Delivery int `json:"delivery,omitempty"`
	// Error is a fixed short summary; transport error text is never copied because it can
	// echo the full callback URL.
	Error string `json:"error,omitempty"`
}

// Record is the append-only, file-backed dispatch record. Every event is encoded as one
// JSON line and written with a single unbuffered write under mu, so concurrent requests
// never interleave inside a line and a line the process wrote survives the process being
// killed. Opening an existing file continues its sequence numbers instead of rewriting it.
type Record struct {
	now func() time.Time

	mu         sync.Mutex
	file       *os.File
	path       string
	seq        int64
	deliveries map[string]int
	taskOrder  []string
}

// OpenRecord opens path for appending, creating it when absent, and continues sequence
// numbering after the last line already present.
func OpenRecord(path string) (*Record, error) {
	if path == "" {
		return nil, errors.New("mockprovider: record path is empty")
	}
	lastSeq, err := lastRecordSeq(path)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("mockprovider: open record: %w", err)
	}
	return &Record{
		now:        time.Now,
		file:       file,
		path:       path,
		seq:        lastSeq,
		deliveries: make(map[string]int),
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
		return 0, fmt.Errorf("mockprovider: read record: %w", err)
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
		return fmt.Errorf("mockprovider: scan record: %w", err)
	}
	return nil
}

// append assigns the next seq and timestamp to entry, writes it as one line and returns
// the seq. A write failure is returned so a caller can surface it; the in-memory sequence
// still advances so seq values stay unique.
func (r *Record) append(entry recordEntry) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.appendLocked(entry)
}

func (r *Record) appendLocked(entry recordEntry) (int64, error) {
	r.seq++
	entry.Seq = r.seq
	entry.At = r.now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(entry)
	if err != nil {
		return 0, fmt.Errorf("mockprovider: encode record line: %w", err)
	}
	line = append(line, '\n')
	if _, err := r.file.Write(line); err != nil {
		return 0, fmt.Errorf("mockprovider: write record line: %w", err)
	}
	return entry.Seq, nil
}

// appendDelivery records one callback attempt for task, numbering it after the attempts
// already recorded for the same externalTaskId in this process.
func (r *Record) appendDelivery(task callbackTask, status int, deliveryErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, known := r.deliveries[task.ExternalTaskID]; !known {
		if len(r.taskOrder) >= maxRecordTasks {
			oldest := r.taskOrder[0]
			r.taskOrder = r.taskOrder[1:]
			delete(r.deliveries, oldest)
		}
		r.taskOrder = append(r.taskOrder, task.ExternalTaskID)
	}
	r.deliveries[task.ExternalTaskID]++

	entry := recordEntry{
		Event:          recordCallback,
		Kind:           kindTask,
		ExternalTaskID: task.ExternalTaskID,
		CallbackTarget: callbackTarget(task.CallbackURL),
		Status:         status,
		Delivery:       r.deliveries[task.ExternalTaskID],
	}
	if deliveryErr != nil {
		entry.Error = "callback delivery failed"
	}
	// A record write failure cannot be reported to anyone from a delivery goroutine; the
	// gap shows up as a missing seq-ordered callback line.
	_, _ = r.appendLocked(entry)
}

// readAfter writes every line with seq greater than after to w, stopping before the
// response would exceed maxRecordResponseBytes. It reports whether lines were left out.
func (r *Record) readAfter(w io.Writer, after int64) (bool, error) {
	file, err := os.Open(r.path)
	if err != nil {
		return false, fmt.Errorf("mockprovider: read record: %w", err)
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
		return false, fmt.Errorf("mockprovider: write record response: %w", err)
	}
	return truncated, nil
}

// Close closes the record file. Entries appended afterwards fail instead of being lost
// silently.
func (r *Record) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.file.Close(); err != nil {
		return fmt.Errorf("mockprovider: close record: %w", err)
	}
	return nil
}

// callbackTarget reduces a callback URL to scheme, host and path, the part of the target a
// script needs to recognise it. Anything that can carry a credential is dropped.
func callbackTarget(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "unparseable"
	}
	reduced := url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: parsed.Path}
	return reduced.String()
}
