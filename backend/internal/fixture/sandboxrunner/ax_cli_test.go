package sandboxrunner

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeAXCLI stands in for the ax CLI: respond decides each call's exit code and output.
type fakeAXCLI struct {
	mu      sync.Mutex
	calls   []string
	stdin   []byte
	respond func(args []string, output io.Writer) int
}

func (f *fakeAXCLI) AX(_ context.Context, args []string, stdin io.Reader, output io.Writer) (int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, strings.Join(args, " "))
	if stdin != nil {
		f.stdin, _ = io.ReadAll(stdin)
	}
	respond := f.respond
	f.mu.Unlock()
	if respond == nil {
		return 0, nil
	}
	return respond(args, output), nil
}

func TestAXCLI_Apply_PipesDocumentsAndHidesOutputFromErrors(t *testing.T) {
	cli := &fakeAXCLI{respond: func(_ []string, output io.Writer) int {
		_, _ = io.WriteString(output, "error: cluster token abc123 rejected")
		return 1
	}}
	err := NewAXCLI(cli).Apply(context.Background(), "emberling-sandbox", []byte("{}\n---\n{}\n"))
	if err == nil || strings.Contains(err.Error(), "abc123") {
		t.Fatalf("apply error = %v, want an error that does not echo the CLI output", err)
	}
	if !slices.Equal(cli.calls, []string{"apply --atespace emberling-sandbox -f -"}) || string(cli.stdin) != "{}\n---\n{}\n" {
		t.Fatalf("calls = %v stdin %q, want one apply reading the documents from stdin", cli.calls, cli.stdin)
	}
}

func TestAXCLI_TaskState_MapsPhaseAndReadyCondition(t *testing.T) {
	cases := map[string]struct {
		output string
		exit   int
		want   string
	}{
		"ready":         {"apiVersion: ax.io/v1alpha1\nkind: Task\nstatus:\n  phase: Running\n  conditions:\n  - type: WorkspaceReady\n    status: \"True\"\n  - type: Ready\n    status: \"True\"\n", 0, AXTaskReady},
		"not yet ready": {"status:\n  phase: Pending\n  conditions:\n  - status: \"False\"\n    type: Ready\n", 0, AXTaskPending},
		"failed":        {"status:\n  phase: Failed\n", 0, AXTaskFailed},
		"suspended":     {"status:\n  phase: Suspended\n  conditions:\n  - type: Ready\n    status: \"True\"\n", 0, AXTaskSuspended},
		"no status yet": {"kind: Task\nspec:\n  image: x\n", 0, AXTaskPending},
		"not found":     {"Error: task \"x\" not found", 1, AXTaskMissing},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cli := &fakeAXCLI{respond: func(_ []string, output io.Writer) int {
				_, _ = io.WriteString(output, tc.output)
				return tc.exit
			}}
			state, err := NewAXCLI(cli).TaskState(context.Background(), "emberling-sandbox", "emberling-sbx-1")
			if err != nil || state != tc.want {
				t.Fatalf("state = %q, %v, want %q", state, err, tc.want)
			}
			if !slices.Equal(cli.calls, []string{"get task emberling-sbx-1 --atespace emberling-sandbox"}) {
				t.Fatalf("calls = %v", cli.calls)
			}
		})
	}
	failing := &fakeAXCLI{respond: func([]string, io.Writer) int { return 2 }}
	if _, err := NewAXCLI(failing).TaskState(context.Background(), "emberling-sandbox", "emberling-sbx-1"); err == nil {
		t.Fatal("TaskState succeeded although the CLI failed")
	}
}

func TestAXCLI_DeleteAndList_TreatMissingAsGoneAndMergeKinds(t *testing.T) {
	cli := &fakeAXCLI{respond: func(args []string, output io.Writer) int {
		switch {
		case args[0] == "delete" && args[1] == "workspace":
			_, _ = io.WriteString(output, "workspace not found")
			return 1
		case args[0] == "get" && args[1] == "tasks":
			_, _ = io.WriteString(output, "NAME                            PHASE\nemberling-sbx-aaaaaaaaaaaaaaaa  Running\nother  Running\n")
		case args[0] == "get" && args[1] == "workspaces":
			_, _ = io.WriteString(output, "NAME\nemberling-sbx-aaaaaaaaaaaaaaaa\nemberling-sbx-bbbbbbbbbbbbbbbb\n")
		}
		return 0
	}}
	control := NewAXCLI(cli)
	if err := control.Delete(context.Background(), "emberling-sandbox", "emberling-sbx-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	names, err := control.List(context.Background(), "emberling-sandbox", "emberling-sbx-")
	if err != nil || !slices.Equal(names, []string{"emberling-sbx-aaaaaaaaaaaaaaaa", "emberling-sbx-bbbbbbbbbbbbbbbb"}) {
		t.Fatalf("names = %v, %v, want both prefixed names once", names, err)
	}
	want := []string{
		"delete task emberling-sbx-1 --atespace emberling-sandbox",
		"delete workspace emberling-sbx-1 --atespace emberling-sandbox",
		"get tasks --atespace emberling-sandbox",
		"get workspaces --atespace emberling-sandbox",
	}
	if !slices.Equal(cli.calls, want) {
		t.Fatalf("calls = %v, want %v", cli.calls, want)
	}
}

func TestAXRouterReader_ReadResult_TargetsTheTaskActor(t *testing.T) {
	var gotPath, gotTarget, gotMethod string
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotTarget = r.Method, r.URL.Path, r.Header.Get("ate-target-actor")
		_, _ = io.WriteString(w, `{"state":"running"}`)
	}))
	t.Cleanup(router.Close)
	status, body, err := NewAXRouterReader(router.URL+"/", router.Client()).ReadResult(context.Background(), "emberling-sandbox", "emberling-sbx-1")
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	raw, _ := io.ReadAll(body)
	_ = body.Close()
	if status != http.StatusOK || string(raw) != `{"state":"running"}` || gotMethod != http.MethodGet ||
		gotPath != "/v1/result" || gotTarget != "emberling-sandbox/emberling-sbx-1" {
		t.Fatalf("status %d body %q, request %s %s target %q", status, raw, gotMethod, gotPath, gotTarget)
	}
}
