//go:build integration

package sandboxrunner

import (
	"context"
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

func dockerJob(testID, patch string) Job {
	return Job{TestID: testID, BaseCommit: FixtureBaseCommit, Patch: patch, PatchDigest: PatchDigest(patch)}
}

func TestDockerBackendIntegration_PatchTouchingAFile_Passes(t *testing.T) {
	requireDocker(t)
	backend := NewDockerBackend(DefaultImage, 5*time.Minute, ExecRunner{})

	outcome := backend.Run(context.Background(), dockerJob("itest_pass", touchPatch))
	if !outcome.Verdict || !outcome.Passed || outcome.Total != 1 || outcome.Failed != 0 {
		t.Fatalf("outcome = %+v, want a passing verdict with 1 test", outcome)
	}
}

func TestDockerBackendIntegration_PatchBreakingATest_Fails(t *testing.T) {
	requireDocker(t)
	backend := NewDockerBackend(DefaultImage, 5*time.Minute, ExecRunner{})

	outcome := backend.Run(context.Background(), dockerJob("itest_fail", breakingPatch))
	if !outcome.Verdict || outcome.Passed || outcome.Total != 1 || outcome.Failed != 1 {
		t.Fatalf("outcome = %+v, want a failing verdict with 1 failed test", outcome)
	}
}

func TestDockerBackendIntegration_PatchNotApplying_ReportsPatchRejected(t *testing.T) {
	requireDocker(t)
	backend := NewDockerBackend(DefaultImage, 5*time.Minute, ExecRunner{})

	outcome := backend.Run(context.Background(), dockerJob("itest_reject", strings.ReplaceAll(touchPatch, "returns the greeting", "returns no greeting")))
	if outcome.Verdict || outcome.FailureCode != codePatchRejected {
		t.Fatalf("outcome = %+v, want %s", outcome, codePatchRejected)
	}
}

func TestDockerBackendIntegration_TinyTimeout_TimesOutAndLeavesNoContainer(t *testing.T) {
	requireDocker(t)
	backend := NewDockerBackend(DefaultImage, 300*time.Millisecond, ExecRunner{})

	outcome := backend.Run(context.Background(), dockerJob("itest_timeout", touchPatch))
	if outcome.Verdict || outcome.FailureCode != codeTimeout {
		t.Fatalf("outcome = %+v, want %s", outcome, codeTimeout)
	}
	output, err := exec.Command("docker", "ps", "-a", "--filter", "name=^"+containerPrefix+"itest_timeout$", "--format", "{{.Names}}").Output()
	if err != nil {
		t.Fatalf("docker ps: %v", err)
	}
	if strings.TrimSpace(string(output)) != "" {
		t.Fatalf("container left behind: %s", output)
	}
}
