package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCallbackTargetType_IsValid_AcceptsDocumentedValues(t *testing.T) {
	for _, target := range []CallbackTargetType{CallbackTargetNodeAttempt, CallbackTargetToolAttempt} {
		if !target.IsValid() {
			t.Fatalf("CallbackTargetType(%q).IsValid() = false, want true", target)
		}
	}
}

func TestCallbackTargetType_IsValid_RejectsUnknownValue(t *testing.T) {
	cases := []struct {
		name   string
		target CallbackTargetType
	}{
		{"empty", CallbackTargetType("")},
		{"lowercase", CallbackTargetType("node_attempt")},
		{"aggregate rather than attempt", CallbackTargetType("NODE_RUN")},
		{"agent turn is not a callback target", CallbackTargetType("AGENT_TURN")},
		{"unknown provider concept", CallbackTargetType("EXTERNAL_TASK")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.target.IsValid() {
				t.Fatalf("CallbackTargetType(%q).IsValid() = true, want false", tc.target)
			}
		})
	}
}

func TestPendingCallback_MarshalJSON_OmitsCallbackTokenHash(t *testing.T) {
	received := time.Date(2026, 7, 10, 12, 0, 2, 900_000_000, time.UTC)
	pending := PendingCallback{
		ExternalTaskID:    "provider_task_789",
		Payload:           json.RawMessage(`{"imageUrl":"https://example.test/a.png"}`),
		PayloadHash:       "sha256:payload",
		CallbackTokenHash: "sha256:token",
		ReceivedAt:        received,
		ExpiresAt:         received.Add(24 * time.Hour),
		DuplicateCount:    0,
	}

	encoded, err := json.Marshal(pending)
	if err != nil {
		t.Fatalf("json.Marshal(PendingCallback) returned error: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(PendingCallback) returned error: %v", err)
	}
	if _, ok := decoded["callbackTokenHash"]; ok {
		t.Fatalf("PendingCallback JSON contains callbackTokenHash: %s", encoded)
	}
	if _, ok := decoded["callback_token_hash"]; ok {
		t.Fatalf("PendingCallback JSON contains callback_token_hash: %s", encoded)
	}
	if _, ok := decoded["payload"]; !ok {
		t.Fatalf("PendingCallback JSON is missing payload: %s", encoded)
	}
}

func TestCallbackBinding_MarshalJSON_ContainsNoSecretFields(t *testing.T) {
	binding := CallbackBinding{
		ID:             "binding_123",
		ProviderID:     "image-provider-v1",
		ExternalTaskID: "provider_task_789",
		TargetType:     CallbackTargetToolAttempt,
		TargetID:       "tool_attempt_123",
		CreatedAt:      time.Date(2026, 8, 3, 12, 0, 3, 0, time.UTC),
	}

	encoded, err := json.Marshal(binding)
	if err != nil {
		t.Fatalf("json.Marshal(CallbackBinding) returned error: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(CallbackBinding) returned error: %v", err)
	}

	want := map[string]bool{
		"id":             true,
		"providerId":     true,
		"externalTaskId": true,
		"targetType":     true,
		"targetId":       true,
		"createdAt":      true,
	}
	for key := range decoded {
		if !want[key] {
			t.Fatalf("CallbackBinding JSON has unexpected key %q: %s", key, encoded)
		}
	}
	for key := range want {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("CallbackBinding JSON is missing key %q: %s", key, encoded)
		}
	}
	if len(decoded) != len(want) {
		t.Fatalf("CallbackBinding JSON has %d keys, want %d: %s", len(decoded), len(want), encoded)
	}
}
