package mockprovider

import (
	"net/url"

	"github.com/leungll/Emberling/backend/internal/mockcontrol"
)

// Record event names. Each names one fact the Mock Provider observed about one request it
// received, so a script can tell "the Provider saw the dispatch" apart from "Emberling
// committed that it dispatched". The barrier also writes mockcontrol.EventAbandoned when a
// caller disconnects while held.
const (
	// recordArrived is written as soon as a request has been decoded, before any barrier
	// hold and before any response byte is written.
	recordArrived = mockcontrol.EventArrived
	// recordReleased is written when a request held by the barrier is let through.
	recordReleased = mockcontrol.EventReleased
	// recordRejected is written when a held request is answered with an error instead of
	// being let through, because the Provider is shutting down.
	recordRejected = mockcontrol.EventRejected
	// recordResponded is written immediately before the response status is sent.
	recordResponded = mockcontrol.EventResponded
	// recordCallback is written after each callback delivery attempt completes.
	recordCallback = "callback"
	// recordPolled is written when a task status query is answered.
	recordPolled = "polled"
)

// Request kinds the record and the barrier distinguish.
const (
	kindTask     = "task"
	kindGenerate = "generate"
	// kindPoll marks a task status query. It is never held by the barrier.
	kindPoll = "poll"
)

// maxRecordTasks bounds the per-task delivery counters the record keeps in memory, the
// same way maxIdempotencyKeys bounds the idempotency map.
const maxRecordTasks = 10_000

// Record is the append-only, file-backed dispatch record WithTestControls writes to.
type Record = mockcontrol.Record

// OpenRecord opens path for appending, creating it when absent, and continues sequence
// numbering after the last line already present.
func OpenRecord(path string) (*Record, error) {
	return mockcontrol.OpenRecord(path)
}

// recordEntry is one line of the dispatch record as it reads back. It deliberately has no
// field that could carry a callback token, an authorization header, a prompt or a callback
// payload: the record holds identifiers and bounded summaries only.
type recordEntry struct {
	Seq   int64  `json:"seq"`
	At    string `json:"at"`
	Event string `json:"event"`
	Kind  string `json:"kind"`
	recordDetail
}

// recordDetail is the Mock Provider's own part of a record line, encoded after kind.
type recordDetail struct {
	ExternalTaskID string `json:"externalTaskId,omitempty"`
	// AssetID is the generated asset a "generated" line reports.
	AssetID string `json:"assetId,omitempty"`
	// CallbackTarget is the callback URL reduced to scheme, host and path. Query string,
	// fragment and user info are dropped because a receiver could carry a credential there.
	CallbackTarget string `json:"callbackTarget,omitempty"`
	// Scenario summarises the scenario fields the request selected, such as "delay=lost"
	// or "scenario=final".
	Scenario string `json:"scenario,omitempty"`
	// Status is the HTTP status this Provider answered with (responded) or the callback
	// receiver answered with (callback).
	Status int `json:"status,omitempty"`
	// TaskStatus is the task status a poll line reported (RUNNING, SUCCEEDED or FAILED);
	// empty when the task was unknown.
	TaskStatus string `json:"taskStatus,omitempty"`
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

// line is entry as a record line with the given event; seq and at are assigned by the
// Record.
func (e recordEntry) line(event string) mockcontrol.Line {
	return mockcontrol.Line{Event: event, Kind: e.Kind, Detail: e.recordDetail}
}

// recordDelivery records one callback attempt for task, numbering it after the attempts
// already recorded for the same externalTaskId in this process.
func (c *testControls) recordDelivery(task callbackTask, status int, deliveryErr error) {
	c.deliveryMu.Lock()
	defer c.deliveryMu.Unlock()

	if _, known := c.deliveries[task.ExternalTaskID]; !known {
		if len(c.deliveryOrder) >= maxRecordTasks {
			oldest := c.deliveryOrder[0]
			c.deliveryOrder = c.deliveryOrder[1:]
			delete(c.deliveries, oldest)
		}
		c.deliveryOrder = append(c.deliveryOrder, task.ExternalTaskID)
	}
	c.deliveries[task.ExternalTaskID]++

	detail := recordDetail{
		ExternalTaskID: task.ExternalTaskID,
		CallbackTarget: callbackTarget(task.CallbackURL),
		Status:         status,
		Delivery:       c.deliveries[task.ExternalTaskID],
	}
	if deliveryErr != nil {
		detail.Error = "callback delivery failed"
	}
	// A record write failure cannot be reported to anyone from a delivery goroutine; the
	// gap shows up as a missing seq-ordered callback line.
	_, _ = c.record.Append(mockcontrol.Line{Event: recordCallback, Kind: kindTask, Detail: detail})
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
