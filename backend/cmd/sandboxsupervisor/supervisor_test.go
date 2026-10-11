package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// workspaceFileSpec is one inline file of a test Workspace document.
type workspaceFileSpec struct {
	Path    string `yaml:"path"`
	Content string `yaml:"content"`
}

// workspaceYAML renders one Workspace document in the shape the platform injects.
func workspaceYAML(t *testing.T, files ...workspaceFileSpec) string {
	t.Helper()
	doc := map[string]any{
		"apiVersion": "ax.io/v1alpha1",
		"kind":       "Workspace",
		"metadata":   map[string]any{"name": "emberling-sbx-0123456789abcdef", "atespace": "emberling-sandbox"},
		"spec":       map[string]any{"files": files},
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal workspace: %v", err)
	}
	return string(out)
}

var sampleFiles = []workspaceFileSpec{
	{Path: "go.mod", Content: "module example.com/fixture\n\ngo 1.22\n"},
	{Path: "greet.go", Content: "package greet\n"},
	{Path: "sub/deeper/data.txt", Content: "data\n"},
}

// newTestSupervisor returns a supervisor writing into a fresh directory and running
// command with the production environment.
func newTestSupervisor(t *testing.T, command []string) *supervisor {
	t.Helper()
	cfg := productionConfig(os.Getenv)
	cfg.workDir = filepath.Join(t.TempDir(), "work")
	cfg.command = command
	return newSupervisor(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func request(t *testing.T, s *supervisor, method, target string) response {
	t.Helper()
	recorder := httptest.NewRecorder()
	s.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
	result := recorder.Result()
	body, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response{status: result.StatusCode, header: result.Header, body: body}
}

// doneResult is the decoded done form of the result endpoint.
type doneResult struct {
	State           string `json:"state"`
	ExitCode        int    `json:"exitCode"`
	WorkspaceDigest string `json:"workspaceDigest"`
	Output          string `json:"output"`
	OutputTruncated bool   `json:"outputTruncated"`
}

func doneResultOf(t *testing.T, s *supervisor) (doneResult, []byte) {
	t.Helper()
	got := request(t, s, http.MethodGet, "/v1/result")
	if got.status != http.StatusOK || got.header.Get("Content-Type") != "application/json" {
		t.Fatalf("result status %d content type %q, want 200 application/json", got.status, got.header.Get("Content-Type"))
	}
	decoder := json.NewDecoder(bytes.NewReader(got.body))
	decoder.DisallowUnknownFields()
	var result doneResult
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode result %q: %v", got.body, err)
	}
	if result.State != "done" {
		t.Fatalf("result = %s, want state done", got.body)
	}
	return result, got.body
}

// runToCompletion runs the supervisor on workspace and waits until it has finished.
func runToCompletion(t *testing.T, s *supervisor, workspace string) {
	t.Helper()
	go s.run(t.Context(), workspace)
	<-s.finished
}

func hexArg(s string) string { return hex.EncodeToString([]byte(s)) }

func TestSupervisor_Readyz_OnlyAfterWorkspaceWritten(t *testing.T) {
	s := newTestSupervisor(t, helperCommand("write", hexArg("ok\n"), "1", "0"))
	reached, release := make(chan struct{}), make(chan struct{})
	s.beforeStart = func() {
		close(reached)
		<-release
	}

	if got := request(t, s, http.MethodGet, "/healthz"); got.status != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", got.status)
	}
	if got := request(t, s, http.MethodGet, "/readyz"); got.status != http.StatusServiceUnavailable {
		t.Fatalf("readyz before the workspace is written = %d, want 503", got.status)
	}
	go s.run(t.Context(), workspaceYAML(t, sampleFiles...))
	<-reached
	for _, file := range sampleFiles {
		content, err := os.ReadFile(filepath.Join(s.cfg.workDir, filepath.FromSlash(file.Path)))
		if err != nil || string(content) != file.Content {
			t.Fatalf("file %s before the command starts = %q, %v; want %q", file.Path, content, err, file.Content)
		}
	}
	if info, err := os.Stat(filepath.Join(s.cfg.workDir, ".gotmp")); err != nil || !info.IsDir() {
		t.Fatalf(".gotmp before the command starts: %v", err)
	}
	if got := request(t, s, http.MethodGet, "/readyz"); got.status != http.StatusServiceUnavailable {
		t.Fatalf("readyz before the command starts = %d, want 503", got.status)
	}
	close(release)
	<-s.finished
	if got := request(t, s, http.MethodGet, "/readyz"); got.status != http.StatusOK {
		t.Fatalf("readyz after the command started = %d, want 200", got.status)
	}
}

