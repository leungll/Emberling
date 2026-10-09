package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// IDPrefixExecutionFact is the stable prefix of Execution Fact identifiers.
const IDPrefixExecutionFact = "fact"

// ErrExecutionFactDuplicate is returned when a Tool Attempt already recorded a fact of
// the same type. The writing transaction must roll back: the conflict is never ignored,
// because it means the same Tool result is being committed twice.
var ErrExecutionFactDuplicate = fmt.Errorf("emberling: execution fact already recorded for this tool attempt and fact type: %w", ErrConflict)

// ExecutionFact is a trusted fact a successful Tool Attempt produced under its
// registered declaration. Protected Tool preconditions and delivery checks read only
// committed facts; they never reinterpret historical Tool results or derive facts from
// Events. A fact is written once, in the Tool result transaction, and never updated.
type ExecutionFact struct {
	ID         string `json:"id"`
	RunID      string `json:"runId"`
	AgentRunID string `json:"agentRunId"`
	// ToolAttemptID names the Attempt whose result produced the fact. Every fact has
	// one; there is no manual source.
	ToolAttemptID string `json:"toolAttemptId"`
	// FactType is a registered string, validated by the Registry rather than by a
	// database enum.
	FactType   string `json:"factType"`
	SubjectRef string `json:"subjectRef"`
	// Binding is a bounded JSON object of references and digests. It never carries
	// credentials, callback tokens or signed URLs. An empty value is stored as {}.
	Binding json.RawMessage `json:"binding"`
	// Verdict is nil for facts that record no conclusion.
	Verdict *bool `json:"verdict"`
	// BasisFactID links a review fact to the generation fact it reviewed.
	BasisFactID *string   `json:"basisFactId"`
	CreatedAt   time.Time `json:"createdAt"`
}
