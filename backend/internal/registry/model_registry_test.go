package registry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// stubModelProvider returns a fixed catalogue and never calls an external system.
type stubModelProvider struct {
	models    []ModelRegistration
	modelsErr error
}

func (p *stubModelProvider) Models(ctx context.Context) ([]ModelRegistration, error) {
	return p.models, p.modelsErr
}

func (p *stubModelProvider) Generate(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	return ModelResponse{}, nil
}

func validModelRegistration(id string) ModelRegistration {
	return ModelRegistration{ModelMetadata: domain.ModelMetadata{
		ID:           id,
		DisplayName:  "Text Model",
		Capabilities: []string{domain.ModelCapabilityTextGeneration},
		ConfigSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
	}}
}

func TestModelRegistry_Register_ValidCatalogueSucceeds(t *testing.T) {
	r := NewModelRegistry()
	provider := &stubModelProvider{models: []ModelRegistration{validModelRegistration("text-model-v1")}}
	if err := r.Register(context.Background(), provider); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
	got, gotProvider, ok := r.Get("text-model-v1")
	if !ok {
		t.Fatal("Get() ok = false, want true")
	}
	if got.ID != "text-model-v1" {
		t.Fatalf("Get().ID = %q, want text-model-v1", got.ID)
	}
	if gotProvider != provider {
		t.Fatal("Get() provider mismatch")
	}
}

func TestModelRegistry_Register_NilProviderRejected(t *testing.T) {
	if err := NewModelRegistry().Register(context.Background(), nil); err == nil {
		t.Fatal("Register() error = nil, want error for nil provider")
	}
}

func TestModelRegistry_Register_ModelsErrorPropagates(t *testing.T) {
	wantErr := errors.New("catalogue unavailable")
	provider := &stubModelProvider{modelsErr: wantErr}
	if err := NewModelRegistry().Register(context.Background(), provider); !errors.Is(err, wantErr) {
		t.Fatalf("Register() error = %v, want wrapped %v", err, wantErr)
	}
}

func TestModelRegistry_Register_EmptyIDRejected(t *testing.T) {
	reg := validModelRegistration("")
	provider := &stubModelProvider{models: []ModelRegistration{reg}}
	if err := NewModelRegistry().Register(context.Background(), provider); err == nil {
		t.Fatal("Register() error = nil, want error for empty model id")
	}
}

func TestModelRegistry_Register_DuplicateIDWithinResponseRejected(t *testing.T) {
	provider := &stubModelProvider{models: []ModelRegistration{
		validModelRegistration("text-model-v1"),
		validModelRegistration("text-model-v1"),
	}}
	if err := NewModelRegistry().Register(context.Background(), provider); err == nil {
		t.Fatal("Register() error = nil, want error for duplicate id in one response")
	}
}

func TestModelRegistry_Register_DuplicateIDAcrossProvidersRejected(t *testing.T) {
	r := NewModelRegistry()
	first := &stubModelProvider{models: []ModelRegistration{validModelRegistration("text-model-v1")}}
	if err := r.Register(context.Background(), first); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}
	second := &stubModelProvider{models: []ModelRegistration{validModelRegistration("text-model-v1")}}
	if err := r.Register(context.Background(), second); err == nil {
		t.Fatal("second Register() error = nil, want duplicate id error")
	}
}

func TestModelRegistry_Register_UnparsableConfigSchemaRejected(t *testing.T) {
	reg := validModelRegistration("text-model-v1")
	reg.ConfigSchema = json.RawMessage(`{"type": 123}`)
	provider := &stubModelProvider{models: []ModelRegistration{reg}}
	if err := NewModelRegistry().Register(context.Background(), provider); err == nil {
		t.Fatal("Register() error = nil, want configSchema compile error")
	}
}

func TestModelRegistry_Register_PartialFailureRegistersNothing(t *testing.T) {
	r := NewModelRegistry()
	bad := validModelRegistration("bad-model")
	bad.ConfigSchema = json.RawMessage(`{"type": 123}`)
	provider := &stubModelProvider{models: []ModelRegistration{
		validModelRegistration("good-model"),
		bad,
	}}
	if err := r.Register(context.Background(), provider); err == nil {
		t.Fatal("Register() error = nil, want error from bad-model")
	}
	if _, _, ok := r.Get("good-model"); ok {
		t.Fatal("Get(\"good-model\") ok = true, want false: a failed Register must not partially register")
	}
}

func TestModelRegistry_Get_UnknownIDReturnsFalse(t *testing.T) {
	if _, _, ok := NewModelRegistry().Get("does-not-exist"); ok {
		t.Fatal("Get() ok = true, want false")
	}
}

func TestModelRegistry_ListMetadata_SortedByID(t *testing.T) {
	r := NewModelRegistry()
	provider := &stubModelProvider{models: []ModelRegistration{
		validModelRegistration("text-model-v2"),
		validModelRegistration("text-model-v1"),
	}}
	if err := r.Register(context.Background(), provider); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	list := r.ListMetadata()
	if len(list) != 2 || list[0].ID != "text-model-v1" || list[1].ID != "text-model-v2" {
		t.Fatalf("ListMetadata() = %v, want sorted by id", list)
	}
}