func TestSupervisor_WorkspaceFilePathEscapingRoot_RefusesToStart(t *testing.T) {
	for name, bad := range map[string]string{
		"parent":             "../escape.go",
		"nested parent":      "sub/../../escape.go",
		"inner parent":       "sub/../greet.go",
		"absolute":           "/tmp/escape.go",
		"empty":              "",
		"dot prefix":         "./greet.go",
		"double slash":       "sub//greet.go",
		"trailing slash":     "sub/",
		"bare parent":        "..",
		"parent named entry": "sub/..",
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestSupervisor(t, helperCommand("write", hexArg("ran\n"), "1", "0"))
			s.beforeStart = func() { t.Error("the command was about to start for an invalid workspace") }
			files := append(slices.Clone(sampleFiles), workspaceFileSpec{Path: bad, Content: "escaped\n"})

			runToCompletion(t, s, workspaceYAML(t, files...))

			if got := request(t, s, http.MethodGet, "/readyz"); got.status != http.StatusServiceUnavailable {
				t.Fatalf("readyz = %d, want 503", got.status)
			}
			if _, err := os.Stat(s.cfg.workDir); !os.IsNotExist(err) {
				entries, _ := os.ReadDir(s.cfg.workDir)
				if len(entries) != 0 {
					t.Fatalf("work directory has %d entries, want nothing written", len(entries))
				}
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(s.cfg.workDir), "escape.go")); !os.IsNotExist(err) {
				t.Fatalf("a file was written outside the work directory: %v", err)
			}
		})
	}
}

func TestSupervisor_InvalidWorkspace_ReportsFailedAndStaysUnready(t *testing.T) {
	valid := workspaceYAML(t, sampleFiles...)
	for name, workspace := range map[string]string{
		"missing":          "",
		"no workspace":     "apiVersion: ax.io/v1alpha1\nkind: Task\nmetadata:\n  name: t\n",
		"two workspaces":   valid + "---\n" + valid,
		"malformed":        "kind: Workspace\nspec: [unclosed\n",
		"duplicate path":   workspaceYAML(t, sampleFiles[0], sampleFiles[0]),
		"file is also dir": workspaceYAML(t, workspaceFileSpec{Path: "a", Content: "x"}, workspaceFileSpec{Path: "a/b", Content: "y"}),
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestSupervisor(t, helperCommand("write", hexArg("ran\n"), "1", "0"))
			s.beforeStart = func() { t.Error("the command was about to start for an invalid workspace") }

			runToCompletion(t, s, workspace)

			if got := request(t, s, http.MethodGet, "/readyz"); got.status != http.StatusServiceUnavailable {
				t.Fatalf("readyz = %d, want 503", got.status)
			}
			got := request(t, s, http.MethodGet, "/v1/result")
			if got.status != http.StatusOK || got.header.Get("Content-Type") != "application/json" {
				t.Fatalf("result status %d content type %q", got.status, got.header.Get("Content-Type"))
			}
			if want := `{"state":"failed","reason":"WORKSPACE_INVALID"}`; strings.TrimSpace(string(got.body)) != want {
				t.Fatalf("result = %s, want %s", got.body, want)
			}
		})
	}
}

