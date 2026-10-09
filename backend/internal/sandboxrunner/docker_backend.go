package sandboxrunner

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultImage is the only image the docker Backend runs unless the operator configures
// another one. It is pinned by digest so a tag move cannot change what runs.
const DefaultImage = "golang:1.27-alpine@sha256:738d1cf061836894ff6bb8c33881080ac66de8cf0586615012a0c8f592649cfa"

// DefaultTestTimeout bounds one test run.
const DefaultTestTimeout = 120 * time.Second

// FixtureBaseCommit is the only base commit the docker Backend can test: the embedded
// fixture module.
const FixtureBaseCommit = "fixture-v1"

// containerPrefix starts every sandbox container's name. Startup removes any container
// with this prefix that a previous runner process left behind.
const containerPrefix = "emberling-sandbox-"

// Failure codes the docker Backend reports. Each is a fixed string; no container output
// reaches a failure.
const (
	codeUnknownBaseCommit = "UNKNOWN_BASE_COMMIT"
	codePatchRejected     = "PATCH_REJECTED"
	codeTimeout           = "TIMEOUT"
	codeSandboxError      = "SANDBOX_ERROR"
)

// exitUnpackFailed is the container script's exit code when the workspace cannot be
// unpacked; any other non-zero code other than go test's 1 means the sandbox failed.
const exitUnpackFailed = 91

// maxTestOutputBytes bounds the container output the docker Backend reads.
const maxTestOutputBytes = 1 << 20

// cleanupTimeout bounds removing one container after a timeout or a shutdown, which must
// happen even though the run's own context has ended.
const cleanupTimeout = 30 * time.Second

// containerScript unpacks the already-patched workspace streamed on stdin and runs the
// tests. The patch is applied by the runner before the container starts, so the image
// needs no patch tool. The workspace, Go build cache and module path all live on tmpfs
// mounts; the root filesystem is read-only and there is no network.
const containerScript = `cd /work && mkdir -p .gotmp && tar xf - || exit 91
exec go test -json ./... 2>&1`

//go:embed testdata/fixture
var fixtureFS embed.FS

// fixtureRoot is the embedded fixture module's directory.
const fixtureRoot = "testdata/fixture"

// DockerBackend runs each test in a fresh container of one configured image through the
// docker CLI. A request can supply only a base commit and a patch: the image, mounts,
// limits and command are fixed here.
type DockerBackend struct {
	image   string
	timeout time.Duration
	runner  CommandRunner
}

// NewDockerBackend returns a docker Backend running image with the given per-test timeout.
func NewDockerBackend(image string, timeout time.Duration, runner CommandRunner) *DockerBackend {
	return &DockerBackend{image: image, timeout: timeout, runner: runner}
}

// ValidateMock rejects every mock mode: a docker run is never simulated.
func (*DockerBackend) ValidateMock(mock string) error {
	if mock != "" {
		return errors.New("mock is accepted only by the mock backend")
	}
	return nil
}

// Run tests job in a container. An unknown base commit runs nothing.
func (d *DockerBackend) Run(ctx context.Context, job Job) Outcome {
	if job.BaseCommit != FixtureBaseCommit {
		return Outcome{FailureCode: codeUnknownBaseCommit, FailureMessage: "only base commit " + FixtureBaseCommit + " is available"}
	}
	files, err := fixtureFiles()
	if err != nil {
		return Outcome{FailureCode: codeSandboxError, FailureMessage: "fixture could not be read"}
	}
	if err := applyPatch(files, job.Patch); err != nil {
		// A patch that does not apply never reaches a container.
		return Outcome{FailureCode: codePatchRejected, FailureMessage: "patch does not apply to " + FixtureBaseCommit}
	}
	archive, err := workspaceArchive(files)
	if err != nil {
		return Outcome{FailureCode: codeSandboxError, FailureMessage: "workspace archive could not be built"}
	}

	name := containerPrefix + job.TestID
	runCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	output := &boundedBuffer{limit: maxTestOutputBytes}
	exitCode, runErr := d.runner.Docker(runCtx, d.runArgs(name), bytes.NewReader(archive), output)
	if runErr != nil {
		// The docker client was stopped or never ran; the container may still exist.
		d.removeContainer(ctx, name)
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return Outcome{FailureCode: codeTimeout, FailureMessage: "test run exceeded " + d.timeout.String()}
		}
		return Outcome{FailureCode: codeSandboxError, FailureMessage: "sandbox container could not be run"}
	}
	switch exitCode {
	case 0, 1:
		total, failed := countTests(output.data)
		return Outcome{Verdict: true, Passed: exitCode == 0 && failed == 0, Total: total, Failed: failed}
	case exitUnpackFailed:
		return Outcome{FailureCode: codeSandboxError, FailureMessage: "fixture could not be unpacked in the sandbox"}
	default:
		return Outcome{FailureCode: codeSandboxError, FailureMessage: "sandbox exited with status " + strconv.Itoa(exitCode)}
	}
}

