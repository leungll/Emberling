// Package mockax is a deterministic, in-process stand-in for AX: the Task and Workspace
// control the sandbox runner's ax backend drives, and the sandbox supervisor's result
// endpoint it reads through the AX router. It stands in for an external system, so it
// shares no Go type with the runner: it satisfies the runner's narrow interfaces by method
// signature alone, and the file tree digest it reports is computed by a function its
// caller injects.
//
// Like AX, the fake creates every Task suspended; only a resume starts it. What a Task does
// then is chosen by the digest of the Workspace files it was applied with, looked up in a
// table of Scenarios; a tree with no entry gets the default Scenario, a passing run of
// twelve tests that is ready as soon as it is resumed. Every call can be recorded and held at a mockcontrol
// barrier, like the other fixtures' requests.
package mockax

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/leungll/Emberling/backend/internal/fixture/mockcontrol"
)

// Task states the fake reports. They carry the same values as the runner's own state
// constants, which this package cannot import.
const (
	TaskPending   = "pending"
	TaskReady     = "ready"
	TaskFailed    = "failed"
	TaskSuspended = "suspended"
	TaskMissing   = "missing"
)

// Request kinds the record and the barrier distinguish, one per interface method.
const (
	KindApply  = "apply"
	KindResume = "resume"
	KindState  = "state"
	KindReady  = "ready"
	KindResult = "result"
	KindDelete = "delete"
	KindList   = "list"
)

// apiVersion is the only document version the fake accepts.
const apiVersion = "ax.io/v1alpha1"

// Startup is how a Task's start goes once it was resumed.
type Startup int

const (
	// StartupReady makes the Task report ready at its first state read after the resume.
	StartupReady Startup = iota
	// StartupFailed makes AX report the Task as failed to start.
	StartupFailed
	// StartupNeverReady keeps the Task from reporting ready and its readiness endpoint
	// answering 503 forever.
	StartupNeverReady
	// StartupReadyLater keeps the Task from reporting ready, as AX does when its sandbox
	// was not ready while it resumed it; the readiness endpoint answers ReadyAnswers in
	// turn and 200 after them.
	StartupReadyLater
)

// Fate is what happens to a Task after it was reported ready.
type Fate int

const (
	// FateStays keeps the Task as it is.
	FateStays Fate = iota
	// FateSuspended reports the Task as suspended from the next state read on.
	FateSuspended
	// FateMissing removes the Task and its Workspace before the next state read.
	FateMissing
)

// Forever, as RunningRounds, answers every result query with "running".
const Forever = -1

// Scenario is what the fake does with one applied Workspace.
type Scenario struct {
	// ResumeRefused makes AX refuse the resume and report the Task as failed.
	ResumeRefused bool
	Startup       Startup
	// ReadyAnswers are the HTTP statuses of the readiness queries under
	// StartupReadyLater, in order; zero makes that query a transport error instead.
	ReadyAnswers []int
	AfterReady   Fate
	// QueryFailures fails this many result queries, counted from the first one, before
	// any of them is answered.
	QueryFailures int
	// QueryFailureStatus is the HTTP status a failed query answers; zero makes it a
	// transport error instead.
	QueryFailureStatus int
	// RunningRounds answers this many result queries after the failures with "running";
	// Forever never finishes.
	RunningRounds int
	// WorkspaceInvalid answers every result query with the failed form, as a supervisor
	// that refused the Workspace does.
	WorkspaceInvalid bool
	// Body, when set, is served verbatim as the finished result.
	Body []byte

	ExitCode        int
	Output          string
	OutputTruncated bool
	// WorkspaceDigest, when set, is reported instead of the digest of the applied files.
	WorkspaceDigest string
}

// DefaultScenario is a passing run of twelve tests.
func DefaultScenario() Scenario {
	return Scenario{ExitCode: 0, Output: GoTestOutput(12, 0)}
}

// GoTestOutput returns `go test -json` output with passed passing and failed failing
// tests.
func GoTestOutput(passed, failed int) string {
	var b strings.Builder
	b.WriteString(`{"Action":"start","Package":"example.com/fixture"}` + "\n")
	for i := range passed {
		fmt.Fprintf(&b, `{"Action":"pass","Package":"example.com/fixture","Test":"TestPass%d"}`+"\n", i)
	}
	for i := range failed {
		fmt.Fprintf(&b, `{"Action":"fail","Package":"example.com/fixture","Test":"TestFail%d"}`+"\n", i)
	}
	return b.String()
}