func TestSupervisor_Result_NotDoneUntilCommandExits(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "release")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	s := newTestSupervisor(t, helperCommand("wait-fifo", fifo, "1"))
	go s.run(t.Context(), workspaceYAML(t, sampleFiles...))

	// Opening the FIFO for writing blocks until the command opens it for reading, so the
	// command is running from here until the FIFO is closed.
	writer, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open fifo: %v", err)
	}
	if got := request(t, s, http.MethodGet, "/v1/result"); strings.TrimSpace(string(got.body)) != `{"state":"running"}` {
		t.Fatalf("result while the command runs = %s, want running", got.body)
	}
	if _, err := writer.WriteString("go"); err != nil {
		t.Fatalf("write fifo: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close fifo: %v", err)
	}
	<-s.finished

	result, _ := doneResultOf(t, s)
	if result.ExitCode != 1 || result.Output != "released after 2 bytes\n" || result.OutputTruncated {
		t.Fatalf("result = %+v, want exit 1 with the command's output", result)
	}
}

func TestSupervisor_Result_ReportsDigestOfFilesReadBack(t *testing.T) {
	s := newTestSupervisor(t, helperCommand("write", hexArg("ok\n"), "1", "0"))
	runToCompletion(t, s, workspaceYAML(t, sampleFiles...))

	result, _ := doneResultOf(t, s)
	onDisk := make(map[string][]byte)
	err := filepath.WalkDir(s.cfg.workDir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(s.cfg.workDir, name)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(name)
		onDisk[filepath.ToSlash(rel)] = content
		return err
	})
	if err != nil {
		t.Fatalf("walk work directory: %v", err)
	}
	sent := make(map[string][]byte)
	for _, file := range sampleFiles {
		sent[file.Path] = []byte(file.Content)
	}
	if result.WorkspaceDigest != workspaceDigest(onDisk) || result.WorkspaceDigest != workspaceDigest(sent) {
		t.Fatalf("digest %s, want the digest of the written tree %s", result.WorkspaceDigest, workspaceDigest(onDisk))
	}
	if result.ExitCode != 0 || result.Output != "ok\n" {
		t.Fatalf("result = %+v, want exit 0 with the command's output", result)
	}
}

func TestSupervisor_Result_TruncatesOutputAtLimit(t *testing.T) {
	for name, tc := range map[string]struct {
		chunk     string
		count     int
		suffix    string
		want      string
		truncated bool
	}{
		"exactly at the limit": {chunk: "a", count: maxTestOutputBytes, want: strings.Repeat("a", maxTestOutputBytes)},
		"over the limit":       {chunk: "a", count: maxTestOutputBytes + 4096, want: strings.Repeat("a", maxTestOutputBytes), truncated: true},
		// The limit falls inside the last two-byte rune: the partial rune is dropped
		// rather than reported as a replacement character.
		"rune split by the limit": {chunk: "a", count: maxTestOutputBytes - 1, suffix: "é", want: strings.Repeat("a", maxTestOutputBytes-1), truncated: true},
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestSupervisor(t, helperCommand("write", hexArg(tc.chunk), strconv.Itoa(tc.count), "1", hexArg(tc.suffix)))
			runToCompletion(t, s, workspaceYAML(t, sampleFiles...))

			result, _ := doneResultOf(t, s)
			if result.ExitCode != 1 || result.OutputTruncated != tc.truncated || result.Output != tc.want {
				t.Fatalf("exit %d truncated %v output length %d, want exit 1 truncated %v length %d",
					result.ExitCode, result.OutputTruncated, len(result.Output), tc.truncated, len(tc.want))
			}
		})
	}
}

