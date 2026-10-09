package mockprovider

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
)

// maxTaskStatuses bounds the per-instance task status map the same way maxIdempotencyKeys
// bounds the idempotency map. Once full, the oldest task is forgotten first, and a status
// query for a forgotten task answers 404 exactly like one for a task never accepted.
const maxTaskStatuses = 10_000

// Task status values reported by GET /v1/tasks/{externalTaskId}.
const (
	taskStatusRunning   = "RUNNING"
	taskStatusSucceeded = "SUCCEEDED"
	taskStatusFailed    = "FAILED"
)

// taskStatusResponse is the body of a successful GET /v1/tasks/{externalTaskId}. Payload
// is present only once the task is terminal and is the same payload its callback carries.
// It deliberately has no field for the callback token or callback URL.
type taskStatusResponse struct {
	ExternalTaskID string          `json:"externalTaskId"`
	Status         string          `json:"status"`
	Payload        json.RawMessage `json:"payload,omitempty"`
}

// trackedTask is what the status map keeps for one accepted task: the outcome its
// callback will report and whether that callback has become due yet. It holds no
// callback token or callback URL.
type trackedTask struct {
	payload  json.RawMessage
	failed   bool
	terminal bool
}

// taskStatuses answers status queries for accepted tasks. It exists on every Server,
// independently of WithTestControls, because a status query is part of the simulated
// Provider's public surface rather than a test control. A task is RUNNING from acceptance
// until its callback first becomes due; from then on it reports its outcome. A task whose
// callback is never scheduled (delayMs "lost") therefore stays RUNNING: the simulated
// Provider never finishes it.
type taskStatuses struct {
	mu    sync.Mutex
	tasks map[string]*trackedTask
	order []string
}

func newTaskStatuses() *taskStatuses {
	return &taskStatuses{tasks: make(map[string]*trackedTask)}
}

// accept records task as RUNNING. Accepting an id again (a new request reusing an
// explicit externalTaskId) restarts it as RUNNING with the new outcome.
func (t *taskStatuses) accept(externalTaskID string, payload json.RawMessage, failed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, known := t.tasks[externalTaskID]; !known {
		if len(t.order) >= maxTaskStatuses {
			oldest := t.order[0]
			t.order = t.order[1:]
			delete(t.tasks, oldest)
		}
		t.order = append(t.order, externalTaskID)
	}
	t.tasks[externalTaskID] = &trackedTask{payload: payload, failed: failed}
}

// markDue makes task terminal. The Dispatcher calls it when a callback delivery for task
// starts, whatever the delivery's own outcome, because the simulated Provider has finished
// the task by then even if its callback is lost or rejected.
func (t *taskStatuses) markDue(task callbackTask) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if tracked, ok := t.tasks[task.ExternalTaskID]; ok {
		tracked.terminal = true
	}
}

// lookup returns the status response for externalTaskID and whether the task is known.
func (t *taskStatuses) lookup(externalTaskID string) (taskStatusResponse, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tracked, ok := t.tasks[externalTaskID]
	if !ok {
		return taskStatusResponse{}, false
	}
	resp := taskStatusResponse{ExternalTaskID: externalTaskID, Status: taskStatusRunning}
	if tracked.terminal {
		resp.Status = taskStatusSucceeded
		if tracked.failed {
			resp.Status = taskStatusFailed
		}
		resp.Payload = tracked.payload
	}
	return resp, true
}

// handleTaskStatus answers a status query for one accepted task. It never schedules,
// delivers or repeats a callback, and it is never held by the barrier: polling only reads
// what the simulated Provider already knows.
func (s *Server) handleTaskStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "externalTaskId")
	resp, ok := s.statuses.lookup(id)
	if !ok {
		s.recordPoll(id, http.StatusNotFound, "")
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "unknown externalTaskId"})
		return
	}
	s.recordPoll(id, http.StatusOK, resp.Status)
	writeJSON(w, http.StatusOK, resp)
}

// recordPoll writes one poll line to the dispatch record. It is a no-op without test
// controls.
func (s *Server) recordPoll(externalTaskID string, status int, taskStatus string) {
	if s.controls == nil {
		return
	}
	_, _ = s.controls.record.append(recordEntry{
		Event:          recordPolled,
		Kind:           kindPoll,
		ExternalTaskID: truncateSummary(externalTaskID),
		Status:         status,
		TaskStatus:     taskStatus,
	})
}
