package registry

import (
	"context"
	"strings"
	"testing"
)

func TestModelRegistry_Register_DescriptionWithinBound_Served(t *testing.T) {
	reg := validModelRegistration("text-model-v1")
	reg.Description = strings.Repeat("é", 280)
	r := NewModelRegistry()
	if err := r.Register(context.Background(), &stubModelProvider{models: []ModelRegistration{reg}}); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
	if got := r.ListMetadata()[0].Description; got != reg.Description {
		t.Errorf("ListMetadata()[0].Description has %d characters, want the registered 280", len([]rune(got)))
	}
}

// TestModelRegistry_Register_DescriptionOverBoundOrMultiline_Rejected covers a description
// that does not come from validated configuration, such as one written in a Provider: the
// Registry refuses it, so the catalogue never serves an unbounded or multi-line value.
func TestModelRegistry_Register_DescriptionOverBoundOrMultiline_Rejected(t *testing.T) {
	for _, tc := range []struct {
		name        string
		description string
	}{
		{"longer than 280 characters", strings.Repeat("x", 281)},
		{"contains a line break", "first line\nsecond line"},
		{"contains a control character", "bell\a"},
		{"not valid UTF-8", "bad \xff byte"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := validModelRegistration("text-model-v1")
			reg.Description = tc.description
			r := NewModelRegistry()
			err := r.Register(context.Background(), &stubModelProvider{models: []ModelRegistration{reg}})
			if err == nil || !strings.Contains(err.Error(), "description") {
				t.Fatalf("Register() error = %v, want a refusal naming the description", err)
			}
			if strings.Contains(err.Error(), tc.description) {
				t.Errorf("Register() error repeats the description: %q", err.Error())
			}
			if _, _, ok := r.Get("text-model-v1"); ok {
				t.Error("Get() ok = true, want the refused model unregistered")
			}
		})
	}
}
