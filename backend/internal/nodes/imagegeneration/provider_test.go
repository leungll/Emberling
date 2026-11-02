package imagegeneration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// providerByModel resolves every model it knows as an image_generation model declared by
// the mapped Provider ID.
type providerByModel map[string]string

func (r providerByModel) Get(modelID string) (registry.ModelRegistration, registry.ModelProvider, bool) {
	if _, ok := r[modelID]; !ok {
		return registry.ModelRegistration{}, nil, false
	}
	model := imageModel()
	model.ID = modelID
	model.ProviderID = r[modelID]
	return model, nil, true
}

func (r providerByModel) ProviderID(modelID string) (string, error) {
	id, ok := r[modelID]
	if !ok {
		return "", fmt.Errorf("model %q: %w", modelID, registry.ErrModelNotRegistered)
	}
	return id, nil
}

// countingProvider is one task Provider instance that counts what reached it.
type countingProvider struct {
	id         string
	dispatches int
	polls      []string
}

func (p *countingProvider) ProviderID() string { return p.id }

func (p *countingProvider) Dispatch(context.Context, string, *domain.ImageRef, map[string]any, registry.CallbackContext, string) (registry.ExternalTask, error) {
	p.dispatches++
	return registry.ExternalTask{ProviderID: p.id, ExternalTaskID: p.id + "_task"}, nil
}

func (p *countingProvider) Poll(_ context.Context, externalTaskID string) (registry.PollStatus, json.RawMessage, error) {
	p.polls = append(p.polls, externalTaskID)
	return registry.PollRunning, nil, nil
}

func twoProviderExecutor() (Executor, *countingProvider, *countingProvider) {
	mock := &countingProvider{id: "mock-task-provider-v1"}
	gateway := &countingProvider{id: "task-gateway-v1"}
	resolver := providerByModel{"image-model-v1": mock.id, "gateway-image": gateway.id, "orphan-image": "unconfigured-provider"}
	return newTestExecutor(resolver, mock, gateway), mock, gateway
}

func configFor(modelID string) map[string]any {
	return map[string]any{"modelId": modelID, "width": float64(1024)}
}

func TestExecutor_ResolvedProviderID_SelectsMatchingInstance(t *testing.T) {
	e, mock, gateway := twoProviderExecutor()
	callback := registry.CallbackContext{URL: "http://emberling.test/callbacks/attempt_1", Token: "token"}

	result, err := e.Execute(context.Background(), promptInput(&callback, "nr_1"), configFor("gateway-image"))
	if err != nil {
		t.Fatalf("Execute(gateway model) error = %v", err)
	}
	if result.ExternalTask == nil || result.ExternalTask.ProviderID != gateway.id || gateway.dispatches != 1 || mock.dispatches != 0 {
		t.Fatalf("gateway model dispatched to %+v (gateway %d, mock %d), want the gateway instance only", result.ExternalTask, gateway.dispatches, mock.dispatches)
	}

	result, err = e.Execute(context.Background(), promptInput(&callback, "nr_2"), configFor("image-model-v1"))
	if err != nil {
		t.Fatalf("Execute(mock model) error = %v", err)
	}
	if result.ExternalTask.ProviderID != mock.id || mock.dispatches != 1 || gateway.dispatches != 1 {
		t.Fatalf("mock model dispatched to %+v (gateway %d, mock %d), want the mock instance only", result.ExternalTask, gateway.dispatches, mock.dispatches)
	}
}

func TestExecutor_Poll_RoutesByBindingProviderID(t *testing.T) {
	e, mock, gateway := twoProviderExecutor()
	state := registry.NodeAsyncState{ExternalTask: registry.ExternalTask{ProviderID: gateway.id, ExternalTaskID: "gw_1"}}

	if _, err := e.Poll(context.Background(), state); err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if len(gateway.polls) != 1 || gateway.polls[0] != "gw_1" || len(mock.polls) != 0 {
		t.Fatalf("polls: gateway %v, mock %v; want only the Binding's Provider asked", gateway.polls, mock.polls)
	}

	// A Binding whose Provider has no instance in this process is not guessed at.
	orphan := registry.NodeAsyncState{ExternalTask: registry.ExternalTask{ProviderID: "unconfigured-provider", ExternalTaskID: "x_1"}}
	if _, err := e.Poll(context.Background(), orphan); err == nil || !strings.Contains(err.Error(), "unconfigured-provider") {
		t.Fatalf("Poll(orphan) error = %v, want an error naming the missing Provider", err)
	}
	if len(gateway.polls) != 1 || len(mock.polls) != 0 {
		t.Fatalf("polls after orphan: gateway %v, mock %v; want no instance asked", gateway.polls, mock.polls)
	}
}

func TestExecutor_ProviderInstanceMissing_FailsBeforeDispatch(t *testing.T) {
	e, mock, gateway := twoProviderExecutor()
	callback := registry.CallbackContext{URL: "http://emberling.test/callbacks/attempt_1", Token: "token"}

	_, err := e.Execute(context.Background(), promptInput(&callback, "nr_1"), configFor("orphan-image"))
	if err == nil || !strings.Contains(err.Error(), "orphan-image") || !strings.Contains(err.Error(), "unconfigured-provider") {
		t.Fatalf("Execute() error = %v, want a failure naming the model and its Provider", err)
	}
	if isUncertainFailure(err) {
		t.Fatalf("Execute() error = %v is uncertain, want a definite failure: nothing was sent", err)
	}
	if mock.dispatches != 0 || gateway.dispatches != 0 {
		t.Fatalf("dispatches: mock %d, gateway %d; want none and no fallback", mock.dispatches, gateway.dispatches)
	}
}

func TestValidateSemantics_ProviderInstanceMissing_Rejected(t *testing.T) {
	e, _, _ := twoProviderExecutor()
	err := e.ValidateSemantics(context.Background(), configFor("orphan-image"))
	if err == nil || !strings.Contains(err.Error(), "orphan-image") || !strings.Contains(err.Error(), "unconfigured-provider") {
		t.Fatalf("ValidateSemantics() error = %v, want a rejection naming the model and its Provider", err)
	}
	for _, modelID := range []string{"image-model-v1", "gateway-image"} {
		if err := e.ValidateSemantics(context.Background(), configFor(modelID)); err != nil {
			t.Fatalf("ValidateSemantics(%s) error = %v, want nil", modelID, err)
		}
	}
}

func TestRegistration_DuplicateProviderID_Panics(t *testing.T) {
	for name, providers := range map[string][]TaskProvider{
		"duplicate id": {&countingProvider{id: "p"}, &countingProvider{id: "p"}},
		"empty id":     {&countingProvider{id: ""}},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("Registration() did not panic, want a wiring error at startup")
				}
			}()
			Registration(providerByModel{}, providers...)
		})
	}
}

// isUncertainFailure reads the classification the Execution Service reads.
func isUncertainFailure(err error) bool {
	var uncertain interface{ Uncertain() bool }
	return errors.As(err, &uncertain) && uncertain.Uncertain()
}
