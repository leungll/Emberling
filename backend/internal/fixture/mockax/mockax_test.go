package mockax

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/fixture/mockcontrol"
)

// sizeDigest is a stand-in digest: tests here only need it to be deterministic.
func sizeDigest(files map[string][]byte) string {
	total := 0
	for name, content := range files {
		total += len(name) + len(content)
	}
	return "size:" + strings.Repeat("x", total)
}

const testAtespace = "emberling-sandbox"

func documents(name string, files map[string]string) []byte {
	type file struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	var list []file
	for path, content := range files {
		list = append(list, file{Path: path, Content: content})
	}
	workspace, _ := json.Marshal(map[string]any{
		"apiVersion": "ax.io/v1alpha1", "kind": "Workspace",
		"metadata": map[string]string{"name": name, "atespace": testAtespace},
		"spec":     map[string]any{"files": list},
	})
	task, _ := json.Marshal(map[string]any{
		"apiVersion": "ax.io/v1alpha1", "kind": "Task",
		"metadata": map[string]string{"name": name, "atespace": testAtespace},
		"spec":     map[string]any{"workspaces": []map[string]string{{"name": name, "path": "/work"}}, "env": []string{}},
	})
	return []byte(string(workspace) + "\n---\n" + string(task) + "\n")
}

func readResult(t *testing.T, fake *Fake, name string) (int, string, error) {
	t.Helper()
	status, body, err := fake.ReadResult(context.Background(), testAtespace, name)
	if err != nil {
		return 0, "", err
	}
	defer body.Close()
	raw, _ := io.ReadAll(body)
	return status, string(raw), nil
}

// resume resumes name and fails the test unless the fake confirms it.
func resume(t *testing.T, fake *Fake, name string) {
	t.Helper()
	if resumed, err := fake.Resume(context.Background(), testAtespace, name); err != nil || !resumed {
		t.Fatalf("resume %s = %v, %v, want confirmed", name, resumed, err)
	}
}

