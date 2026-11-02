package registry

import (
	"encoding/json"
	"strings"
	"testing"
)

// The dispatch model declaration lets the Runtime read, from a frozen node config, the
// Model ID an ASYNC node dispatches to. It must therefore name a string property of the
// ConfigSchema of an ASYNC node; anything else is refused at registration so the Runtime
// never reads a property that does not exist.

func TestNodeRegistry_Register_DispatchModelPathStringProperty_Accepted(t *testing.T) {
	reg := validAsyncNodeRegistration()
	reg.Metadata.DispatchModelPath = "label"
	if err := NewNodeRegistry().Register(reg); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
}

func TestNodeRegistry_Register_DispatchModelPathOnSyncNode_Rejected(t *testing.T) {
	reg := validSyncNodeRegistration()
	reg.Metadata.DispatchModelPath = "label"
	err := NewNodeRegistry().Register(reg)
	if err == nil || !strings.Contains(err.Error(), "dispatchModelPath is only allowed with executionKind ASYNC") {
		t.Fatalf("Register() error = %v, want the ASYNC-only rule", err)
	}
}

func TestNodeRegistry_Register_DispatchModelPathUndeclared_Rejected(t *testing.T) {
	reg := validAsyncNodeRegistration()
	reg.Metadata.DispatchModelPath = "modelId"
	err := NewNodeRegistry().Register(reg)
	if err == nil || !strings.Contains(err.Error(), `dispatchModelPath "modelId"`) {
		t.Fatalf("Register() error = %v, want the undeclared property reported", err)
	}
}

func TestNodeRegistry_Register_DispatchModelPathNonString_Rejected(t *testing.T) {
	reg := validAsyncNodeRegistration()
	reg.Metadata.DispatchModelPath = "required"
	err := NewNodeRegistry().Register(reg)
	if err == nil || !strings.Contains(err.Error(), "must name a string property") {
		t.Fatalf("Register() error = %v, want the string-property rule", err)
	}
}

func TestNodeMetadata_DispatchModelPath_StaysOffTheWire(t *testing.T) {
	reg := validAsyncNodeRegistration()
	reg.Metadata.DispatchModelPath = "label"
	r := NewNodeRegistry()
	if err := r.Register(reg); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	listed := r.ListMetadata()
	if len(listed) != 1 || listed[0].DispatchModelPath != "label" {
		t.Fatalf("ListMetadata() = %+v, want the registration with its dispatch model path", listed)
	}
	// The catalog response is the JSON encoding of the listed metadata: the dispatch
	// model path is registration-internal and must not appear in it under any key.
	encoded, err := json.Marshal(listed[0])
	if err != nil {
		t.Fatalf("json.Marshal(NodeMetadata) error = %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &keys); err != nil {
		t.Fatalf("decode encoded metadata: %v", err)
	}
	for key, value := range keys {
		if strings.Contains(strings.ToLower(key), "dispatch") || string(value) == `"label"` {
			t.Fatalf("encoded NodeMetadata exposes the dispatch model path under %q", key)
		}
	}
}
