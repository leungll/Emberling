package domain

import (
	"encoding/json"
	"time"
)

// CallbackBinding is the only authoritative route from an external task identity back to
// the Attempt that dispatched it. It is immutable once committed: a handler resolves a
// target by querying ExternalTaskID, never by inspecting ID shape, in-process state or
// Event payloads. The binding carries no credential; the plaintext callback token is sent
// to the Provider and never stored, and even its hash lives only on the Attempt.
type CallbackBinding struct {
	ID string `json:"id"`
	// ProviderID is the stable Provider identity actually used for this dispatch. It
	// supports recovery, optional polling and Trace display, and is not a credential.
	ProviderID     string             `json:"providerId"`
	ExternalTaskID string             `json:"externalTaskId"`
	TargetType     CallbackTargetType `json:"targetType"`
	TargetID       string             `json:"targetId"`
	CreatedAt      time.Time          `json:"createdAt"`
}

// PendingCallback holds an authenticated callback that arrived before its Callback
// Binding committed. ExternalTaskID is the identity; a duplicate delivery only bumps
// DuplicateCount and ReceivedAt and never overwrites the first valid Payload. Consumption
// is a conditional update on ConsumedAt so the callback handler and the post-commit check
// cannot both consume the same record.
type PendingCallback struct {
	ExternalTaskID string          `json:"externalTaskId"`
	Payload        json.RawMessage `json:"payload"`
	PayloadHash    string          `json:"payloadHash"`
	// CallbackTokenHash is the only persisted form of the dispatch credential. The
	// plaintext token is sent to the Provider and never stored.
	CallbackTokenHash string     `json:"-"`
	ReceivedAt        time.Time  `json:"receivedAt"`
	ExpiresAt         time.Time  `json:"expiresAt"`
	ConsumedAt        *time.Time `json:"consumedAt"`
	DuplicateCount    int        `json:"duplicateCount"`
}