func TestFake_DefaultScenario_ReadyThenDoneWithTwelvePassingTests(t *testing.T) {
	fake := New(Config{Digest: sizeDigest})
	files := map[string]string{"a.go": "package a\n"}
	if err := fake.Apply(context.Background(), testAtespace, documents("emberling-sbx-1", files)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if state, err := fake.TaskState(context.Background(), testAtespace, "emberling-sbx-1"); err != nil || state != TaskSuspended {
		t.Fatalf("state before resume = %q, %v, want suspended", state, err)
	}
	resume(t, fake, "emberling-sbx-1")
	if state, err := fake.TaskState(context.Background(), testAtespace, "emberling-sbx-1"); err != nil || state != TaskReady {
		t.Fatalf("state = %q, %v, want ready", state, err)
	}
	status, body, err := readResult(t, fake, "emberling-sbx-1")
	if err != nil || status != http.StatusOK {
		t.Fatalf("result = %d, %v, want 200", status, err)
	}
	var result struct {
		State           string `json:"state"`
		ExitCode        int    `json:"exitCode"`
		WorkspaceDigest string `json:"workspaceDigest"`
		Output          string `json:"output"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	wantDigest := sizeDigest(map[string][]byte{"a.go": []byte("package a\n")})
	if result.State != "done" || result.ExitCode != 0 || result.WorkspaceDigest != wantDigest ||
		strings.Count(result.Output, `"Action":"pass"`) != 12 {
		t.Fatalf("result = %+v, want done, exit 0, the injected digest and 12 passing tests", result)
	}
}

func TestFake_ScenarioByDigest_OverridesDefault(t *testing.T) {
	files := map[string]string{"a.go": "broken\n"}
	digest := sizeDigest(map[string][]byte{"a.go": []byte("broken\n")})
	fake := New(Config{Digest: sizeDigest, Scenarios: map[string]Scenario{digest: {Startup: StartupNeverReady}}})
	if err := fake.Apply(context.Background(), testAtespace, documents("emberling-sbx-2", files)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	resume(t, fake, "emberling-sbx-2")
	for range 3 {
		if state, _ := fake.TaskState(context.Background(), testAtespace, "emberling-sbx-2"); state != TaskPending {
			t.Fatalf("state = %q, want pending forever", state)
		}
		if status, err := fake.ReadReady(context.Background(), testAtespace, "emberling-sbx-2"); err != nil || status != http.StatusServiceUnavailable {
			t.Fatalf("readiness = %d, %v, want 503 forever", status, err)
		}
	}
}

func TestFake_Resume_RefusedOrUncertain(t *testing.T) {
	refused := New(Config{Digest: sizeDigest, Default: &Scenario{ResumeRefused: true}})
	if err := refused.Apply(context.Background(), testAtespace, documents("emberling-sbx-6", map[string]string{"a": "b"})); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if resumed, err := refused.Resume(context.Background(), testAtespace, "emberling-sbx-6"); err != nil || resumed {
		t.Fatalf("resume = %v, %v, want a refusal", resumed, err)
	}
	if state, _ := refused.TaskState(context.Background(), testAtespace, "emberling-sbx-6"); state != TaskFailed {
		t.Fatalf("state after a refused resume = %q, want failed", state)
	}
	if resumed, err := refused.Resume(context.Background(), testAtespace, "emberling-sbx-missing"); err != nil || resumed {
		t.Fatalf("resume of a missing task = %v, %v, want a refusal", resumed, err)
	}

	uncertain := New(Config{Digest: sizeDigest, ResumeUncertain: true})
	if err := uncertain.Apply(context.Background(), testAtespace, documents("emberling-sbx-7", map[string]string{"a": "b"})); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := uncertain.Resume(context.Background(), testAtespace, "emberling-sbx-7"); err == nil {
		t.Fatal("resume succeeded, want an unknown outcome")
	}
	if state, _ := uncertain.TaskState(context.Background(), testAtespace, "emberling-sbx-7"); state != TaskReady {
		t.Fatalf("state after an unconfirmed resume = %q, want the Task started anyway", state)
	}
	if names := uncertain.Resumed(); len(names) != 1 || names[0] != "emberling-sbx-7" {
		t.Fatalf("resumed = %v, want the one call", names)
	}
}

func TestFake_ReadyLater_AnswersReadinessSequenceThen200(t *testing.T) {
	scenario := Scenario{Startup: StartupReadyLater, ReadyAnswers: []int{http.StatusServiceUnavailable, 0, http.StatusNotFound}}
	fake := New(Config{Digest: sizeDigest, Default: &scenario})
	if err := fake.Apply(context.Background(), testAtespace, documents("emberling-sbx-8", map[string]string{"a": "b"})); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if status, _ := fake.ReadReady(context.Background(), testAtespace, "emberling-sbx-8"); status != http.StatusNotFound {
		t.Fatalf("readiness before resume = %d, want 404", status)
	}
	resume(t, fake, "emberling-sbx-8")
	if state, _ := fake.TaskState(context.Background(), testAtespace, "emberling-sbx-8"); state != TaskPending {
		t.Fatalf("state after resume = %q, want not ready", state)
	}
	for i, want := range []int{http.StatusServiceUnavailable, 0, http.StatusNotFound, http.StatusOK, http.StatusOK} {
		status, err := fake.ReadReady(context.Background(), testAtespace, "emberling-sbx-8")
		if (want == 0) != (err != nil) || status != want {
			t.Fatalf("readiness query %d = %d, %v, want %d", i+1, status, err, want)
		}
	}
	if status, _ := fake.ReadReady(context.Background(), testAtespace, "emberling-sbx-missing"); status != http.StatusNotFound {
		t.Fatalf("readiness of a missing task = %d, want 404", status)
	}
}

func TestFake_SameNameTwice_SecondApplyFails(t *testing.T) {
	fake := New(Config{Digest: sizeDigest})
	docs := documents("emberling-sbx-3", map[string]string{"a": "b"})
	if err := fake.Apply(context.Background(), testAtespace, docs); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := fake.Apply(context.Background(), testAtespace, docs); err == nil {
		t.Fatal("second apply of the same name succeeded, want an error")
	}
}

func TestFake_ApplyUncertain_CreatesResourcesButReturnsError(t *testing.T) {
	fake := New(Config{Digest: sizeDigest, ApplyUncertain: true})
	if err := fake.Apply(context.Background(), testAtespace, documents("emberling-sbx-4", map[string]string{"a": "b"})); err == nil {
		t.Fatal("apply succeeded, want an uncertain outcome")
	}
	if names, _ := fake.List(context.Background(), testAtespace, "emberling-sbx-"); len(names) != 1 {
		t.Fatalf("names = %v, want the resources created anyway", names)
	}
}

func TestFake_RecordAndBarrier_HoldsResultQueryUntilRelease(t *testing.T) {
	record, err := mockcontrol.OpenRecord(filepath.Join(t.TempDir(), "ax.jsonl"))
	if err != nil {
		t.Fatalf("open record: %v", err)
	}
	t.Cleanup(func() { _ = record.Close() })
	held := make(chan string, 1)
	fake := New(Config{Digest: sizeDigest, Record: record, HeldNotify: held})
	if err := fake.Apply(context.Background(), testAtespace, documents("emberling-sbx-5", map[string]string{"a": "b"})); err != nil {
		t.Fatalf("apply: %v", err)
	}
	control := httptest.NewServer(fake.ControlHandler())
	t.Cleanup(control.Close)
	post := func(action, body string) {
		resp, err := http.Post(control.URL+"/control/"+action, "application/json", strings.NewReader(body))
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /control/%s: %v %v", action, resp, err)
		}
		_ = resp.Body.Close()
	}
	post("pause", `{"kinds":["result"]}`)

	done := make(chan int, 1)
	go func() {
		status, _, _ := readResult(t, fake, "emberling-sbx-5")
		done <- status
	}()
	if kind := <-held; kind != KindResult {
		t.Fatalf("held kind = %q, want %q", kind, KindResult)
	}
	post("release", "")
	if status := <-done; status != http.StatusOK {
		t.Fatalf("released query status = %d, want 200", status)
	}

	resp, err := http.Get(control.URL + "/control/record?after=0")
	if err != nil {
		t.Fatalf("GET record: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	for _, want := range []string{`"kind":"apply"`, `"kind":"result"`, `"event":"released"`, `"name":"emberling-sbx-5"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("record %s lacks %s", raw, want)
		}
	}
}