// Config configures a Fake.
type Config struct {
	// Digest computes the file tree digest reported for an applied Workspace and used to
	// choose its Scenario. It is required.
	Digest func(files map[string][]byte) string
	// Default is the Scenario of a tree without an entry; nil means DefaultScenario.
	Default *Scenario
	// Scenarios chooses a Scenario by file tree digest.
	Scenarios map[string]Scenario
	// ApplyUncertain makes every Apply create its resources and then fail, as a create
	// whose response was lost does.
	ApplyUncertain bool
	// ResumeUncertain makes every Resume start its Task and then fail, as a resume whose
	// command did not exit on its own may have done.
	ResumeUncertain bool
	// DeleteFails makes every Delete fail and keep the resources.
	DeleteFails bool
	// Record, when set, records every call and enables the barrier.
	Record *mockcontrol.Record
	// HeldNotify, when set, receives the kind of each call right after the barrier holds
	// it. It is a test barrier.
	HeldNotify chan<- string
}

// resource is one Task with its same-named Workspace.
type resource struct {
	files         map[string][]byte
	digest        string
	scenario      Scenario
	resumed       bool
	readyReported bool
	readyQueries  int
	queries       int
}

// Fake is the in-process AX stand-in. It is safe for concurrent use.
type Fake struct {
	cfg     Config
	barrier *mockcontrol.Barrier

	mu        sync.Mutex
	resources map[string]*resource // keyed by atespace + "/" + name
	applied   [][]byte
	resumed   []string
	deleted   []string
}

// New returns a Fake with no resources.
func New(cfg Config) *Fake {
	if cfg.Digest == nil {
		panic("mockax: Config.Digest is required")
	}
	return &Fake{
		cfg:       cfg,
		barrier:   mockcontrol.NewBarrier(KindApply, KindResume, KindState, KindReady, KindResult, KindDelete, KindList),
		resources: make(map[string]*resource),
	}
}

// ControlHandler serves the barrier and the record under /control, like the other
// fixtures' control routes. Without a Record it serves nothing.
func (f *Fake) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	if f.cfg.Record != nil {
		mux.Handle("POST /control/pause", mockcontrol.PauseHandler(f.barrier))
		mux.Handle("POST /control/release", mockcontrol.ReleaseHandler(f.barrier))
		mux.Handle("GET /control/barrier", mockcontrol.BarrierHandler(f.barrier))
		mux.Handle("GET /control/record", mockcontrol.RecordHandler(f.cfg.Record))
	}
	return mux
}

// StopHolding rejects every held call and disables the barrier.
func (f *Fake) StopHolding() { f.barrier.Stop() }

