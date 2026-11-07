package externaltool_test

import (
	"context"
	"encoding/json"
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
