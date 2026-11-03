package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MaxChangeRequestBasisItems bounds the decision basis one change request cites.
const MaxChangeRequestBasisItems = 32

// ChangeRequest is the execution fact that binds an Agent Run's patch, named by digest, to
// the verification fact it passed and to the decision basis the Agent cites. Its rendered
// content is a text Execution Artifact, named by id together with the digest of those
// bytes; the row never carries the content itself. A change request is written once, in the
// Tool result transaction that accepted it, and is never updated. Within one Agent Run two
// requests with the same content digest are one record.
type ChangeRequest struct {
	ID                 string `json:"id"`
	AgentRunID         string `json:"agentRunId"`
	PatchDigest        string `json:"patchDigest"`
	VerificationFactID string `json:"verificationFactId"`
	ContentArtifactID  string `json:"contentArtifactId"`
	// ContentDigest is the lowercase hex SHA-256 of the rendered content bytes, the same
	// digest the content Artifact is addressed by.
	ContentDigest string `json:"contentDigest"`
	// DecisionBasis is the JSON array of ChangeRequestBasis items the request cites.
	DecisionBasis json.RawMessage `json:"decisionBasis"`
	ToolAttemptID string          `json:"toolAttemptId"`
	CreatedAt     time.Time       `json:"createdAt"`
}

// ChangeRequestBasis is one item of a change request's decision basis: a statement the
// Agent relied on, and the Context Manifest item of the named Turn that holds the material
// it rests on. ManifestRef is the item's reference inside that Turn's Manifest, so an
// approver can open the original material from the request.
type ChangeRequestBasis struct {
	Statement   string `json:"statement"`
	TurnNo      int    `json:"turnNo"`
	ManifestRef string `json:"manifestRef"`
}

// DecodeChangeRequestBasis reads a decision basis from its JSON array. It accepts the array
// a change-request call carries as its `decisionBasis` argument and the array a recorded
// request stores, and rejects anything that is not a bounded array of complete items.
func DecodeChangeRequestBasis(raw json.RawMessage) ([]ChangeRequestBasis, error) {
	if len(raw) == 0 {
		return nil, errors.New("decision basis is empty")
	}
	var items []ChangeRequestBasis
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("decision basis is not a JSON array of items: %w", err)
	}
	if len(items) == 0 {
		return nil, errors.New("decision basis has no items")
	}
	if len(items) > MaxChangeRequestBasisItems {
		return nil, fmt.Errorf("decision basis has %d items, over the %d-item bound", len(items), MaxChangeRequestBasisItems)
	}
	for i, item := range items {
		switch {
		case item.Statement == "":
			return nil, fmt.Errorf("decision basis item %d has no statement", i)
		case item.TurnNo < 1:
			return nil, fmt.Errorf("decision basis item %d names turn %d, want a turn number from 1", i, item.TurnNo)
		case item.ManifestRef == "":
			return nil, fmt.Errorf("decision basis item %d names no manifest item", i)
		}
	}
	return items, nil
}
