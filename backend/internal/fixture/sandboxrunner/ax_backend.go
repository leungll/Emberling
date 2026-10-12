package sandboxrunner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/fixture/mockcontrol"
)

// axResourcePrefix starts the name of every Task and Workspace the ax Backend creates.
// Startup cleanup deletes every resource with this prefix in the runner's atespace, so the
// atespace must belong to one runner.
const axResourcePrefix = "emberling-sbx-"

// axWorkPath is where the Task binds its one Workspace, the same working directory the
// docker Backend uses.
const axWorkPath = "/work"

// axAPIVersion is the AX resource version the documents are written in.
const axAPIVersion = "ax.io/v1alpha1"

// Polling bounds. The interval paces both the readiness and the result polling, and both
// reach the supervisor through the same router channel under the same rules: one query
// is bounded by axRouterQueryTimeout, and queries that fail maxRouterQueryFailures rounds
// in a row are treated as a broken tunnel or router rather than a transient interruption.
const (
	axPollInterval         = time.Second
	axRouterQueryTimeout   = 5 * time.Second
	maxRouterQueryFailures = 3
)

// eventCleanupFailed is the record line written when the AX resources of a finished test
// could not be deleted; the next startup cleanup and the operator's inventory remove them.
const eventCleanupFailed = "cleanup_failed"

// taskIDPattern is the only test id shape a Task name can be derived from.
var taskIDPattern = regexp.MustCompile(`^test_[0-9a-f]{16}$`)

// rfc1123Label is a lowercase RFC 1123 label without its length limit.
var rfc1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ValidAXName reports whether name can be an AX resource or atespace name: a lowercase
// RFC 1123 label of at most 63 characters.
func ValidAXName(name string) bool {
	return len(name) <= 63 && rfc1123Label.MatchString(name)
}

// axTaskName derives the Task and Workspace name from a test id: test_<16 hex> becomes
// emberling-sbx-<16 hex>. The name is deterministic, so a resource AX accepted before the
// runner could note it is still found by its test id or by the startup cleanup.
func axTaskName(testID string) (string, error) {
	if !taskIDPattern.MatchString(testID) {
		return "", errors.New("test id cannot name an AX task")
	}
	name := axResourcePrefix + strings.TrimPrefix(testID, "test_")
	if !ValidAXName(name) {
		return "", errors.New("derived AX task name is not an RFC 1123 label")
	}
	return name, nil
}

