package sandboxrunner

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDocker stands in for the docker CLI. respond decides each call's exit code and
// output; every call is kept.
type fakeDocker struct {
	mu      sync.Mutex
	calls   [][]string
	stdin   []byte
	respond func(ctx context.Context, args []string, output io.Writer) (int, error)
}

func (f *fakeDocker) Docker(ctx context.Context, args []string, stdin io.Reader, output io.Writer) (int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, slices.Clone(args))
	if stdin != nil && args[0] == "start" {
		f.stdin, _ = io.ReadAll(stdin)
	}
	respond := f.respond
	f.mu.Unlock()
	if respond == nil {
		return 0, nil
	}
	return respond(ctx, args, output)
}

func (f *fakeDocker) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, call := range f.calls {
		out = append(out, strings.Join(call, " "))
	}
	return out
}

func fixtureJob() Job {
	return Job{TestID: "test_1", BaseCommit: FixtureBaseCommit, Patch: touchPatch, PatchDigest: PatchDigest(touchPatch)}
}

const goTestJSON = `{"Action":"start","Package":"example.com/fixture"}
{"Action":"run","Package":"example.com/fixture","Test":"TestA"}
{"Action":"pass","Package":"example.com/fixture","Test":"TestA"}
{"Action":"fail","Package":"example.com/fixture","Test":"TestB"}
{"Action":"skip","Package":"example.com/fixture","Test":"TestC"}
not json
{"Action":"fail","Package":"example.com/fixture"}
`

func TestDockerBackend_Run_UsesOnlyTheFixedArguments(t *testing.T) {
	docker := &fakeDocker{respond: func(_ context.Context, args []string, output io.Writer) (int, error) {
		if args[0] != "start" {
			return 0, nil
		}
		_, _ = io.WriteString(output, goTestJSON)
		return 1, nil
	}}
	outcome := NewDockerBackend("img@sha256:abc", time.Minute, docker).Run(context.Background(), fixtureJob())

	if !outcome.Verdict || outcome.Passed || outcome.Total != 3 || outcome.Failed != 1 {
		t.Fatalf("outcome = %+v, want a failing verdict with 3 tests and 1 failure", outcome)
	}
	calls := docker.commands()
	if len(calls) != 2 || calls[1] != "start -a -i emberling-sandbox-test_1" {
		t.Fatalf("docker calls = %v, want exactly the create then the start", calls)
	}
	create := calls[0]
	for _, required := range []string{
		"create -i --rm --network none --user 65534:65534 --read-only",
		"--tmpfs /work:exec,uid=65534,gid=65534 --tmpfs /tmp",
		"--memory 512m --cpus 1 --pids-limit 256",
		"--name emberling-sandbox-test_1",
		"-e GOPROXY=off", "-e GOCACHE=/tmp/gocache", "-e GOPATH=/tmp/gopath",
		" img@sha256:abc sh -c ",
	} {
		if !strings.Contains(create, required) {
			t.Fatalf("docker create %q lacks %q", create, required)
		}
	}
	for _, forbidden := range []string{"-v ", "--volume", "--mount", "--privileged", "docker.sock"} {
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
	if !strings.Contains(files["go.mod"], "module ") || !strings.Contains(files["greet.go"], "the friendly greeting") || files["greet_test.go"] == "" || len(files) != 3 {
		t.Fatalf("archive files = %v, want go.mod, the patched greet.go and greet_test.go only", slices.Collect(maps.Keys(files)))
	}
}

func TestDockerBackend_Run_MapsExitCodes(t *testing.T) {
	cases := map[int]Outcome{
		0:                {Verdict: true, Passed: true, Total: 0},
		1:                {Verdict: true, Passed: false},
		exitUnpackFailed: {FailureCode: codeSandboxError},
		125:              {FailureCode: codeSandboxError},
	}
	for exitCode, want := range cases {
		docker := &fakeDocker{respond: func(_ context.Context, args []string, _ io.Writer) (int, error) {
			if args[0] != "start" {
				return 0, nil
			}
			return exitCode, nil
		}}
		got := NewDockerBackend("img@sha256:abc", time.Minute, docker).Run(context.Background(), fixtureJob())
		if got.Verdict != want.Verdict || got.Passed != want.Passed || got.FailureCode != want.FailureCode {
			t.Fatalf("exit %d: outcome = %+v, want %+v", exitCode, got, want)
		}
	}
}

func TestDockerBackend_PatchNotApplying_RunsNothing(t *testing.T) {
	docker := &fakeDocker{}
	job := fixtureJob()
	job.Patch = strings.Replace(touchPatch, "Greeting returns", "Greeting yields", 1)
	outcome := NewDockerBackend("img@sha256:abc", time.Minute, docker).Run(context.Background(), job)
	if outcome.Verdict || outcome.FailureCode != codePatchRejected {
		t.Fatalf("outcome = %+v, want %s", outcome, codePatchRejected)
	}
	if calls := docker.commands(); len(calls) != 0 {
		t.Fatalf("docker calls = %v, want none", calls)
	}
}

func TestDockerBackend_UnknownBaseCommit_RunsNothing(t *testing.T) {
	docker := &fakeDocker{}
	job := fixtureJob()
	job.BaseCommit = "main"
	outcome := NewDockerBackend("img@sha256:abc", time.Minute, docker).Run(context.Background(), job)
	if outcome.Verdict || outcome.FailureCode != codeUnknownBaseCommit {
		t.Fatalf("outcome = %+v, want %s", outcome, codeUnknownBaseCommit)
	}
	if calls := docker.commands(); len(calls) != 0 {
		t.Fatalf("docker calls = %v, want none", calls)
	}
}

func TestDockerBackend_Timeout_RemovesContainerAndReportsTimeout(t *testing.T) {
	docker := &fakeDocker{respond: func(ctx context.Context, args []string, _ io.Writer) (int, error) {
		if args[0] != "start" {
			return 0, nil
		}
		<-ctx.Done()
		return -1, ctx.Err()
	}}
	outcome := NewDockerBackend("img@sha256:abc", time.Millisecond, docker).Run(context.Background(), fixtureJob())
	if outcome.Verdict || outcome.FailureCode != codeTimeout {
		t.Fatalf("outcome = %+v, want %s", outcome, codeTimeout)
	}
	calls := docker.commands()
	want := []string{"start -a -i emberling-sandbox-test_1", "kill emberling-sandbox-test_1", "rm -f emberling-sandbox-test_1", "ps -a --filter name=^emberling-sandbox-test_1$ --format {{.Names}}"}
	if len(calls) != 5 || !strings.HasPrefix(calls[0], "create ") || !slices.Equal(calls[1:], want) {
		t.Fatalf("docker calls = %v, want the create then %v", calls, want)
	}
}

func TestDockerBackend_ParentCancelled_RemovesContainerWithoutTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	docker := &fakeDocker{respond: func(runCtx context.Context, args []string, _ io.Writer) (int, error) {
		if args[0] != "start" {
			return 0, nil
		}
		cancel()
		<-runCtx.Done()
		return -1, runCtx.Err()
	}}
	outcome := NewDockerBackend("img@sha256:abc", time.Minute, docker).Run(ctx, fixtureJob())
	if outcome.FailureCode == codeTimeout {
		t.Fatal("a shutdown was reported as a test timeout")
	}
	if calls := docker.commands(); len(calls) != 5 || calls[3] != "rm -f emberling-sandbox-test_1" {
		t.Fatalf("docker calls = %v, want the container removed", calls)
	}
}

