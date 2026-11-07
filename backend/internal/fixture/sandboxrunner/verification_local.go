package sandboxrunner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// LocalGoBackend runs verifications with the host's go command in a throwaway checkout,
// against a fixed offline module cache. It is the backend integration tests use where no
// Docker daemon is available, so it applies the same offline rules the container does:
// GOPROXY=off, -mod=readonly, no checksum database, no toolchain download, and a fixed,
// minimal environment that carries nothing from the runner's own. It scans by comparing
// the module list with the snapshot, because govulncheck is not part of the host.
type LocalGoBackend struct {
	repos    *LocalRepositories
	goBinary string
	modCache string
	goCache  string
	snapshot *vulnSnapshot
	timeout  time.Duration
}

// DefaultVerificationTimeout bounds one verification.
const DefaultVerificationTimeout = 5 * time.Minute

// NewLocalGoBackend returns a local backend using goBinary, the module cache at modCache
// and the build cache at goCache, for the registered repositories.
func NewLocalGoBackend(repos *LocalRepositories, goBinary, modCache, goCache string, timeout time.Duration) (*LocalGoBackend, error) {
	snapshot, err := loadVulnSnapshot()
	if err != nil {
		return nil, err
	}
	return &LocalGoBackend{repos: repos, goBinary: goBinary, modCache: modCache, goCache: goCache, snapshot: snapshot, timeout: timeout}, nil
}

// ValidateMock rejects every mock mode: a local run is never simulated.
func (*LocalGoBackend) ValidateMock(mock string) error {
	if mock != "" {
		return errors.New("mock is accepted only by the mock backend")
	}
	return nil
}

// Verify prepares the checkout and runs the three steps in it.
func (b *LocalGoBackend) Verify(ctx context.Context, job VerificationJob) VerificationOutcome {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	c, failure := prepareCheckout(ctx, b.repos, job)
	if failure != nil {
		return *failure
	}
	defer c.remove()

	steps := verificationSteps{Preparation: c.log.Bytes()}
	steps.Modules = b.runStep(ctx, c.dir, stepModules, "list", "-m", "-json", "all")
	if steps.Modules.Err == nil && steps.Modules.ExitCode == 0 {
		steps.Tests = b.runStep(ctx, c.dir, stepTests, "test", "-json", "./...")
		steps.Scan, steps.ScanOutput = snapshotScan(steps.Modules.Output, b.snapshot)
	}
	steps.ModuleFilesChanged = !c.moduleFilesUnchanged()
	outcome := evaluateVerification(steps, b.snapshot)
	if outcome.FailureCode == codeSandboxError && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		outcome.FailureCode, outcome.FailureMessage = codeTimeout, "verification exceeded "+b.timeout.String()
	}
	return outcome
}

// runStep runs one go command in dir with the fixed environment.
func (b *LocalGoBackend) runStep(ctx context.Context, dir, name string, args ...string) stepResult {
	cmd := exec.CommandContext(ctx, b.goBinary, args...) //nolint:gosec // G204: the binary is the configured go command and the arguments are this backend's fixed lists, never request text, and no shell is involved
	cmd.Dir = dir
	cmd.Env = b.environment(dir)
	output := &boundedBuffer{limit: maxStepOutputBytes}
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	result := stepResult{Name: name, Output: output.data}
	if ctx.Err() != nil {
		result.Err = ctx.Err()
		return result
	}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		result.ExitCode = exitErr.ExitCode()
	case err != nil:
		result.Err = err
	}
	return result
}

// environment is the complete environment a step runs with. Nothing of the runner's own
// environment is inherited, so no credential or token of the host can reach a test or its
// output; only the go binary's directory and the system paths are on PATH.
func (b *LocalGoBackend) environment(dir string) []string {
	tmp := filepath.Join(dir, ".gotmp")
	_ = os.MkdirAll(tmp, checkoutDirMode)
	return []string{
		"PATH=" + filepath.Dir(b.goBinary) + ":/usr/bin:/bin",
		"HOME=" + tmp,
		"TMPDIR=" + tmp,
		"GOTMPDIR=" + tmp,
		"GOCACHE=" + b.goCache,
		"GOMODCACHE=" + b.modCache,
		"GOPATH=" + filepath.Join(tmp, "gopath"),
		"GOFLAGS=-mod=readonly",
		"GOPROXY=off",
		"GOSUMDB=off",
		"GONOSUMDB=*",
		"GOTOOLCHAIN=local",
		"GOTELEMETRY=off",
		"CGO_ENABLED=0",
		"GOWORK=off",
	}
}