// Clock is the time source the ax Backend measures deadlines with and waits between
// polling rounds on.
type Clock interface {
	Now() time.Time
	// Sleep waits d, returning early with ctx's error when ctx ends.
	Sleep(ctx context.Context, d time.Duration) error
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// AXBackendConfig configures an AXBackend.
type AXBackendConfig struct {
	// Atespace is the dedicated atespace every Task and Workspace is created in.
	Atespace string
	// Image is the sandbox supervisor image, pinned by digest.
	Image string
	// TestTimeout bounds one test from the start of resource creation, ready wait and
	// result polling included.
	TestTimeout time.Duration
	// ReadyTimeout bounds the wait for the Task to become ready, counted from the resume.
	ReadyTimeout time.Duration
	Control      AXTaskControl
	Results      AXResultReader
	// Clock defaults to the system clock.
	Clock Clock
	// Logger and Record, when set, receive resource deletions that failed.
	Logger *slog.Logger
	Record *mockcontrol.Record
}

// AXBackend runs each test as one AX Task in a gVisor sandbox. The runner applies the
// patch itself and sends the patched tree as an inline Workspace; the sandbox supervisor
// writes it out, runs the fixed test command and publishes its exit code, output and the
// digest of the tree it wrote. A request can choose none of the image, the command, the
// environment or the resources.
type AXBackend struct {
	cfg AXBackendConfig
}

// NewAXBackend returns an ax Backend, or an error naming the first unusable setting.
func NewAXBackend(cfg AXBackendConfig) (*AXBackend, error) {
	switch {
	case !ValidAXName(cfg.Atespace):
		return nil, errors.New("sandboxrunner: the ax atespace must be a lowercase RFC 1123 label")
	case !strings.Contains(cfg.Image, "@sha256:"):
		return nil, errors.New("sandboxrunner: the ax image must be pinned by digest")
	case cfg.TestTimeout <= 0 || cfg.ReadyTimeout <= 0:
		return nil, errors.New("sandboxrunner: the ax timeouts must be positive")
	case cfg.Control == nil || cfg.Results == nil:
		return nil, errors.New("sandboxrunner: the ax backend needs task control and a result reader")
	}
	if cfg.Clock == nil {
		cfg.Clock = systemClock{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return &AXBackend{cfg: cfg}, nil
}

// ValidateMock rejects every mock mode: an AX run is never simulated.
func (*AXBackend) ValidateMock(mock string) error {
	if mock != "" {
		return errors.New("mock is accepted only by the mock backend")
	}
	return nil
}

// Run tests job in one AX Task. An unknown base commit, a patch that does not apply, a
// test id that cannot name a Task and a tree AX cannot carry all fail before any AX
// resource exists. Once creation was attempted, the Task and Workspace are deleted on
// every path before the outcome is returned; neither creation nor resume is ever retried.
func (b *AXBackend) Run(ctx context.Context, job Job) Outcome {
	if job.BaseCommit != FixtureBaseCommit {
		return Outcome{FailureCode: codeUnknownBaseCommit, FailureMessage: "only base commit " + FixtureBaseCommit + " is available"}
	}
	files, err := fixtureFiles()
	if err != nil {
		return sandboxError("fixture could not be read")
	}
	if err := applyPatch(files, job.Patch); err != nil {
		return Outcome{FailureCode: codePatchRejected, FailureMessage: "patch does not apply to " + FixtureBaseCommit}
	}
	name, err := axTaskName(job.TestID)
	if err != nil {
		return sandboxError("test id cannot name an AX task")
	}
	if message := checkWorkspaceFiles(files); message != "" {
		return sandboxError(message)
	}
	if estimatedWorkspaceEncoding(name, b.cfg.Atespace, files) > maxEncodedWorkspaceChars {
		return sandboxError("patched workspace is too large to reach the sandbox")
	}
	documents, err := b.documents(name, files)
	if err != nil {
		return sandboxError("AX documents could not be built")
	}

	outcome := b.execute(ctx, name, documents, WorkspaceDigest(files))
	b.deleteResources(ctx, job.TestID, name)
	return outcome
}

// checkWorkspaceFiles returns why the patched tree cannot travel as an inline Workspace,
// or "". Paths and contents are protobuf strings and must be valid UTF-8. A content that
// starts with a space or a tab does not survive AX's YAML encoding: the sandbox cannot
// read the Workspace back and the Task never becomes ready, so such a file is refused
// before anything is created.
func checkWorkspaceFiles(files map[string][]byte) string {
	total := 0
	for name, content := range files {
		if !utf8.ValidString(name) || !utf8.Valid(content) {
			return "patched workspace is not valid UTF-8"
		}
		if len(content) > 0 && (content[0] == ' ' || content[0] == '\t') {
			return "patched workspace has a file starting with whitespace, which AX cannot carry"
		}
		total += len(content)
	}
	if total > maxWorkspaceContentBytes {
		return "patched workspace exceeds 16 KiB"
	}
	return ""
}

// axMetadata names one AX resource.
type axMetadata struct {
	Name     string `json:"name"`
	Atespace string `json:"atespace"`
}

type axFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type axWorkspaceDoc struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Metadata   axMetadata `json:"metadata"`
	Spec       struct {
		Files []axFile `json:"files"`
	} `json:"spec"`
}

type axWorkspaceBinding struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// axTaskSpec is the complete Task spec. Every member is fixed except the names; env is an
// always-empty list, so no test data and no credential ever reaches the Task.
type axTaskSpec struct {
	Image      string               `json:"image"`
	Workspaces []axWorkspaceBinding `json:"workspaces"`
	Env        []struct{}           `json:"env"`
	Debug      bool                 `json:"debug"`
	Resources  axResources          `json:"resources"`
}

// axResources mirrors the docker Backend's CPU and memory limits. AX accepts the field but
// does not pass it on to the sandbox it starts, so the limits have no effect today; they
// are written only so they apply once AX honours them, and no guarantee rests on them.
type axResources struct {
	Limits struct {
		CPU    string `json:"cpu"`
		Memory string `json:"memory"`
	} `json:"limits"`
}

type axTaskDoc struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Metadata   axMetadata `json:"metadata"`
	Spec       axTaskSpec `json:"spec"`
}

// documents returns the Workspace and Task as two JSON documents in one YAML stream; JSON
// is a subset of YAML, so no YAML encoder is needed. Files are listed in path byte order.
func (b *AXBackend) documents(name string, files map[string][]byte) ([]byte, error) {
	metadata := axMetadata{Name: name, Atespace: b.cfg.Atespace}
	workspace := axWorkspaceDoc{APIVersion: axAPIVersion, Kind: "Workspace", Metadata: metadata}
	workspace.Spec.Files = []axFile{}
	for _, path := range slices.Sorted(maps.Keys(files)) {
		workspace.Spec.Files = append(workspace.Spec.Files, axFile{Path: path, Content: string(files[path])})
	}
	task := axTaskDoc{APIVersion: axAPIVersion, Kind: "Task", Metadata: metadata, Spec: axTaskSpec{
		Image:      b.cfg.Image,
		Workspaces: []axWorkspaceBinding{{Name: name, Path: axWorkPath}},
		Env:        []struct{}{},
	}}
	task.Spec.Resources.Limits.CPU = "1"
	task.Spec.Resources.Limits.Memory = "512Mi"
	encodedWorkspace, err := json.Marshal(workspace)
	if err != nil {
		return nil, err
	}
	encodedTask, err := json.Marshal(task)
	if err != nil {
		return nil, err
	}
	stream := make([]byte, 0, len(encodedWorkspace)+len(encodedTask)+6)
	stream = append(stream, encodedWorkspace...)
	stream = append(stream, "\n---\n"...)
	stream = append(stream, encodedTask...)
	return append(stream, '\n'), nil
}

// execute creates the resources, starts the Task, waits for it to be ready and polls its
// result. The caller deletes the resources afterwards whatever the outcome.
func (b *AXBackend) execute(ctx context.Context, name string, documents []byte, digest string) Outcome {
	deadline := b.cfg.Clock.Now().Add(b.cfg.TestTimeout)
	applyCtx, cancel := b.untilDeadline(ctx, deadline)
	err := b.cfg.Control.Apply(applyCtx, b.cfg.Atespace, documents)
	cancel()
	if err != nil {
		// The create may have taken effect; the caller deletes the derived name, and the
		// create is never sent again.
		return b.callFailed(ctx, deadline, "AX task creation was not confirmed")
	}
	if outcome, ready := b.startTask(ctx, name, deadline); !ready {
		return outcome
	}
	return b.pollResult(ctx, name, deadline, digest)
}

// pollResult queries the supervisor once per interval until it reports a result. Each
// round first reads the Task state: a query through the router would resume a suspended
// actor, so a suspended or vanished Task is never queried. Transport failures and non-200
// answers are transient until maxRouterQueryFailures happen in a row.
func (b *AXBackend) pollResult(ctx context.Context, name string, deadline time.Time, digest string) Outcome {
	failures := 0
	for {
		if ctx.Err() != nil {
			return interrupted()
		}
		if !b.cfg.Clock.Now().Before(deadline) {
			return b.timeout()
		}
		state, outcome, ok := b.readState(ctx, name, deadline)
		if !ok {
			return outcome
		}
		switch state {
		case AXTaskReady, AXTaskPending:
		case AXTaskFailed:
			return sandboxError("AX task failed while the test ran")
		case AXTaskSuspended, AXTaskMissing:
			return sandboxError("AX task was suspended or disappeared while the test ran")
		default:
			return sandboxError("AX reported an unknown task state")
		}

		result, transient := b.queryResult(ctx, name, deadline)
		switch {
		case transient:
			failures++
			if failures >= maxRouterQueryFailures {
				return sandboxError("sandbox result could not be read through the AX router")
			}
		case result.state == resultRunning:
			failures = 0
		default:
			return result.outcome(digest)
		}
		if b.cfg.Clock.Sleep(ctx, axPollInterval) != nil {
			return interrupted()
		}
	}
}

// readState reads the Task state. It returns false with the outcome to report when the
// state could not be read.
func (b *AXBackend) readState(ctx context.Context, name string, deadline time.Time) (string, Outcome, bool) {
	stateCtx, cancel := b.untilDeadline(ctx, deadline)
	defer cancel()
	state, err := b.cfg.Control.TaskState(stateCtx, b.cfg.Atespace, name)
	if err != nil {
		return "", b.callFailed(ctx, deadline, "AX task state could not be read"), false
	}
	return state, Outcome{}, true
}

// untilDeadline bounds one AX call by the time left before the test deadline.
func (b *AXBackend) untilDeadline(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, max(deadline.Sub(b.cfg.Clock.Now()), 0))
}