// runArgs is the complete, fixed `docker run` argument list for one test.
func (d *DockerBackend) runArgs(name string) []string {
	return []string{
		"run", "-i", "--rm",
		"--network", "none",
		"--user", "65534:65534",
		"--read-only",
		"--tmpfs", "/work:exec,uid=65534,gid=65534",
		"--tmpfs", "/tmp",
		"--memory", "512m",
		"--cpus", "1",
		"--pids-limit", "256",
		"--name", name,
		"-e", "HOME=/tmp",
		"-e", "GOCACHE=/tmp/gocache",
		"-e", "GOPATH=/tmp/gopath",
		"-e", "GOTMPDIR=/work/.gotmp",
		"-e", "GOFLAGS=-mod=readonly",
		"-e", "GOPROXY=off",
		"-e", "GOTOOLCHAIN=local",
		"-e", "GOTELEMETRY=off",
		"-e", "CGO_ENABLED=0",
		d.image,
		"sh", "-c", containerScript,
	}
}

// removeContainer force-removes one sandbox container, ignoring a container that is
// already gone. It runs even after ctx has ended, which is exactly when a timed-out or
// interrupted container is left behind, so it keeps ctx's values but not its cancellation.
func (d *DockerBackend) removeContainer(parent context.Context, name string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), cleanupTimeout)
	defer cancel()
	discard := &boundedBuffer{}
	_, _ = d.runner.Docker(ctx, []string{"kill", name}, nil, discard)
	_, _ = d.runner.Docker(ctx, []string{"rm", "-f", name}, nil, discard)
}

// RemoveOrphans removes every sandbox container a previous runner process left behind and
// returns how many it removed. It is called once at startup, before any test runs.
func (d *DockerBackend) RemoveOrphans(ctx context.Context) (int, error) {
	listing := &boundedBuffer{limit: maxTestOutputBytes}
	exitCode, err := d.runner.Docker(ctx, []string{"ps", "-a", "--filter", "name=^" + containerPrefix, "--format", "{{.Names}}"}, nil, listing)
	if err != nil {
		return 0, fmt.Errorf("sandboxrunner: list sandbox containers: %w", err)
	}
	if exitCode != 0 {
		return 0, fmt.Errorf("sandboxrunner: list sandbox containers: docker exited with status %d", exitCode)
	}
	removed := 0
	for _, name := range strings.Fields(string(listing.data)) {
		if !strings.HasPrefix(name, containerPrefix) {
			continue
		}
		code, err := d.runner.Docker(ctx, []string{"rm", "-f", name}, nil, &boundedBuffer{})
		if err != nil {
			return removed, fmt.Errorf("sandboxrunner: remove sandbox container: %w", err)
		}
		if code == 0 {
			removed++
		}
	}
	return removed, nil
}

// fixtureFiles returns the embedded fixture module's files by their workspace path.
func fixtureFiles() (map[string][]byte, error) {
	files := make(map[string][]byte)
	err := fs.WalkDir(fixtureFS, fixtureRoot, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := fixtureFS.ReadFile(name)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(name, fixtureRoot+"/")
		// go.mod is embedded under another name: a go.mod would make the fixture directory
		// a separate module, which cannot be embedded.
		if path.Base(rel) == "go.mod.txt" {
			rel = path.Join(path.Dir(rel), "go.mod")
		}
		files[rel] = content
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// workspaceArchive returns a tar of files in sorted path order, with the directories
// each path needs.
func workspaceArchive(files map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	dirs := make(map[string]bool)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		for dir := path.Dir(name); dir != "." && !dirs[dir]; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}
	for _, dir := range slices.Sorted(maps.Keys(dirs)) {
		if err := writer.WriteHeader(&tar.Header{Name: dir + "/", Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
			return nil, err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		content := files[name]
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := writer.Write(content); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// testEvent is the part of one `go test -json` line the count needs.
type testEvent struct {
	Action string `json:"Action"`
	Test   string `json:"Test"`
}

// countTests counts the tests in `go test -json` output that passed, failed or were
// skipped, and those that failed. Lines that are not test events are ignored.
func countTests(output []byte) (int, int) {
	total, failed := 0, 0
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64<<10), maxTestOutputBytes)
	for scanner.Scan() {
		var event testEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Test == "" {
			continue
		}
		switch event.Action {
		case "pass", "skip":
			total++
		case "fail":
			total++
			failed++
		}
	}
	return total, failed
}
