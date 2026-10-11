//go:build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/fixture/sandboxrunner"
)

// These tests build the sandbox image from its Dockerfile and run the supervisor in it on
// a local Docker daemon. They need no database; without a reachable daemon, or without
// the pinned base image for linux/amd64 locally or from the registry within a bounded
// time, they skip with the reason.

const (
	// imageTag names the image built from the Dockerfile for these tests.
	imageTag = "emberling-sandboxsupervisor:integration-test"
	// imagePlatform is the only platform the sandbox image is built for.
	imagePlatform = "linux/amd64"
	// buildTimeout bounds building the image, which compiles the supervisor, possibly
	// under emulation.
	buildTimeout = 10 * time.Minute
	// runTimeout bounds one container from start until the test command has exited.
	runTimeout = 10 * time.Minute
	// commandTimeout bounds each short docker CLI call.
	commandTimeout = 2 * time.Minute
)

// fixtureDir holds the runner's fixture module, the base the patched trees start from.
var fixtureDir = filepath.Join("..", "..", "internal", "fixture", "sandboxrunner", "testdata", "fixture")

// imageUnavailable caches the probe result so a missing base image costs one bounded pull
// per test binary rather than one per test.
var (
	imageProbeOnce   sync.Once
	imageUnavailable string
)

func requireSandboxImageDocker(t *testing.T) {
	t.Helper()
	imageProbeOnce.Do(func() { imageUnavailable = probeSandboxImageDocker() })
	if imageUnavailable != "" {
		t.Skip(imageUnavailable)
	}
}

// probeSandboxImageDocker returns an empty string when the daemon is reachable and the
// pinned base image can run for linux/amd64, and otherwise the reason the image tests
// must skip.
func probeSandboxImageDocker() string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		return fmt.Sprintf("docker daemon unavailable (docker info: %v)", err)
	}
	present := func() bool {
		runCtx, runCancel := context.WithTimeout(context.Background(), commandTimeout)
		defer runCancel()
		run := exec.CommandContext(runCtx, "docker", "run", "--rm", "--pull", "never", "--platform", imagePlatform,
			"--network", "none", "--entrypoint", "true", sandboxrunner.DefaultImage)
		run.WaitDelay = 5 * time.Second
		return run.Run() == nil
	}
	if present() {
		return ""
	}
	// The pull is bounded so an unreachable registry skips the tests instead of hanging
	// until the whole test binary times out. WaitDelay is needed as well: a credential
	// helper started by the docker CLI can outlive the killed CLI and keep the output pipe
	// open, which would otherwise block the wait indefinitely.
	pullCtx, pullCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer pullCancel()
	pull := exec.CommandContext(pullCtx, "docker", "pull", "-q", "--platform", imagePlatform, sandboxrunner.DefaultImage)
	pull.WaitDelay = 5 * time.Second
	if output, err := pull.CombinedOutput(); err != nil {
		if pullCtx.Err() != nil {
			return fmt.Sprintf("base image %s for %s is not present locally and pulling it did not finish within 120s", sandboxrunner.DefaultImage, imagePlatform)
		}
		return fmt.Sprintf("base image %s for %s is not present locally and cannot be pulled: %v: %s", sandboxrunner.DefaultImage, imagePlatform, err, strings.TrimSpace(string(output)))
	}
	if !present() {
		return fmt.Sprintf("base image %s cannot run for %s on this daemon", sandboxrunner.DefaultImage, imagePlatform)
	}
	return ""
}

// buildOnce builds the sandbox image from its Dockerfile once per test binary; buildErr
// keeps the outcome for every test that needs the image.
var (
	buildOnce sync.Once
	buildErr  error
)

func buildSandboxImage(t *testing.T) {
	t.Helper()
	buildOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
		defer cancel()
		build := exec.CommandContext(ctx, "docker", "build", "--platform", imagePlatform,
			"-f", imageDockerfile, "-t", imageTag, filepath.Join("..", ".."))
		build.WaitDelay = 5 * time.Second
		if output, err := build.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("docker build: %w: %s", err, tail(output))
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
}

// fixtureTree returns the runner's fixture module keyed by workspace path, with go.mod
// restored from the name it is embedded under.
func fixtureTree(t *testing.T) map[string]string {
	t.Helper()
	tree := make(map[string]string)
	err := filepath.WalkDir(fixtureDir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(fixtureDir, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if path.Base(rel) == "go.mod.txt" {
			rel = path.Join(path.Dir(rel), "go.mod")
		}
		tree[rel] = string(content)
		return nil
	})
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return tree
}

