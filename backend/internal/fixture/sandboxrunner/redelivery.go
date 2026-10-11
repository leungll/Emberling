package sandboxrunner

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

// redeliverySuffix names the redelivery store next to the request record.
const redeliverySuffix = ".redelivery"

// maxRedeliveryEntries bounds how many finished results one startup redelivers; the most
// recently stored are kept.
const maxRedeliveryEntries = 10_000

// maxRedeliveryLineBytes bounds one stored entry when the store is read back.
const maxRedeliveryLineBytes = 1 << 20

// redeliveryEntry is one finished result as the redelivery store keeps it. Unlike a record
// line it holds the callback token, which is why the store is a separate file that only its
// owner can read and that no route serves.
type redeliveryEntry struct {
	TestID        string          `json:"testId"`
	CallbackURL   string          `json:"callbackUrl"`
	CallbackToken string          `json:"callbackToken"`
	Payload       json.RawMessage `json:"payload"`
	// Deliveries is how many delivery attempts the result has had, so a redelivery
	// continues the attempt numbering.
	Deliveries int `json:"deliveries"`
}

// RedeliveryPath returns the redelivery store path that belongs to a request record path.
func RedeliveryPath(recordPath string) string {
	return recordPath + redeliverySuffix
}

// RedeliveryStore is the append-only file of finished results a restarted runner delivers
// once more. A later entry for the same testId supersedes an earlier one.
type RedeliveryStore struct {
	mu   sync.Mutex
	file *os.File
}

// StoredResult is one finished result read back from a RedeliveryStore.
type StoredResult struct {
	entry redeliveryEntry
}

// OpenRedeliveryStore opens path for appending with owner-only permissions, creating it
// when absent, and returns the results already stored there in first-stored order. A line
// that does not decode is skipped: a torn final line left by a killed process must not
// lose every other result.
func OpenRedeliveryStore(path string) (*RedeliveryStore, []StoredResult, error) {
	stored, err := readRedeliveryEntries(path)
	if err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("sandboxrunner: open redelivery store: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("sandboxrunner: restrict redelivery store: %w", err)
	}
	return &RedeliveryStore{file: file}, stored, nil
}

func readRedeliveryEntries(path string) ([]StoredResult, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sandboxrunner: read redelivery store: %w", err)
	}
	defer file.Close()

	latest := make(map[string]redeliveryEntry)
	var order []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), maxRedeliveryLineBytes)
	for scanner.Scan() {
		var entry redeliveryEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil || entry.TestID == "" || entry.CallbackURL == "" || len(entry.Payload) == 0 {
			continue
		}
		if _, seen := latest[entry.TestID]; !seen {
			order = append(order, entry.TestID)
		}
		latest[entry.TestID] = entry
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("sandboxrunner: read redelivery store: %w", err)
	}
	if len(order) > maxRedeliveryEntries {
		order = order[len(order)-maxRedeliveryEntries:]
	}
	stored := make([]StoredResult, 0, len(order))
	for _, testID := range order {
		stored = append(stored, StoredResult{entry: latest[testID]})
	}
	return stored, nil
}

// Append stores entry as one line.
func (r *RedeliveryStore) Append(entry redeliveryEntry) error {
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("sandboxrunner: encode redelivery entry for test %s: %w", entry.TestID, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.file.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("sandboxrunner: write redelivery entry for test %s: %w", entry.TestID, err)
	}
	return nil
}

// Close closes the store file.
func (r *RedeliveryStore) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.file.Close()
}

// Redeliver delivers each stored result once more, in stored order, in one goroutine the
// Server owns, and makes each result answer GET /v1/tests/{testId} again. A restarted
// runner calls it once at startup; the Runtime deduplicates by its Callback Binding.
func (s *Server) Redeliver(stored []StoredResult) {
	if len(stored) == 0 {
		return
	}
	for _, result := range stored {
		s.setState(result.entry.TestID, payloadStatus(result.entry.Payload), result.entry.Payload)
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		for _, result := range stored {
			if s.ctx.Err() != nil {
				return
			}
			entry := result.entry
			entry.Deliveries++
			s.appendLine(eventRedelivered, kindCallback, callbackArrival{TestID: entry.TestID, Attempt: entry.Deliveries})
			s.storeForRedelivery(entry)
			s.deliver(s.ctx, entry.TestID, callbackDestination{URL: entry.CallbackURL, Token: entry.CallbackToken}, entry.Payload, entry.Deliveries)
		}
	}()
}
