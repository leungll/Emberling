package sandboxrunner

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests run the local backend with the host's go command against the generated
// vulnerability-fix example repository and an offline module cache that holds exactly the
// two module versions the example's mirror serves. Both are built once per test binary.

const (
	vulnRepoID       = "vuln-demo"
	vulnRepoBaseline = "2706ca6fb4ddbe969e9acfa5464a347ff90c7774"
)

// mirrorModules are the only module versions the example repository's mirror serves.
var mirrorModules = []string{"github.com/golang-jwt/jwt@v3.2.2+incompatible", "github.com/golang-jwt/jwt/v5@v5.3.1"}

type vulnRepoFixture struct {
	repoDir  string
	modCache string
	goCache  string
	goBinary string
	skip     string
}

var (
	vulnRepoOnce     sync.Once
	vulnRepoFixtures vulnRepoFixture
)

// vulnRepo generates the example repository and the offline module cache once, and skips
// the test when the host cannot build them: without git, without go, or without a way to
// fetch the two modules into the cache.
func vulnRepo(t *testing.T) vulnRepoFixture {
	t.Helper()
	vulnRepoOnce.Do(buildVulnRepoFixture)
	if vulnRepoFixtures.skip != "" {
		t.Skip(vulnRepoFixtures.skip)
	}
	return vulnRepoFixtures
}

func buildVulnRepoFixture() {
	f := &vulnRepoFixtures
	var err error
	if f.goBinary, err = exec.LookPath("go"); err != nil {
		f.skip = "go command not on PATH: " + err.Error()
		return
	}
	if _, err := exec.LookPath("git"); err != nil {
		f.skip = "git not on PATH: " + err.Error()
		return
	}
	root, err := os.MkdirTemp("", "emberling-vulnrepo-")
	if err != nil {
		f.skip = "temporary directory: " + err.Error()
		return
	}
	f.repoDir = filepath.Join(root, "repo")
	f.modCache = filepath.Join(root, "modcache")
	f.goCache = filepath.Join(root, "gocache")
	script := filepath.Join("..", "vulnrepo", "generate.sh")
	if out, err := exec.Command("sh", script, f.repoDir).CombinedOutput(); err != nil {
		f.skip = "generate example repository: " + err.Error() + ": " + string(out)
		return
	}
	// The cache is filled from a scratch copy of the module, never from a checkout under
	// test, so no test sees a go.sum the download rewrote. The host's own module cache is
	// consulted first as a file proxy; the public proxy only when it lacks a version.
	scratch := filepath.Join(root, "download")
	if out, err := exec.Command("git", "-C", f.repoDir, "worktree", "add", "--detach", scratch, vulnRepoBaseline).CombinedOutput(); err != nil {
		f.skip = "export module for download: " + err.Error() + ": " + string(out)
		return
	}
	hostCache, _ := exec.Command(f.goBinary, "env", "GOMODCACHE").Output()
	proxy := "https://proxy.golang.org,direct"
	if host := strings.TrimSpace(string(hostCache)); host != "" {
		proxy = "file://" + filepath.ToSlash(filepath.Join(host, "cache", "download")) + "," + proxy
	}
	download := exec.Command(f.goBinary, append([]string{"mod", "download"}, mirrorModules...)...)
	download.Dir = scratch
	download.Env = append(os.Environ(), "GOMODCACHE="+f.modCache, "GOFLAGS=-mod=mod", "GOPROXY="+proxy, "GOSUMDB=off", "GOTOOLCHAIN=local")
	if out, err := download.CombinedOutput(); err != nil {
		f.skip = "offline module cache could not be filled (network to the module proxy is needed once): " + err.Error() + ": " + string(out)
		return
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	if vulnRepoFixtures.repoDir != "" {
		_ = os.RemoveAll(filepath.Dir(vulnRepoFixtures.repoDir))
	}
	os.Exit(code)
}

func (f vulnRepoFixture) backend(t *testing.T) *LocalGoBackend {
	t.Helper()
	repos := NewLocalRepositories(map[string]string{vulnRepoID: f.repoDir})
	backend, err := NewLocalGoBackend(repos, f.goBinary, f.modCache, f.goCache, DefaultVerificationTimeout)
	if err != nil {
		t.Fatalf("NewLocalGoBackend: %v", err)
	}
	return backend
}

// fixturePatch reads one of the example repository's testdata patches.
func fixturePatch(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "vulnrepo", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture patch: %v", err)
	}
	return string(content)
}

