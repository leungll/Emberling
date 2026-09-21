package domain

import (
	"encoding/json"
	"time"
)

// Definition is one immutable Workflow version. Saving produces a version; a Run binds
// to that version for its whole life and recovery never re-reads a newer one.
//
// JSON tags are the wire names of the Definition contract, so the same struct serialises
// to what Studio sends and reads.
type Definition struct {
	WorkflowID     string          `json:"workflowId"`
	Version        int             `json:"version"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Nodes          []Node          `json:"nodes"`
	Edges          []Edge          `json:"edges"`
	RunInputSchema json.RawMessage `json:"runInputSchema"`
	Validation     Validation      `json:"validation"`
	CreatedAt      time.Time       `json:"createdAt"`
}

// Node is one execution unit in a Definition. Position serves Studio only and takes no
// part in scheduling.
type Node struct {
	ID              string           `json:"id"`
	Type            string           `json:"type"`
	Name            string           `json:"name"`
	Position        Position         `json:"position"`
	Config          json.RawMessage  `json:"config"`
	ExecutionPolicy *ExecutionPolicy `json:"executionPolicy,omitempty"`
}

// Edge is a data dependency between two node handles.
type Edge struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	SourceHandle string `json:"sourceHandle"`
	Target       string `json:"target"`
	TargetHandle string `json:"targetHandle"`
}

// Position is canvas geometry.
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Validation is the frozen compile result of a saved version. MVP allows only VALID;
// a failed Save creates no version at all.
type Validation struct {
	Status ValidationStatus `json:"status"`
	// ValidatorVersion identifies the whole Backend validation contract, including the
	// runInputSchema generation algorithm. Changing the algorithm requires a new value.
	ValidatorVersion string    `json:"validatorVersion"`
	ValidatedAt      time.Time `json:"validatedAt"`
}

type ValidationStatus string

const ValidationValid ValidationStatus = "VALID"

func (s ValidationStatus) IsValid() bool { return s == ValidationValid }

// ExecutionPolicy configures timeout, retry count and backoff for a plain node. Whether
// those retries may actually fire is decided by the registered SideEffectPolicy.
type ExecutionPolicy struct {
	TimeoutMs   int64       `json:"timeoutMs"`
	MaxAttempts int         `json:"maxAttempts"`
	Backoff     BackoffKind `json:"backoff"`
}

type BackoffKind string

const (
	BackoffFixed       BackoffKind = "FIXED"
	BackoffExponential BackoffKind = "EXPONENTIAL"
)

func (b BackoffKind) IsValid() bool {
	return b == BackoffFixed || b == BackoffExponential
}

// SideEffectPolicy is declared by an extension registration and tells the Runtime whether
// repeating a call is safe. EXTERNAL + UNKNOWN forbids automatic re-dispatch once the
// result is uncertain.
type SideEffectPolicy struct {
	Kind        SideEffectKind  `json:"kind"`
	Idempotency IdempotencyMode `json:"idempotency"`
}

type SideEffectKind string

const (
	SideEffectNone     SideEffectKind = "NONE"
	SideEffectExternal SideEffectKind = "EXTERNAL"
)

func (k SideEffectKind) IsValid() bool {
	return k == SideEffectNone || k == SideEffectExternal
}

type IdempotencyMode string

const (
	IdempotencySafe    IdempotencyMode = "SAFE"
	IdempotencyKeyed   IdempotencyMode = "KEYED"
	IdempotencyUnknown IdempotencyMode = "UNKNOWN"
)

func (m IdempotencyMode) IsValid() bool {
	switch m {
	case IdempotencySafe, IdempotencyKeyed, IdempotencyUnknown:
		return true
	default:
		return false
	}
}

// AssetRef is the immutable reference Run input keeps instead of binary content.
type AssetRef struct {
	AssetID   string `json:"assetId"`
	MediaType string `json:"mediaType"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}

// ArtifactRef is the internal persistence form for large recovery values held outside
// PostgreSQL. It is not a public Studio input type or a separate product interface.
type ArtifactRef struct {
	ArtifactID string `json:"artifactId"`
	MediaType  string `json:"mediaType"`
	SizeBytes  int64  `json:"sizeBytes"`
	SHA256     string `json:"sha256"`
}