// callRef identifies a call on the record. It names only the atespace and the resource.
type callRef struct {
	Atespace string `json:"atespace"`
	Name     string `json:"name,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
}

// admit records one call and holds it while the barrier is paused for its kind.
func (f *Fake) admit(ctx context.Context, kind string, ref callRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.cfg.Record == nil {
		return nil
	}
	return f.barrier.Admit(ctx, f.cfg.Record, mockcontrol.Request{Kind: kind, Detail: ref, Ref: ref}, func() {
		if f.cfg.HeldNotify == nil {
			return
		}
		select {
		case f.cfg.HeldNotify <- kind:
		case <-ctx.Done():
		}
	})
}

// Applied returns every document stream Apply received, in order.
func (f *Fake) Applied() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.applied)
}

// Resumed returns the name of every Resume call, in order, whether or not it succeeded.
func (f *Fake) Resumed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.resumed)
}

// Deleted returns the name of every Delete call, in order, whether or not it succeeded.
func (f *Fake) Deleted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.deleted)
}

// Seed creates a Task and its Workspace with no files, as a previous runner process
// would have left behind.
func (f *Fake) Seed(atespace, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resources[atespace+"/"+name] = &resource{files: map[string][]byte{}, scenario: f.defaultScenario()}
}

func (f *Fake) defaultScenario() Scenario {
	if f.cfg.Default != nil {
		return *f.cfg.Default
	}
	return DefaultScenario()
}

// document is the part of a Workspace or Task document the fake reads.
type document struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name     string `json:"name"`
		Atespace string `json:"atespace"`
	} `json:"metadata"`
	Spec struct {
		Files []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"files"`
		Workspaces []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"workspaces"`
	} `json:"spec"`
}

// Apply creates one Workspace and one Task of the same name from a stream of JSON
// documents separated by YAML document separators. A name already in use is refused, as
// AX refuses to create a Task twice.
func (f *Fake) Apply(ctx context.Context, atespace string, documents []byte) error {
	workspace, task, err := parseDocuments(atespace, documents)
	ref := callRef{Atespace: atespace}
	if err == nil {
		ref.Name = workspace.Metadata.Name
	}
	if admitErr := f.admit(ctx, KindApply, ref); admitErr != nil {
		return admitErr
	}
	if err != nil {
		return err
	}
	if task.Metadata.Name != workspace.Metadata.Name || len(task.Spec.Workspaces) != 1 ||
		task.Spec.Workspaces[0].Name != workspace.Metadata.Name {
		return errors.New("mockax: the Task must bind exactly its same-named Workspace")
	}
	files := make(map[string][]byte, len(workspace.Spec.Files))
	for _, file := range workspace.Spec.Files {
		if file.Path == "" {
			return errors.New("mockax: a Workspace file has an empty path")
		}
		files[file.Path] = []byte(file.Content)
	}
	digest := f.cfg.Digest(files)
	scenario, ok := f.cfg.Scenarios[digest]
	if !ok {
		scenario = f.defaultScenario()
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, slices.Clone(documents))
	key := atespace + "/" + workspace.Metadata.Name
	if _, exists := f.resources[key]; exists {
		return fmt.Errorf("mockax: task %q already exists", workspace.Metadata.Name)
	}
	f.resources[key] = &resource{files: files, digest: digest, scenario: scenario}
	if f.cfg.ApplyUncertain {
		return errors.New("mockax: create outcome unknown")
	}
	return nil
}

// parseDocuments splits the stream and returns its one Workspace and one Task.
func parseDocuments(atespace string, documents []byte) (document, document, error) {
	var workspace, task *document
	for _, part := range strings.Split(string(documents), "\n---\n") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(part))
		var doc document
		if err := decoder.Decode(&doc); err != nil {
			return document{}, document{}, fmt.Errorf("mockax: document is not JSON: %w", err)
		}
		if doc.APIVersion != apiVersion || doc.Metadata.Atespace != atespace || doc.Metadata.Name == "" {
			return document{}, document{}, errors.New("mockax: document needs apiVersion " + apiVersion + ", a name and the call's atespace")
		}
		switch doc.Kind {
		case "Workspace":
			if workspace != nil {
				return document{}, document{}, errors.New("mockax: more than one Workspace")
			}
			workspace = &doc
		case "Task":
			if task != nil {
				return document{}, document{}, errors.New("mockax: more than one Task")
			}
			task = &doc
		default:
			return document{}, document{}, fmt.Errorf("mockax: unknown kind %q", doc.Kind)
		}
	}
	if workspace == nil || task == nil {
		return document{}, document{}, errors.New("mockax: one Workspace and one Task are required")
	}
	return *workspace, *task, nil
}

// Resume starts the named Task under its Scenario. A Task that does not exist or whose
// Scenario refuses the resume is refused; a refused Task reports failed from then on.
func (f *Fake) Resume(ctx context.Context, atespace, name string) (bool, error) {
	if err := f.admit(ctx, KindResume, callRef{Atespace: atespace, Name: name}); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumed = append(f.resumed, name)
	res, ok := f.resources[atespace+"/"+name]
	if !ok {
		return false, nil
	}
	res.resumed = true
	if res.scenario.ResumeRefused {
		return false, nil
	}
	if f.cfg.ResumeUncertain {
		return false, errors.New("mockax: resume outcome unknown")
	}
	return true, nil
}

// TaskState reports the named Task's state under its Scenario: suspended until it was
// resumed, as AX creates it.
func (f *Fake) TaskState(ctx context.Context, atespace, name string) (string, error) {
	if err := f.admit(ctx, KindState, callRef{Atespace: atespace, Name: name}); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := atespace + "/" + name
	res, ok := f.resources[key]
	switch {
	case !ok:
		return TaskMissing, nil
	case !res.resumed:
		return TaskSuspended, nil
	case res.scenario.ResumeRefused:
		return TaskFailed, nil
	}
	if res.readyReported {
		switch res.scenario.AfterReady {
		case FateStays:
		case FateSuspended:
			return TaskSuspended, nil
		case FateMissing:
			delete(f.resources, key)
			return TaskMissing, nil
		}
	}
	switch res.scenario.Startup {
	case StartupFailed:
		return TaskFailed, nil
	case StartupNeverReady, StartupReadyLater:
		return TaskPending, nil
	case StartupReady:
	}
	res.readyReported = true
	return TaskReady, nil
}

// Delete removes the Task and Workspace of that name, unless deletes are configured to
// fail.
func (f *Fake) Delete(ctx context.Context, atespace, name string) error {
	if err := f.admit(ctx, KindDelete, callRef{Atespace: atespace, Name: name}); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, name)
	if f.cfg.DeleteFails {
		return errors.New("mockax: delete failed")
	}
	delete(f.resources, atespace+"/"+name)
	return nil
}

// List returns the sorted names in atespace that start with prefix.
func (f *Fake) List(ctx context.Context, atespace, prefix string) ([]string, error) {
	if err := f.admit(ctx, KindList, callRef{Atespace: atespace, Prefix: prefix}); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for key := range f.resources {
		space, name, _ := strings.Cut(key, "/")
		if space == atespace && strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names, nil
}