func TestDockerBackend_CreateOutlastingTheTestTimeout_StillRemovesTheContainer(t *testing.T) {
	// The daemon is slow to create the container: creation takes longer than the whole
	// per-test timeout. Creation must not be cut short by that timeout, so the container
	// exists before the start can time out and the cleanup that follows can find it.
	const testTimeout = 20 * time.Millisecond
	var createDeadline time.Time
	var createDeadlineSet bool
	docker := &fakeDocker{respond: func(ctx context.Context, args []string, _ io.Writer) (int, error) {
		switch args[0] {
		case "create":
			createDeadline, createDeadlineSet = ctx.Deadline()
			select {
			case <-time.After(5 * testTimeout):
				return 0, nil
			case <-ctx.Done():
				return -1, ctx.Err()
			}
		case "start":
			<-ctx.Done()
			return -1, ctx.Err()
		}
		return 0, nil
	}}
	outcome := NewDockerBackend("img@sha256:abc", testTimeout, docker).Run(context.Background(), fixtureJob())

	if outcome.Verdict || outcome.FailureCode != codeTimeout {
		t.Fatalf("outcome = %+v, want %s", outcome, codeTimeout)
	}
	if !createDeadlineSet || time.Until(createDeadline) < time.Second {
		t.Fatalf("create deadline = %v (set %v), want a bound independent of the %v test timeout", createDeadline, createDeadlineSet, testTimeout)
	}
	calls := docker.commands()
	want := []string{"start -a -i emberling-sandbox-test_1", "kill emberling-sandbox-test_1", "rm -f emberling-sandbox-test_1", "ps -a --filter name=^emberling-sandbox-test_1$ --format {{.Names}}"}
	if len(calls) != 5 || !strings.HasPrefix(calls[0], "create ") || !slices.Equal(calls[1:], want) {
		t.Fatalf("docker calls = %v, want the completed create then %v", calls, want)
	}
}

