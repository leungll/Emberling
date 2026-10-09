//go:build integration

package sandboxrunner

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// These tests drive a real Docker daemon with the pinned default image. They need no
// database; without a reachable daemon they skip with the reason, since the docker backend
// is a local-only, opt-in demo backend.

func requireDocker(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		t.Skipf("docker daemon unavailable (docker info: %v)", err)
	}
	pullCtx, pullCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer pullCancel()
	if output, err := exec.CommandContext(pullCtx, "docker", "pull", "-q", DefaultImage).CombinedOutput(); err != nil {
		t.Fatalf("pull %s: %v: %s", DefaultImage, err, output)
	}
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