func TestSupervisor_Result_EncodedBodyWithinLimitForEscapeHeavyOutput(t *testing.T) {
	// Control characters encode as six bytes, quotes and backslashes as two, and an
	// invalid byte becomes a three-byte replacement character; '<' must stay unescaped.
	chunk := "\x01\"\\\xff<"
	count := maxTestOutputBytes/len(chunk) + 1
	s := newTestSupervisor(t, helperCommand("write", hexArg(chunk), strconv.Itoa(count), "1"))
	runToCompletion(t, s, workspaceYAML(t, sampleFiles...))

	result, body := doneResultOf(t, s)
	if len(body) > maxTestOutputBytes+4096 {
		t.Fatalf("response body is %d bytes, want at most %d", len(body), maxTestOutputBytes+4096)
	}
	if !result.OutputTruncated || !utf8.ValidString(result.Output) {
		t.Fatalf("truncated %v valid %v, want a truncated valid UTF-8 output", result.OutputTruncated, utf8.ValidString(result.Output))
	}
	full := strings.Repeat("\x01\"\\�<", count)
	if !strings.HasPrefix(full, result.Output) {
		t.Fatal("output is not a prefix of the command's output with invalid bytes replaced")
	}
	if encoded := encodedLengthForTest(result.Output); encoded > maxTestOutputBytes {
		t.Fatalf("encoded output is %d bytes, want at most %d", encoded, maxTestOutputBytes)
	}
	_, next := utf8.DecodeRuneInString(full[len(result.Output):])
	if encoded := encodedLengthForTest(full[:len(result.Output)+next]); encoded <= maxTestOutputBytes {
		t.Fatalf("one more rune encodes to %d bytes, within the limit: the prefix is not the longest", encoded)
	}
	if !bytes.Contains(body, []byte("<")) {
		t.Fatal("'<' was HTML-escaped in the response body")
	}
}

func TestSupervisor_Command_RunsWithFixedEnvOnly(t *testing.T) {
	t.Setenv("AX_TASK_YAML", "kind: Task")
	t.Setenv("AX_WORKSPACES_YAML", "kind: Workspace")
	t.Setenv("SUPERVISOR_TEST_SECRET", "must-not-leak")
	s := newTestSupervisor(t, helperCommand("env"))
	runToCompletion(t, s, workspaceYAML(t, sampleFiles...))

	result, _ := doneResultOf(t, s)
	want := []string{
		"CGO_ENABLED=0",
		"GOCACHE=/tmp/gocache",
		"GOFLAGS=-mod=readonly",
		"GOPATH=/tmp/gopath",
		"GOPROXY=off",
		"GOTELEMETRY=off",
		"GOTMPDIR=/work/.gotmp",
		"GOTOOLCHAIN=local",
		"HOME=/tmp",
		"PATH=" + os.Getenv("PATH"),
	}
	slices.Sort(want)
	got := strings.Split(strings.TrimSuffix(result.Output, "\n"), "\n")
	if !slices.Equal(got, want) {
		t.Fatalf("command environment = %q, want exactly %q", got, want)
	}
}

func TestSupervisor_NonGetRequest_Rejected(t *testing.T) {
	s := newTestSupervisor(t, helperCommand("write", hexArg("ok\n"), "1", "0"))
	for _, target := range []string{"/healthz", "/readyz", "/v1/result", "/unknown"} {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions} {
			got := request(t, s, method, target)
			if got.status != http.StatusMethodNotAllowed || got.header.Get("Allow") != http.MethodGet {
				t.Errorf("%s %s = %d Allow %q, want 405 Allow GET", method, target, got.status, got.header.Get("Allow"))
			}
		}
	}
}

func TestSupervisor_UnknownPath_NotFound(t *testing.T) {
	s := newTestSupervisor(t, helperCommand("write", hexArg("ok\n"), "1", "0"))
	for _, target := range []string{"/", "/v1", "/v1/result/extra", "/v1/results", "/healthz/", "/metrics"} {
		if got := request(t, s, http.MethodGet, target); got.status != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, got.status)
		}
	}
}

// encodedLengthForTest is the length of s encoded as a JSON string without HTML escaping,
// excluding the surrounding quotes; it is computed here independently of the supervisor.
func encodedLengthForTest(s string) int {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(s); err != nil {
		panic(err)
	}
	return len(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))) - 2
}
