package postgres

import (
	"encoding/json"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// nullableJSON maps an empty raw message to SQL NULL so that "absent" and "the JSON
// literal null" do not collapse into the same stored value.
func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

func marshalExecutionError(execErr *domain.ExecutionError) (any, error) {
	if execErr == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(execErr)
	if err != nil {
		return nil, fmt.Errorf("marshal execution error: %w", err)
	}
	return encoded, nil
}

func unmarshalExecutionError(raw []byte) (*domain.ExecutionError, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var execErr domain.ExecutionError
	if err := json.Unmarshal(raw, &execErr); err != nil {
		return nil, fmt.Errorf("unmarshal execution error: %w", err)
	}
	return &execErr, nil
}

func marshalTokenUsage(usage *domain.TokenUsage) (any, error) {
	if usage == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(usage)
	if err != nil {
		return nil, fmt.Errorf("marshal token usage: %w", err)
	}
	return encoded, nil
}

func unmarshalTokenUsage(raw []byte) (*domain.TokenUsage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var usage domain.TokenUsage
	if err := json.Unmarshal(raw, &usage); err != nil {
		return nil, fmt.Errorf("unmarshal token usage: %w", err)
	}
	return &usage, nil
}
