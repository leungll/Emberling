package sandboxrunner

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scriptOutput renders the sentinel-separated output the verification container script
// writes for the given steps.
func scriptOutput(steps map[string]string, exits map[string]int) string {
	var out strings.Builder
	for _, name := range []string{scriptStepModfilesPre, scriptStepModules, scriptStepTests, scriptStepScan, scriptStepModfilesPost} {
		out.WriteString(sentinelStep + name + "\n" + steps[name] + sentinelExit + strconv.Itoa(exits[name]) + "\n")
	}
	return out.String()
}

func TestDockerVerifier_Verify_UsesOnlyTheFixedArgumentsAndStreamsTheCheckout(t *testing.T) {
	f := vulnRepo(t)
	hashes := "aaa  go.mod\nbbb  go.sum\n"
	docker := &fakeDocker{respond: func(_ context.Context, args []string, output io.Writer) (int, error) {
		if args[0] != "start" {
			return 0, nil
		}
		_, _ = io.WriteString(output, scriptOutput(map[string]string{
			scriptStepModfilesPre: hashes, scriptStepModules: modulesClean, scriptStepTests: testsAllPass,
			scriptStepScan: `{"config":{}}` + "\n", scriptStepModfilesPost: hashes,
		}, nil))
		return 0, nil
	}}
	repos := NewLocalRepositories(map[string]string{vulnRepoID: f.repoDir})
	verifier, err := NewDockerVerifier(repos, "verify@sha256:abc", time.Minute, docker)
	if err != nil {
		t.Fatalf("NewDockerVerifier: %v", err)
	}
	outcome := verifier.Verify(context.Background(), vulnJob("verify_1", fixturePatch(t, "fix-complete.patch")))
	if !outcome.Verdict || !outcome.Passed() || outcome.Scanner != scannerGovulncheck || outcome.Tests.Total != 2 {
		t.Fatalf("outcome = %+v\n%s", outcome, outcome.Log)
	}

	calls := docker.commands()
	if len(calls) != 2 || calls[1] != "start -a -i emberling-sandbox-verify-verify_1" {
		t.Fatalf("docker calls = %v, want exactly the create then the start", calls)
	}
	create := calls[0]
	for _, required := range []string{
		"create -i --rm --network none --user 65534:65534 --read-only",
		"--tmpfs /work:exec,uid=65534,gid=65534,size=512m --tmpfs /tmp:size=1g",
		"--memory 1g --cpus 1 --pids-limit 256",
		"--name emberling-sandbox-verify-verify_1",
		"-e GOPROXY=off", "-e GOFLAGS=-mod=readonly", "-e GOMODCACHE=/modcache", "-e GOSUMDB=off",
		" verify@sha256:abc sh -c ",
	} {
		if !strings.Contains(create, required) {
			t.Fatalf("docker create %q lacks %q", create, required)
		}
	}
	for _, forbidden := range []string{"-v ", "--volume", "--mount", "--privileged", "docker.sock", f.repoDir} {
		if strings.Contains(create, forbidden) {
			t.Fatalf("docker create %q contains %q", create, forbidden)
		}
	}

	files := map[string]string{}
	reader := tar.NewReader(bytes.NewReader(docker.stdin))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		content, _ := io.ReadAll(reader)
		files[header.Name] = string(content)
	}
	if !strings.Contains(files["go.mod"], "github.com/golang-jwt/jwt/v5 v5.3.1") || !strings.Contains(files["internal/auth/token.go"], "jwt.RegisteredClaims") || files["tests/auth_test.go"] == "" {
		t.Fatalf("archive lacks the patched checkout: %v", keysOf(files))
	}
	if _, hasGit := files[".git/HEAD"]; hasGit {
		t.Fatal("archive carries the repository's .git directory")
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestDockerVerifier_ModuleFilesChangedInContainer_NoVerdict(t *testing.T) {
	f := vulnRepo(t)
	docker := &fakeDocker{respond: func(_ context.Context, args []string, output io.Writer) (int, error) {
		if args[0] != "start" {
			return 0, nil
		}
		_, _ = io.WriteString(output, scriptOutput(map[string]string{
			scriptStepModfilesPre: "aaa  go.mod\nbbb  go.sum\n", scriptStepModules: modulesClean, scriptStepTests: testsAllPass,
			scriptStepScan: `{"config":{}}` + "\n", scriptStepModfilesPost: "aaa  go.mod\nccc  go.sum\n",
		}, nil))
		return 0, nil
	}}
	verifier, err := NewDockerVerifier(NewLocalRepositories(map[string]string{vulnRepoID: f.repoDir}), "verify@sha256:abc", time.Minute, docker)
	if err != nil {
		t.Fatalf("NewDockerVerifier: %v", err)
	}
	outcome := verifier.Verify(context.Background(), vulnJob("verify_2", ""))
	if outcome.Verdict || outcome.FailureCode != codeSandboxError || !strings.Contains(outcome.FailureMessage, "go.mod or go.sum changed") {
		t.Fatalf("outcome = %+v, want no verdict", outcome)
	}
}

func TestDockerVerifier_Timeout_RemovesContainerAndReportsTimeout(t *testing.T) {
	f := vulnRepo(t)
	docker := &fakeDocker{respond: func(ctx context.Context, args []string, _ io.Writer) (int, error) {
		if args[0] == "start" {
			<-ctx.Done()
			return -1, ctx.Err()
		}
		return 0, nil
	}}
	verifier, err := NewDockerVerifier(NewLocalRepositories(map[string]string{vulnRepoID: f.repoDir}), "verify@sha256:abc", 20*time.Millisecond, docker)
	if err != nil {
		t.Fatalf("NewDockerVerifier: %v", err)
	}
	outcome := verifier.Verify(context.Background(), vulnJob("verify_3", ""))
	if outcome.Verdict || outcome.FailureCode != codeTimeout {
		t.Fatalf("outcome = %+v, want %s", outcome, codeTimeout)
	}
	calls := docker.commands()
	if len(calls) < 4 || calls[2] != "kill emberling-sandbox-verify-verify_3" || calls[3] != "rm -f emberling-sandbox-verify-verify_3" {
		t.Fatalf("docker calls = %v, want kill and rm after the timed-out start", calls)
	}
}

func TestDockerVerifier_ScanCommand_ReadsTheOfflineSnapshot(t *testing.T) {
	f := vulnRepo(t)
	docker := &fakeDocker{respond: func(_ context.Context, _ []string, _ io.Writer) (int, error) { return 0, nil }}
	verifier, err := NewDockerVerifier(NewLocalRepositories(map[string]string{vulnRepoID: f.repoDir}), "verify@sha256:abc", time.Minute, docker)
	if err != nil {
		t.Fatalf("NewDockerVerifier: %v", err)
	}
	verifier.Verify(context.Background(), vulnJob("verify_3", ""))
	calls := docker.commands()
	if len(calls) == 0 {
		t.Fatal("docker was not called")
	}
	// govulncheck takes its database only from -db; a scan without it would try to reach
	// the public database, which the container's missing network makes impossible.
	if !strings.Contains(calls[0], "step scan govulncheck -db file:///vulndb -json ./...") {
		t.Fatalf("docker create %q does not run govulncheck against the offline snapshot", calls[0])
	}
}

func TestDockerVerifier_ScanCannotReachItsDatabase_VerdictWithoutPass(t *testing.T) {
	f := vulnRepo(t)
	hashes := "aaa  go.mod\nbbb  go.sum\n"
	docker := &fakeDocker{respond: func(_ context.Context, args []string, output io.Writer) (int, error) {
		if args[0] != "start" {
			return 0, nil
		}
		_, _ = io.WriteString(output, scriptOutput(map[string]string{
			scriptStepModfilesPre: hashes, scriptStepModules: modulesClean, scriptStepTests: testsAllPass,
			scriptStepScan:         "govulncheck: fetching vulnerabilities: Get \"https://vuln.go.dev/index/modules.json.gz\": dial tcp: connect: network is unreachable\n",
			scriptStepModfilesPost: hashes,
		}, map[string]int{scriptStepScan: 1}))
		return 0, nil
	}}
	verifier, err := NewDockerVerifier(NewLocalRepositories(map[string]string{vulnRepoID: f.repoDir}), "verify@sha256:abc", time.Minute, docker)
	if err != nil {
		t.Fatalf("NewDockerVerifier: %v", err)
	}
	outcome := verifier.Verify(context.Background(), vulnJob("verify_4", fixturePatch(t, "fix-complete.patch")))
	if !outcome.Verdict || outcome.Passed() || outcome.ScanRan || outcome.ScanClean || !outcome.TestsPassed || !outcome.DependenciesClean {
		t.Fatalf("outcome = %+v, want a verdict that does not pass because only the scan is missing", outcome)
	}
	if !strings.Contains(string(outcome.Log), "network is unreachable") || !strings.Contains(string(outcome.Log), "govulncheck did not run to a result") {
		t.Fatalf("log does not carry the scanner's output and the reason:\n%s", outcome.Log)
	}
}