// callFailed maps a failed AX call: an ended run is interrupted, a passed deadline is a
// timeout, anything else is a sandbox error with message.
func (b *AXBackend) callFailed(ctx context.Context, deadline time.Time, message string) Outcome {
	switch {
	case ctx.Err() != nil:
		return interrupted()
	case !b.cfg.Clock.Now().Before(deadline):
		return b.timeout()
	default:
		return sandboxError(message)
	}
}

func (b *AXBackend) timeout() Outcome {
	return Outcome{FailureCode: codeTimeout, FailureMessage: "test run exceeded " + b.cfg.TestTimeout.String()}
}

// interrupted is the outcome of a run whose context ended, which the Server discards.
func interrupted() Outcome {
	return sandboxError("test run was interrupted")
}

func sandboxError(message string) Outcome {
	return Outcome{FailureCode: codeSandboxError, FailureMessage: message}
}

// cleanupFailure is the record line for a deletion that failed.
type cleanupFailure struct {
	TestID string `json:"testId"`
	Task   string `json:"task"`
}

// deleteResources deletes the Task and Workspace of name. It runs even after ctx has
// ended, which is exactly when a stopped run leaves them behind, so it keeps ctx's values
// but not its cancellation. A failure does not change the test's outcome: it is logged
// and recorded, and the next startup cleanup retries it.
func (b *AXBackend) deleteResources(parent context.Context, testID, name string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), cleanupTimeout)
	defer cancel()
	err := b.cfg.Control.Delete(ctx, b.cfg.Atespace, name)
	if err == nil {
		return
	}
	b.cfg.Logger.Error("sandboxrunner: ax resources could not be deleted",
		slog.String("test_id", testID), slog.String("task", name), slog.String("error", err.Error()))
	if b.cfg.Record != nil {
		_, _ = b.cfg.Record.Append(mockcontrol.Line{Event: eventCleanupFailed, Kind: kindTest, Detail: cleanupFailure{TestID: testID, Task: name}})
	}
}

// RemoveOrphans deletes every Task and Workspace with the runner's prefix in its atespace
// and returns how many it deleted. It is called once at startup, before any test runs, so
// it also removes resources a previous runner created but never recorded.
func (b *AXBackend) RemoveOrphans(ctx context.Context) (int, error) {
	names, err := b.cfg.Control.List(ctx, b.cfg.Atespace, axResourcePrefix)
	if err != nil {
		return 0, errors.New("sandboxrunner: list ax resources: " + err.Error())
	}
	removed := 0
	for _, name := range names {
		if !strings.HasPrefix(name, axResourcePrefix) {
			continue
		}
		if err := b.cfg.Control.Delete(ctx, b.cfg.Atespace, name); err != nil {
			return removed, errors.New("sandboxrunner: delete ax resources: " + err.Error())
		}
		removed++
	}
	return removed, nil
}
