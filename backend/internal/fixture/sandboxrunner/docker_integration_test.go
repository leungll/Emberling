//go:build integration

package sandboxrunner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests drive a real Docker daemon with the pinned default image. They need no
// database; without a reachable daemon they skip with the reason, since the docker backend
// is a local-only, opt-in demo backend.

// dockerUnavailable caches the probe result so a missing image costs one bounded pull per
// test binary rather than one per test.
var (
	dockerProbeOnce   sync.Once
	dockerUnavailable string
)

func requireDocker(t *testing.T) {
	t.Helper()
	dockerProbeOnce.Do(func() { dockerUnavailable = probeDocker() })
	if dockerUnavailable != "" {
		t.Skip(dockerUnavailable)
	}
}

// probeDocker returns an empty string when the daemon is reachable and the pinned image is
// available, and otherwise the reason the Docker backend tests must skip.
func probeDocker() string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		return fmt.Sprintf("docker daemon unavailable (docker info: %v)", err)
	}
	inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer inspectCancel()
	if exec.CommandContext(inspectCtx, "docker", "image", "inspect", DefaultImage).Run() == nil {
		return ""
	}
	// The pull is bounded so an unreachable registry skips the tests instead of hanging
	// until the whole test binary times out. WaitDelay is needed as well: a credential
	// helper started by the docker CLI can outlive the killed CLI and keep the output pipe
	// open, which would otherwise block the wait indefinitely.
	pullCtx, pullCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer pullCancel()
	pull := exec.CommandContext(pullCtx, "docker", "pull", "-q", DefaultImage)
	pull.WaitDelay = 5 * time.Second
	if output, err := pull.CombinedOutput(); err != nil {
		if pullCtx.Err() != nil {
			return fmt.Sprintf("sandbox image %s is not present locally and pulling it did not finish within 60s", DefaultImage)
		}
		return fmt.Sprintf("sandbox image %s is not present locally and cannot be pulled: %v: %s", DefaultImage, err, strings.TrimSpace(string(output)))
	}
	return ""
}

// runSuffix is generated once per test binary so concurrent runs of this package from
// other checkouts against the same daemon never reuse a container name; docker run --name
// rejects a name that is already taken.
var runSuffix = func() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("generate container name suffix: %v", err))
	}
	return hex.EncodeToString(b[:])
}()

// dockerTestID returns a test ID unique to this test binary. The backend names the
// container after the test ID under the shared sandbox prefix.
func dockerTestID(base string) string {
	return base + "_" + runSuffix
}

func dockerJob(testID, patch string) Job {
	return Job{TestID: testID, BaseCommit: FixtureBaseCommit, Patch: patch, PatchDigest: PatchDigest(patch)}
}

func TestDockerBackendIntegration_PatchTouchingAFile_Passes(t *testing.T) {
	requireDocker(t)
	backend := NewDockerBackend(DefaultImage, 5*time.Minute, ExecRunner{})

	outcome := backend.Run(context.Background(), dockerJob(dockerTestID("itest_pass"), touchPatch))
	if !outcome.Verdict || !outcome.Passed || outcome.Total != 1 || outcome.Failed != 0 {
		t.Fatalf("outcome = %+v, want a passing verdict with 1 test", outcome)
	}
}

func TestDockerBackendIntegration_PatchBreakingATest_Fails(t *testing.T) {
	requireDocker(t)
	backend := NewDockerBackend(DefaultImage, 5*time.Minute, ExecRunner{})

	outcome := backend.Run(context.Background(), dockerJob(dockerTestID("itest_fail"), breakingPatch))
	if !outcome.Verdict || outcome.Passed || outcome.Total != 1 || outcome.Failed != 1 {
		t.Fatalf("outcome = %+v, want a failing verdict with 1 failed test", outcome)
	}
}

func TestDockerBackendIntegration_PatchNotApplying_ReportsPatchRejected(t *testing.T) {
	requireDocker(t)
	backend := NewDockerBackend(DefaultImage, 5*time.Minute, ExecRunner{})

	outcome := backend.Run(context.Background(), dockerJob(dockerTestID("itest_reject"), strings.ReplaceAll(touchPatch, "returns the greeting", "returns no greeting")))
	if outcome.Verdict || outcome.FailureCode != codePatchRejected {
		t.Fatalf("outcome = %+v, want %s", outcome, codePatchRejected)
	}
}