func vulnJob(id, patch string) VerificationJob {
	return VerificationJob{VerificationID: id, RepositoryID: vulnRepoID, BaseCommit: vulnRepoBaseline, Patch: patch, PatchDigest: WorkspacePatchDigest(patch)}
}

func TestVulnRepo_Baseline_TestsPass(t *testing.T) {
	f := vulnRepo(t)
	outcome := f.backend(t).Verify(context.Background(), vulnJob("verify_baseline", ""))
	if !outcome.Verdict || !outcome.TestsPassed || outcome.Tests.Failed != 0 || outcome.Tests.Total < 9 {
		t.Fatalf("outcome = %+v\n%s", outcome, outcome.Log)
	}
	// The baseline is what the demo starts from: its tests pass while the dependency is
	// still vulnerable, so passed must be false.
	if outcome.DependenciesClean || outcome.ScanClean || outcome.Passed() {
		t.Fatalf("baseline outcome = %+v, want vulnerable dependencies and scan", outcome)
	}
	if len(outcome.VulnerableDependencies) != 1 || outcome.VulnerableDependencies[0] != "github.com/golang-jwt/jwt@v3.2.2+incompatible (GO-2025-3553)" {
		t.Fatalf("vulnerable dependencies = %v", outcome.VulnerableDependencies)
	}
}

func TestVulnRepo_UpgradeWithoutAdaptation_TestsFail(t *testing.T) {
	f := vulnRepo(t)
	outcome := f.backend(t).Verify(context.Background(), vulnJob("verify_noadapt", fixturePatch(t, "upgrade-without-adaptation.patch")))
	if !outcome.Verdict || outcome.TestsPassed || outcome.Passed() {
		t.Fatalf("outcome = %+v, want a verdict with failing tests", outcome)
	}
	if !outcome.DependenciesClean {
		t.Fatalf("outcome = %+v, want clean dependencies: go.mod moved to /v5", outcome)
	}
	if !strings.Contains(string(outcome.Log), "undefined: jwt.StandardClaims") {
		t.Fatalf("log lacks the compile error:\n%s", outcome.Log)
	}
}

func TestVulnRepo_CompleteFix_PassesAllChecks(t *testing.T) {
	f := vulnRepo(t)
	outcome := f.backend(t).Verify(context.Background(), vulnJob("verify_complete", fixturePatch(t, "fix-complete.patch")))
	if !outcome.Verdict || !outcome.Passed() {
		t.Fatalf("outcome = %+v\n%s", outcome, outcome.Log)
	}
	if outcome.Tests.Total < 9 || outcome.Tests.Failed != 0 || len(outcome.ScanFindings) != 0 || len(outcome.VulnerableDependencies) != 0 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.Scanner != scannerSnapshot || !strings.HasPrefix(outcome.SnapshotDigest, "sha256:") {
		t.Fatalf("scanner = %s snapshot = %s", outcome.Scanner, outcome.SnapshotDigest)
	}
}

// go.sum already lists the fixed version, so a patch that edits go.mod alone resolves the
// module graph offline; only the build then fails, because the code still imports the old
// path.
func TestLocalGoBackend_GoModOnlyPatch_PassesModuleVerification(t *testing.T) {
	f := vulnRepo(t)
	outcome := f.backend(t).Verify(context.Background(), vulnJob("verify_gomod", fixturePatch(t, "fix-gomod-only.patch")))
	if !outcome.Verdict || outcome.FailureCode != "" {
		t.Fatalf("outcome = %+v, want a verdict: module verification must pass\n%s", outcome, outcome.Log)
	}
	if outcome.TestsPassed || !outcome.DependenciesClean {
		t.Fatalf("outcome = %+v, want failing tests with clean dependencies", outcome)
	}
	if !strings.Contains(string(outcome.Log), "cannot find module providing package github.com/golang-jwt/jwt") {
		t.Fatalf("log lacks the build failure:\n%s", outcome.Log)
	}
}

func TestLocalGoBackend_ModuleOutsideCache_FailsWithModuleUnavailable(t *testing.T) {
	f := vulnRepo(t)
	outcome := f.backend(t).Verify(context.Background(), vulnJob("verify_v4", fixturePatch(t, "fix-v4.patch")))
	if outcome.Verdict || outcome.FailureCode != codeModuleUnavailable {
		t.Fatalf("outcome = %+v, want %s\n%s", outcome, codeModuleUnavailable, outcome.Log)
	}
	if !strings.Contains(outcome.FailureMessage, "github.com/golang-jwt/jwt/v4@v4.5.2") {
		t.Fatalf("message = %q, want it to name the missing module", outcome.FailureMessage)
	}
}