// patchedTree returns the fixture tree with one exact replacement in greet.go, the file
// the runner's test patches change.
func patchedTree(t *testing.T, old, replacement string) map[string]string {
	t.Helper()
	tree := fixtureTree(t)
	if strings.Count(tree["greet.go"], old) != 1 {
		t.Fatalf("fixture greet.go does not contain %q exactly once", old)
	}
	tree["greet.go"] = strings.Replace(tree["greet.go"], old, replacement, 1)
	return tree
}

// runSupervisorImage starts the image with tree as its Workspace, waits until the
// supervisor logs that the test command exited, and returns the result endpoint's body.
func runSupervisorImage(t *testing.T, name string, tree map[string]string) []byte {
	t.Helper()
	var files []workspaceFileSpec
	for _, rel := range sortedKeys(tree) {
		files = append(files, workspaceFileSpec{Path: rel, Content: tree[rel]})
	}
	container := fmt.Sprintf("emberling-supervisor-itest-%s-%d", name, os.Getpid())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", container).Run()
	})
	docker(t, "run", "-d", "--platform", imagePlatform, "--network", "none", "--name", container,
		"-e", "AX_WORKSPACES_YAML="+workspaceYAML(t, files...), imageTag)

	// Following the container log is a blocking read, not a poll: it returns the
	// supervisor's structured log lines as they are written.
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	reader, writer := io.Pipe()
	logs := exec.CommandContext(ctx, "docker", "logs", "-f", container)
	logs.Stdout = writer
	logs.Stderr = writer
	logs.WaitDelay = 5 * time.Second
	if err := logs.Start(); err != nil {
		t.Fatalf("docker logs: %v", err)
	}
	go func() { _ = writer.CloseWithError(logs.Wait()) }()
	var seen []string
	scanner := bufio.NewScanner(reader)
	exited := false
	for !exited && scanner.Scan() {
		line := scanner.Text()
		seen = append(seen, line)
		var entry struct {
			Msg string `json:"msg"`
		}
		exited = json.Unmarshal([]byte(line), &entry) == nil && entry.Msg == "test command exited"
	}
	cancel()
	_, _ = io.Copy(io.Discard, reader)
	if !exited {
		t.Fatalf("supervisor did not report the command's exit within %s; log:\n%s", runTimeout, strings.Join(seen, "\n"))
	}
	// wget exits non-zero on any status other than 200, which fails the docker call.
	docker(t, "exec", container, "wget", "-qO-", "http://127.0.0.1/readyz")
	return docker(t, "exec", container, "wget", "-qO-", "http://127.0.0.1/v1/result")
}

func docker(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", args...)
	command.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("docker %s: %v: %s", args[0], err, tail(stderr.Bytes()))
	}
	return output
}

func sortedKeys(tree map[string]string) []string {
	keys := make([]string, 0, len(tree))
	for key := range tree {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func tail(output []byte) string {
	const keep = 4096
	if len(output) > keep {
		output = output[len(output)-keep:]
	}
	return strings.TrimSpace(string(output))
}

func TestSupervisorImage_FailingAndFixingPatch_ReportExitOneAndZero(t *testing.T) {
	requireSandboxImageDocker(t)
	buildSandboxImage(t)

	for _, tc := range []struct {
		name     string
		tree     map[string]string
		exitCode int
		event    string
	}{
		// The failing patch changes the greeting the fixture's test expects.
		{name: "failing", tree: patchedTree(t, `"Hello, "`, `"Hi, "`), exitCode: 1, event: `"Action":"fail"`},
		// The fixing patch keeps the greeting and only rewords its comment.
		{name: "fixing", tree: patchedTree(t, "returns the greeting", "returns the friendly greeting"), exitCode: 0, event: `"Action":"pass"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := runSupervisorImage(t, tc.name, tc.tree)
			decoder := json.NewDecoder(bytes.NewReader(body))
			decoder.DisallowUnknownFields()
			var result doneResult
			if err := decoder.Decode(&result); err != nil {
				t.Fatalf("decode result %q: %v", body, err)
			}
			tree := make(map[string][]byte, len(tc.tree))
			for rel, content := range tc.tree {
				tree[rel] = []byte(content)
			}
			if result.State != "done" || result.ExitCode != tc.exitCode || result.OutputTruncated {
				t.Fatalf("result state %s exit %d truncated %v, want done exit %d; output:\n%s",
					result.State, result.ExitCode, result.OutputTruncated, tc.exitCode, result.Output)
			}
			if result.WorkspaceDigest != workspaceDigest(tree) {
				t.Fatalf("digest %s, want %s for the tree sent", result.WorkspaceDigest, workspaceDigest(tree))
			}
			if !strings.Contains(result.Output, `"Test":"TestGreeting"`) || !strings.Contains(result.Output, tc.event) {
				t.Fatalf("output lacks the TestGreeting %s event:\n%s", tc.event, result.Output)
			}
		})
	}
}