func TestDockerBackendIntegration_TinyTimeout_TimesOutAndLeavesNoContainer(t *testing.T) {
	requireDocker(t)
	backend := NewDockerBackend(DefaultImage, 300*time.Millisecond, ExecRunner{})

	testID := dockerTestID("itest_timeout")
	outcome := backend.Run(context.Background(), dockerJob(testID, touchPatch))
	if outcome.Verdict || outcome.FailureCode != codeTimeout {
		t.Fatalf("outcome = %+v, want %s", outcome, codeTimeout)
	}
	output, err := exec.Command("docker", "ps", "-a", "--filter", "name=^"+containerPrefix+testID+"$", "--format", "{{.Names}}").Output()
	if err != nil {
		t.Fatalf("docker ps: %v", err)
	}
	if strings.TrimSpace(string(output)) != "" {
		t.Fatalf("container left behind: %s", output)
	}
}

// verifyImage names the verification image, built by deploy/sandboxrunner-verify/build.sh
// and pinned by digest, that the verification integration test runs. It is a test flag
// because the image is built locally and never published; without it the test skips.
var verifyImage = flag.String("verify-image", "", "verification image reference pinned by digest (name@sha256:...)")

func requireVerifyImage(t *testing.T) string {
	t.Helper()
	requireDocker(t)
	if *verifyImage == "" {
		t.Skip("no verification image: pass -verify-image=<name@sha256:...> built by deploy/sandboxrunner-verify/build.sh")
	}
	if !strings.Contains(*verifyImage, "@sha256:") {
		t.Fatalf("-verify-image %q is not pinned by digest", *verifyImage)
	}
	return *verifyImage
}

// A test that only passes when it can open a network connection must fail inside the
// verification container, which has no network.
func TestDockerVerification_NetworkAccess_TestFails(t *testing.T) {
	image := requireVerifyImage(t)
	f := vulnRepo(t)
	verifier, err := NewDockerVerifier(NewLocalRepositories(map[string]string{vulnRepoID: f.repoDir}), image, 5*time.Minute, ExecRunner{})
	if err != nil {
		t.Fatalf("NewDockerVerifier: %v", err)
	}
	networkTest := fixturePatch(t, "fix-complete.patch") + `diff --git a/tests/network_test.go b/tests/network_test.go
new file mode 100644
--- /dev/null
+++ b/tests/network_test.go
@@ -0,0 +1,14 @@
+package tests
+
+import (
+	"net"
+	"testing"
+	"time"
+)
+
+func TestNetwork_IsReachable(t *testing.T) {
+	conn, err := net.DialTimeout("tcp", "1.1.1.1:80", 3*time.Second)
+	if err != nil {
+		t.Fatalf("dial: %v", err)
+	}
+	_ = conn.Close()
+}
`
	outcome := verifier.Verify(context.Background(), vulnJob(dockerTestID("itest_network"), networkTest))
	if !outcome.Verdict || outcome.TestsPassed || outcome.Tests.Failed != 1 {
		t.Fatalf("outcome = %+v, want a verdict with exactly the network test failing\n%s", outcome, outcome.Log)
	}
	if !outcome.ScanClean || !outcome.DependenciesClean || outcome.Scanner != scannerGovulncheck {
		t.Fatalf("outcome = %+v, want a clean govulncheck scan and clean dependencies", outcome)
	}
}

// The complete fix passes every check in the container, with govulncheck reading the
// pinned snapshot and the module cache baked into the image.
func TestDockerVerification_CompleteFix_PassesAllChecks(t *testing.T) {
	image := requireVerifyImage(t)
	f := vulnRepo(t)
	verifier, err := NewDockerVerifier(NewLocalRepositories(map[string]string{vulnRepoID: f.repoDir}), image, 5*time.Minute, ExecRunner{})
	if err != nil {
		t.Fatalf("NewDockerVerifier: %v", err)
	}
	outcome := verifier.Verify(context.Background(), vulnJob(dockerTestID("itest_complete"), fixturePatch(t, "fix-complete.patch")))
	if !outcome.Verdict || !outcome.Passed() || outcome.Scanner != scannerGovulncheck {
		t.Fatalf("outcome = %+v\n%s", outcome, outcome.Log)
	}
	baseline := verifier.Verify(context.Background(), vulnJob(dockerTestID("itest_baseline"), ""))
	if !baseline.Verdict || !baseline.TestsPassed || baseline.ScanClean || baseline.DependenciesClean {
		t.Fatalf("baseline outcome = %+v, want passing tests with a vulnerable scan and dependency list\n%s", baseline, baseline.Log)
	}
}
