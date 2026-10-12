package sandboxrunner

import (
	"context"
	"io"
)

// Task states an AXTaskControl reports. They are plain strings, not a named type, so a
// stand-in for AX can satisfy the interface without importing this package.
const (
	// AXTaskPending is a Task that exists but does not report ready.
	AXTaskPending = "pending"
	// AXTaskReady is a Task whose sandbox reports ready: its files are written and the
	// test command has started.
	AXTaskReady = "ready"
	// AXTaskFailed is a Task that AX reports as failed to start or run.
	AXTaskFailed = "failed"
	// AXTaskSuspended is a Task that AX suspended. AX creates every Task suspended, so the
	// state means something only after the Task was resumed.
	AXTaskSuspended = "suspended"
	// AXTaskMissing is a Task that does not exist.
	AXTaskMissing = "missing"
)

// AXTaskControl is the ax Backend's only way to change or observe AX resources. Every
// method is scoped to one atespace. The real implementation drives the ax CLI; tests use
// a deterministic stand-in.
type AXTaskControl interface {
	// Apply submits a stream of Workspace and Task documents, each a JSON object, separated
	// by YAML document separators. An error means the outcome is unknown: the resources may
	// or may not exist.
	Apply(ctx context.Context, atespace string, documents []byte) error
	// Resume starts the named Task, which AX creates suspended. resumed is false when AX
	// refused; an error means the outcome is unknown: the Task may or may not have started.
	Resume(ctx context.Context, atespace, name string) (resumed bool, err error)
	// TaskState reports one of the AXTask* states for the named Task.
	TaskState(ctx context.Context, atespace, name string) (string, error)
	// Delete removes the Task and the Workspace of that name. A resource that is already
	// gone is not an error.
	Delete(ctx context.Context, atespace, name string) error
	// List returns the names of the Tasks and Workspaces whose name starts with prefix.
	List(ctx context.Context, atespace, prefix string) ([]string, error)
}

// AXResultReader queries the sandbox supervisor of one Task through the AX router. An
// error is a transport failure.
type AXResultReader interface {
	// ReadReady reports the HTTP status of the supervisor's readiness endpoint.
	ReadReady(ctx context.Context, atespace, taskName string) (int, error)
	// ReadResult reports the HTTP status and the unread body of the supervisor's result
	// endpoint; the caller bounds the read and closes the body.
	ReadResult(ctx context.Context, atespace, taskName string) (int, io.ReadCloser, error)
}
