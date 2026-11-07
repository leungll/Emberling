package service

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// noOccupations is the occupation repository of a fake transaction whose Run holds no
// release target: ending that Run schedules nothing.
type noOccupations struct {
	store.ReleaseOccupationRepository
}

func (noOccupations) ScheduleQueriesOfRun(context.Context, string, time.Time) error { return nil }

// statusWriteRecorder is a fake transaction that records the order of the two writes.
type statusWriteRecorder struct {
	store.Tx
	writes []string
	at     time.Time
}

func (r *statusWriteRecorder) Runs() store.RunRepository { return recordedRuns{recorder: r} }

func (r *statusWriteRecorder) NodeRuns() store.NodeRunRepository { return noPendingRetries{} }

func (r *statusWriteRecorder) ReleaseOccupations() store.ReleaseOccupationRepository {
	return recordedOccupations{recorder: r}
}

// noPendingRetries is a Run with no NodeRun waiting for a scheduled retry.
type noPendingRetries struct {
	store.NodeRunRepository
}

func (noPendingRetries) ListByRun(context.Context, string) ([]domain.NodeRun, error) {
	return nil, nil
}

type recordedRuns struct {
	store.RunRepository
	recorder *statusWriteRecorder
}

func (r recordedRuns) UpdateAggregate(_ context.Context, _ *store.RunLock, status domain.RunStatus, _ time.Time) error {
	r.recorder.writes = append(r.recorder.writes, "status "+string(status))
	return nil
}

type recordedOccupations struct {
	store.ReleaseOccupationRepository
	recorder *statusWriteRecorder
}

func (r recordedOccupations) ScheduleQueriesOfRun(_ context.Context, runID string, at time.Time) error {
	r.recorder.writes, r.recorder.at = append(r.recorder.writes, "schedule "+runID), at
	return nil
}

func TestWriteRunStatusLocked_TerminalStatus_SchedulesOccupationsInTheSameWrite(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		status domain.RunStatus
		want   []string
	}{
		{domain.RunCompleted, []string{"schedule run_1", "status COMPLETED"}},
		{domain.RunFailed, []string{"schedule run_1", "status FAILED"}},
		{domain.RunRunning, []string{"status RUNNING"}},
		{domain.RunPaused, []string{"status PAUSED"}},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			tx := &statusWriteRecorder{}
			lock := store.NewRunLock("run_1", domain.RunRunning, 1)
			if err := (&ExecutionService{}).writeRunStatusLocked(context.Background(), tx, lock, tc.status, now); err != nil {
				t.Fatalf("write run status: %v", err)
			}
			if strings.Join(tx.writes, ", ") != strings.Join(tc.want, ", ") {
				t.Fatalf("writes = %v, want %v", tx.writes, tc.want)
			}
			if len(tc.want) == 2 && !tx.at.Equal(now) {
				t.Errorf("first query at %v, want the Run's end %v", tx.at, now)
			}
		})
	}
}

// Every way a Run ends passes through writeRunStatusLocked, because that is what makes the
// takeover of an occupied target hold for use cases that do not know about releases. A
// direct UpdateAggregate is only allowed for a status that cannot end a Run.
func TestRunStatusWrites_DerivedStatus_GoesThroughTheTerminalPath(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list package files: %v", err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == "writeRunStatusLocked" {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "UpdateAggregate" {
					return true
				}
				checked++
				if len(call.Args) != 4 || !cannotEndRun(call.Args[2]) {
					t.Errorf("%s: %s writes a derived Run status directly; use writeRunStatusLocked", fset.Position(call.Pos()), fn.Name.Name)
				}
				return true
			})
		}
	}
	if checked == 0 {
		t.Fatal("found no UpdateAggregate call to check")
	}
}

// cannotEndRun recognises the two status arguments that never end a Run: the status read
// under the lock, written back for the seq watermark, and the RUNNING constant.
func cannotEndRun(arg ast.Expr) bool {
	switch expr := arg.(type) {
	case *ast.CallExpr:
		selector, ok := expr.Fun.(*ast.SelectorExpr)
		return ok && selector.Sel.Name == "Status"
	case *ast.SelectorExpr:
		pkg, ok := expr.X.(*ast.Ident)
		return ok && pkg.Name == "domain" && expr.Sel.Name == "RunRunning"
	}
	return false
}