func TestDockerBackend_CreateFails_ReportsSandboxErrorWithoutStarting(t *testing.T) {
	for name, respond := range map[string]func(context.Context, []string, io.Writer) (int, error){
		"non-zero exit": func(_ context.Context, args []string, _ io.Writer) (int, error) {
			if args[0] == "create" {
				return 125, nil
			}
			return 0, nil
		},
		"client error": func(_ context.Context, args []string, _ io.Writer) (int, error) {
			if args[0] == "create" {
				return -1, errors.New("docker not found")
			}
			return 0, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			docker := &fakeDocker{respond: respond}
			outcome := NewDockerBackend("img@sha256:abc", time.Minute, docker).Run(context.Background(), fixtureJob())
			if outcome.Verdict || outcome.FailureCode != codeSandboxError {
				t.Fatalf("outcome = %+v, want %s", outcome, codeSandboxError)
			}
			calls := docker.commands()
			want := []string{"kill emberling-sandbox-test_1", "rm -f emberling-sandbox-test_1", "ps -a --filter name=^emberling-sandbox-test_1$ --format {{.Names}}"}
			if len(calls) != 4 || !strings.HasPrefix(calls[0], "create ") || !slices.Equal(calls[1:], want) {
				t.Fatalf("docker calls = %v, want the create then only the defensive cleanup %v", calls, want)
			}
		})
	}
}

func TestDockerBackend_RemovalAlreadyInProgress_WaitsUntilTheContainerIsGone(t *testing.T) {
	// The container stopped and the daemon's own --rm removal is under way: rm -f returns
	// at once while the container is still listed for two more checks.
	stillListed := 2
	docker := &fakeDocker{respond: func(ctx context.Context, args []string, output io.Writer) (int, error) {
		switch args[0] {
		case "start":
			<-ctx.Done()
			return -1, ctx.Err()
		case "rm":
			_, _ = io.WriteString(output, "removal of container emberling-sandbox-test_1 is already in progress")
			return 1, nil
		case "ps":
			if stillListed > 0 {
				stillListed--
				_, _ = io.WriteString(output, "emberling-sandbox-test_1\n")
			}
		}
		return 0, nil
	}}
	outcome := NewDockerBackend("img@sha256:abc", time.Millisecond, docker).Run(context.Background(), fixtureJob())

	if outcome.FailureCode != codeTimeout {
		t.Fatalf("outcome = %+v, want %s", outcome, codeTimeout)
	}
	listing := "ps -a --filter name=^emberling-sandbox-test_1$ --format {{.Names}}"
	calls := docker.commands()
	if len(calls) != 7 || !slices.Equal(calls[4:], []string{listing, listing, listing}) {
		t.Fatalf("docker calls = %v, want the cleanup to list the container until it is gone", calls)
	}
}

func TestDockerBackend_RemoveOrphans_RemovesOnlySandboxContainers(t *testing.T) {
	docker := &fakeDocker{respond: func(_ context.Context, args []string, output io.Writer) (int, error) {
		if args[0] == "ps" {
			_, _ = io.WriteString(output, "emberling-sandbox-test_a\nemberling-sandbox-test_b\nunrelated\n")
		}
		return 0, nil
	}}
	removed, err := NewDockerBackend("img@sha256:abc", time.Minute, docker).RemoveOrphans(context.Background())
	if err != nil || removed != 2 {
		t.Fatalf("removed = %d (%v), want 2", removed, err)
	}
	want := []string{
		"ps -a --filter name=^emberling-sandbox- --format {{.Names}}",
		"rm -f emberling-sandbox-test_a",
		"rm -f emberling-sandbox-test_b",
	}
	if calls := docker.commands(); !slices.Equal(calls, want) {
		t.Fatalf("docker calls = %v, want %v", calls, want)
	}
}

func TestDockerBackend_RemoveOrphans_DockerUnavailable_ReturnsError(t *testing.T) {
	docker := &fakeDocker{respond: func(context.Context, []string, io.Writer) (int, error) { return 1, nil }}
	if _, err := NewDockerBackend("img@sha256:abc", time.Minute, docker).RemoveOrphans(context.Background()); err == nil {
		t.Fatal("RemoveOrphans succeeded although docker ps failed")
	}
}

func TestBoundedBuffer_KeepsOnlyTheLimit(t *testing.T) {
	buffer := &boundedBuffer{limit: 4}
	n, err := buffer.Write([]byte("abcdef"))
	if err != nil || n != 6 || string(buffer.data) != "abcd" {
		t.Fatalf("write = %d %v, data %q, want 6 consumed and abcd kept", n, err, buffer.data)
	}
}
