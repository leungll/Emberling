package externaltool_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/externaltool"
)

func TestRegistration_CompleteSpec_RegistersAsyncSafePollableTool(t *testing.T) {
	reg, err := externaltool.Registration(echoSpec(), &fakeTaskClient{})
	if err != nil {
		t.Fatalf("Registration: %v", err)
	}
	if reg.Metadata.ExecutionKind != domain.ToolExecutionAsync {
		t.Errorf("executionKind = %s, want ASYNC", reg.Metadata.ExecutionKind)
	}
	if reg.Metadata.SideEffect.Kind != domain.SideEffectExternal || reg.Metadata.SideEffect.Idempotency != domain.IdempotencySafe {
		t.Errorf("side effect = %+v, want EXTERNAL/SAFE", reg.Metadata.SideEffect)
	}
	if _, ok := reg.Executor.(registry.PollableAsyncToolExecutor); !ok {
		t.Errorf("executor %T is not pollable", reg.Executor)
	}
	tools := registry.NewToolRegistry()
	if err := tools.Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
}

func TestRegistration_ExplicitAsyncSafeDeclaration_Accepted(t *testing.T) {
	spec := echoSpec()
	spec.Metadata.ExecutionKind = domain.ToolExecutionAsync
	spec.Metadata.SideEffect = domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencySafe}
	spec.ProviderID = "media-service"
	reg, err := externaltool.Registration(spec, &fakeTaskClient{})
	if err != nil {
		t.Fatalf("Registration: %v", err)
	}
	got, err := pollable(t, reg).Execute(context.Background(), echoAction())
	if err != nil || got.ExternalTask.ProviderID != "media-service" {
		t.Fatalf("dispatch = %+v, %v, want the spec's provider id", got, err)
	}
}

func TestRegistration_IncompleteOrContradictorySpec_Fails(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*externaltool.Spec)
		want   string
	}{
		"no poll":       {mutate: func(s *externaltool.Spec) { s.Metadata.Poll = nil }, want: "poll"},
		"no task type":  {mutate: func(s *externaltool.Spec) { s.TaskType = "" }, want: "task type"},
		"no map input":  {mutate: func(s *externaltool.Spec) { s.MapInput = nil }, want: "MapInput"},
		"no map result": {mutate: func(s *externaltool.Spec) { s.MapResult = nil }, want: "MapResult"},
		"no name":       {mutate: func(s *externaltool.Spec) { s.Metadata.Name = "" }, want: "names no tool"},
		"sync kind":     {mutate: func(s *externaltool.Spec) { s.Metadata.ExecutionKind = domain.ToolExecutionSync }, want: "ASYNC"},
		"keyed effect": {mutate: func(s *externaltool.Spec) {
			s.Metadata.SideEffect = domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed}
		}, want: "side effect"},
		"no side effect": {mutate: func(s *externaltool.Spec) {
			s.Metadata.SideEffect = domain.SideEffectPolicy{Kind: domain.SideEffectNone}
		}, want: "side effect"},
	} {
		t.Run(name, func(t *testing.T) {
			spec := echoSpec()
			tc.mutate(&spec)
			_, err := externaltool.Registration(spec, &fakeTaskClient{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Registration error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
	if _, err := externaltool.Registration(echoSpec(), nil); err == nil {
		t.Error("Registration with a nil client succeeded, want an error")
	}
}

func TestRegistration_MetadataSchemasStillValidated_ByRegistry(t *testing.T) {
	spec := echoSpec()
	spec.Metadata.OutputSchema = json.RawMessage(`{"type": 42}`)
	reg, err := externaltool.Registration(spec, &fakeTaskClient{})
	if err != nil {
		t.Fatalf("Registration: %v", err)
	}
	if err := registry.NewToolRegistry().Register(reg); err == nil {
		t.Fatal("Register accepted an invalid output schema")
	}
}

// A Tool with one stable Name may dispatch to more than one task type; the selector reads
// the committed Action once per dispatch, and a selection that fails sends nothing.
func TestExecute_TaskTypeSelector_PicksTheTaskTypePerCall(t *testing.T) {
	spec := echoSpec()
	spec.TaskType = ""
	spec.TaskTypeFor = func(action registry.ToolAction) (string, error) {
		var args struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(action.Arguments, &args); err != nil {
			return "", err
		}
		if args.Text == "" {
			return "", errors.New("text selects no task type")
		}
		return "ECHO_" + strings.ToUpper(args.Text), nil
	}
	client := &fakeTaskClient{}
	executor := pollable(t, mustRegistration(t, spec, client))

	for _, text := range []string{"hello", "world"} {
		action := echoAction()
		action.Arguments = json.RawMessage(`{"text":"` + text + `"}`)
		if _, err := executor.Execute(context.Background(), action); err != nil {
			t.Fatalf("Execute(%s): %v", text, err)
		}
	}
	if len(client.submissions) != 2 || client.submissions[0].taskType != "ECHO_HELLO" || client.submissions[1].taskType != "ECHO_WORLD" {
		t.Fatalf("submissions = %+v, want ECHO_HELLO then ECHO_WORLD", client.submissions)
	}

	action := echoAction()
	action.Arguments = json.RawMessage(`{"text":""}`)
	_, err := executor.Execute(context.Background(), action)
	if err == nil || !strings.Contains(err.Error(), "select task type") {
		t.Fatalf("Execute with a failing selector = %v, want a dispatch error naming the selection", err)
	}
	if len(client.submissions) != 2 {
		t.Errorf("submissions after the failed selection = %d, want still 2: nothing was sent", len(client.submissions))
	}
}

func TestExecute_TaskTypeSelectorPicksEmptyType_SendsNothing(t *testing.T) {
	spec := echoSpec()
	spec.TaskType = ""
	spec.TaskTypeFor = func(registry.ToolAction) (string, error) { return " ", nil }
	client := &fakeTaskClient{}
	executor := pollable(t, mustRegistration(t, spec, client))

	if _, err := executor.Execute(context.Background(), echoAction()); err == nil || !strings.Contains(err.Error(), "empty task type") {
		t.Fatalf("Execute = %v, want an error about the empty task type", err)
	}
	if len(client.submissions) != 0 {
		t.Errorf("submissions = %d, want none", len(client.submissions))
	}
}

func TestRegistration_TaskTypeDeclaredTwiceOrNotAtAll_Fails(t *testing.T) {
	both := echoSpec()
	both.TaskTypeFor = func(registry.ToolAction) (string, error) { return echoTaskType, nil }
	if _, err := externaltool.Registration(both, &fakeTaskClient{}); err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Errorf("Registration with TaskType and TaskTypeFor = %v, want an error about the double declaration", err)
	}
	neither := echoSpec()
	neither.TaskType = ""
	if _, err := externaltool.Registration(neither, &fakeTaskClient{}); err == nil || !strings.Contains(err.Error(), "TaskTypeFor") {
		t.Errorf("Registration without a task type = %v, want an error naming both ways to declare one", err)
	}
}