func TestLocalGoBackend_KeepOldRequire_DependenciesCleanFalse(t *testing.T) {
	f := vulnRepo(t)
	outcome := f.backend(t).Verify(context.Background(), vulnJob("verify_keep", fixturePatch(t, "keep-old-require.patch")))
	if !outcome.Verdict || !outcome.TestsPassed || outcome.DependenciesClean || outcome.Passed() {
		t.Fatalf("outcome = %+v, want passing tests with the vulnerable version still required\n%s", outcome, outcome.Log)
	}
}

// Nothing of the runner's environment reaches a verification: a credential-shaped variable
// set on the runner process is absent from the log even when the tests print their
// environment.
func TestLocalGoBackend_EnvironmentSecrets_AbsentFromLog(t *testing.T) {
	f := vulnRepo(t)
	const planted = "planted-secret-value-7f3a9c"
	t.Setenv("EMBERLING_CALLBACK_TOKEN", planted)
	t.Setenv("AWS_SECRET_ACCESS_KEY", planted)
	t.Setenv("OPENROUTER_API_KEY", planted)
	envDump := fixturePatch(t, "fix-complete.patch") + `diff --git a/tests/env_test.go b/tests/env_test.go
new file mode 100644
--- /dev/null
+++ b/tests/env_test.go
@@ -0,0 +1,12 @@
+package tests
+
+import (
+	"os"
+	"testing"
+)
+
+func TestEnvironment_IsPrinted(t *testing.T) {
+	for _, kv := range os.Environ() {
+		t.Log(kv)
+	}
+}
`
	outcome := f.backend(t).Verify(context.Background(), vulnJob("verify_env", envDump))
	if !outcome.Verdict || !outcome.TestsPassed {
		t.Fatalf("outcome = %+v\n%s", outcome, outcome.Log)
	}
	if !strings.Contains(string(outcome.Log), "GOPROXY=off") {
		t.Fatalf("log does not show the printed environment:\n%s", outcome.Log)
	}
	for _, name := range []string{planted, "EMBERLING_CALLBACK_TOKEN", "AWS_SECRET_ACCESS_KEY", "OPENROUTER_API_KEY"} {
		if bytes.Contains(outcome.Log, []byte(name)) {
			t.Fatalf("log contains %q", name)
		}
	}
}

func TestLocalGoBackend_UnknownRepositoryOrCommit_RunsNothing(t *testing.T) {
	f := vulnRepo(t)
	backend := f.backend(t)
	job := vulnJob("verify_norepo", "")
	job.RepositoryID = "other"
	if outcome := backend.Verify(context.Background(), job); outcome.Verdict || outcome.FailureCode != codeUnknownRepository {
		t.Fatalf("outcome = %+v, want %s", outcome, codeUnknownRepository)
	}
	job = vulnJob("verify_nocommit", "")
	job.BaseCommit = "0000000000000000000000000000000000000000"
	if outcome := backend.Verify(context.Background(), job); outcome.Verdict || outcome.FailureCode != codeUnknownBaseCommit {
		t.Fatalf("outcome = %+v, want %s", outcome, codeUnknownBaseCommit)
	}
	job = vulnJob("verify_badpatch", "--- a/go.mod\n+++ b/go.mod\n@@ -1 +1 @@\n-nonsense\n+other\n")
	if outcome := backend.Verify(context.Background(), job); outcome.Verdict || outcome.FailureCode != codePatchRejected {
		t.Fatalf("outcome = %+v, want %s", outcome, codePatchRejected)
	}
}

func TestLocalGoBackend_Timeout_ReportsTimeout(t *testing.T) {
	f := vulnRepo(t)
	repos := NewLocalRepositories(map[string]string{vulnRepoID: f.repoDir})
	backend, err := NewLocalGoBackend(repos, f.goBinary, f.modCache, f.goCache, time.Nanosecond)
	if err != nil {
		t.Fatalf("NewLocalGoBackend: %v", err)
	}
	outcome := backend.Verify(context.Background(), vulnJob("verify_timeout", ""))
	if outcome.Verdict || (outcome.FailureCode != codeTimeout && outcome.FailureCode != codeSandboxError) {
		t.Fatalf("outcome = %+v, want a timeout", outcome)
	}
}
