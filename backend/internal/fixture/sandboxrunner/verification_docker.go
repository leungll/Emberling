package sandboxrunner

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"time"
)

// DockerVerifier runs each verification in a fresh container of one configured image
// through the docker CLI, the way the docker Backend runs a test. The image is built by the
// repository's own build script: it holds the go toolchain, govulncheck, the vulnerability
// database snapshot at /vulndb and the offline module cache at /modcache, all pinned, and
// is referenced by digest. The patched checkout is prepared on the host and streamed into
// the container; the container has no network, a read-only root, an unprivileged user
// and memory and process limits, and the go tools run with GOPROXY=off and -mod=readonly,
// so the only modules a patch can build against are the ones in the image.
type DockerVerifier struct {
	repos    *LocalRepositories
	image    string
	timeout  time.Duration
	runner   CommandRunner
	snapshot *vulnSnapshot
}

// verifyContainerPrefix starts every verification container's name. It shares the sandbox
// prefix, so the docker Backend's orphan removal covers verification containers too.
const verifyContainerPrefix = containerPrefix + "verify-"

// verifyContainerScript unpacks the patched checkout streamed on stdin and runs the steps,
// each between sentinel lines carrying its name and exit status, so the runner can split
// one output stream back into steps. The module files are hashed before and after so a
// verification that changed them is detected.
const verifyContainerScript = `cd /work && mkdir -p .gotmp && tar xf - || exit 91
step() { name=$1; shift; printf '\036STEP %s\n' "$name"; "$@" 2>&1; printf '\036EXIT %s\n' "$?"; }
step modfiles-before sha256sum go.mod go.sum
step modules go list -m -json all
step tests go test -json ./...
step scan govulncheck -json ./...
step modfiles-after sha256sum go.mod go.sum
exit 0`

// NewDockerVerifier returns a docker verifier running image with the given per-verification
// timeout for the registered repositories.
func NewDockerVerifier(repos *LocalRepositories, image string, timeout time.Duration, runner CommandRunner) (*DockerVerifier, error) {
	snapshot, err := loadVulnSnapshot()
	if err != nil {
		return nil, err
	}
	return &DockerVerifier{repos: repos, image: image, timeout: timeout, runner: runner, snapshot: snapshot}, nil
}

// ValidateMock rejects every mock mode: a docker run is never simulated.
func (*DockerVerifier) ValidateMock(mock string) error {
	if mock != "" {
		return errors.New("mock is accepted only by the mock backend")
	}
	return nil
}

// Verify prepares the checkout on the host and runs the steps in a container.
func (d *DockerVerifier) Verify(ctx context.Context, job VerificationJob) VerificationOutcome {
	c, failure := prepareCheckout(ctx, d.repos, job)
	if failure != nil {
		return *failure
	}
	defer c.remove()
	archive, err := c.archive()
	if err != nil {
		return VerificationOutcome{FailureCode: codeSandboxError, FailureMessage: "checkout archive could not be built"}
	}

	name := verifyContainerPrefix + job.VerificationID
	lifecycle := &DockerBackend{image: d.image, timeout: d.timeout, runner: d.runner}
	if !lifecycle.createContainerWith(ctx, name, d.createArgs(name)) {
		return VerificationOutcome{FailureCode: codeSandboxError, FailureMessage: "sandbox container could not be created"}
	}
	runCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	output := &boundedBuffer{limit: maxStepOutputBytes}
	exitCode, runErr := d.runner.Docker(runCtx, startArgs(name), bytes.NewReader(archive), output)
	if runErr != nil {
		lifecycle.removeContainer(ctx, name)
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return VerificationOutcome{FailureCode: codeTimeout, FailureMessage: "verification exceeded " + d.timeout.String(), Log: output.data}
		}
		return VerificationOutcome{FailureCode: codeSandboxError, FailureMessage: "sandbox container could not be run"}
	}
	switch exitCode {
	case 0:
		named, preamble := splitSentinelSteps(output.data)
		return evaluateVerification(stepsFromScript(named, preamble, c.log.Bytes()), d.snapshot)
	case exitUnpackFailed:
		return VerificationOutcome{FailureCode: codeSandboxError, FailureMessage: "checkout could not be unpacked in the sandbox"}
	default:
		return VerificationOutcome{FailureCode: codeSandboxError, FailureMessage: "sandbox exited with status " + strconv.Itoa(exitCode), Log: output.data}
	}
}

// createArgs is the complete, fixed `docker create` argument list for one verification.
// The module cache and vulnerability database are part of the image, read-only like the
// rest of the root filesystem.
func (d *DockerVerifier) createArgs(name string) []string {
	return []string{
		"create", "-i", "--rm",
		"--network", "none",
		"--user", "65534:65534",
		"--read-only",
		"--tmpfs", "/work:exec,uid=65534,gid=65534,size=512m",
		"--tmpfs", "/tmp:size=1g",
		"--memory", "1g",
		"--cpus", "1",
		"--pids-limit", "256",
		"--name", name,
		"-e", "HOME=/tmp",
		"-e", "GOCACHE=/tmp/gocache",
		"-e", "GOPATH=/tmp/gopath",
		"-e", "GOTMPDIR=/work/.gotmp",
		"-e", "GOMODCACHE=/modcache",
		"-e", "GOVULNDB=file:///vulndb",
		"-e", "GOFLAGS=-mod=readonly",
		"-e", "GOPROXY=off",
		"-e", "GOSUMDB=off",
		"-e", "GOTOOLCHAIN=local",
		"-e", "GOTELEMETRY=off",
		"-e", "CGO_ENABLED=0",
		d.image,
		"sh", "-c", verifyContainerScript,
	}
}
