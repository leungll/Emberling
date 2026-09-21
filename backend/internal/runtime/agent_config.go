package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// AgentNodeConfig is the fixed Definition-side shape of an `agent` Node's config
// (docs/08-interface-spec.md §2.1). It is a pure parse/validate result: it carries no
// execution state and is not itself the immutable AgentRun record (docs/05-data-model.md
// §2, AgentRun) -- that record is only created when a Run is created, from these same
// fields plus the identifiers Service resolves through the Registry.
type AgentNodeConfig struct {
	Instructions  string          `json:"instructions,omitempty"`
	ModelID       string          `json:"modelId"`
	ModelConfig   json.RawMessage `json:"modelConfig,omitempty"`
	AllowedTools  []string        `json:"allowedTools,omitempty"`
	ContextSchema json.RawMessage `json:"contextSchema,omitempty"`
	StateSchema   json.RawMessage `json:"stateSchema,omitempty"`
	OutputSchema  json.RawMessage `json:"outputSchema,omitempty"`
	MaxTurns      int             `json:"maxTurns"`
	TimeoutMs     int             `json:"timeoutMs"`
}

// AgentConfigError is one `agent` Node config violation found by ParseAgentNodeConfig.
// Field, when non-empty, names the offending top-level config field, so the Compiler's
// Semantics stage can point a ValidationError.Path at it (mirroring how ConfigSchema
// violations already point at a JSON-pointer instance location).
type AgentConfigError struct {
	Field   string
	Message string
}

func (e *AgentConfigError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("agent config: %s: %s", e.Field, e.Message)
	}
	return fmt.Sprintf("agent config: %s", e.Message)
}

// ParseAgentNodeConfig parses and validates one `agent` Node's raw config against the
// fixed shape docs/08-interface-spec.md §2.1 defines. Beyond the struct shape itself
// (unknown fields rejected), it checks the three invariants ConfigSchema alone cannot
// express:
//
//   - maxTurns and timeoutMs are both present and >= 1 (docs/05-data-model.md §2 AgentRun
//     invariants).
//   - contextSchema, stateSchema and outputSchema, when present, are each a compilable
//     JSON Schema document.
//   - stateSchema, when present, accepts `{}` -- an Agent Run's State Version 0
//     (docs/05-data-model.md §2 "State Version 0 = {}").
//   - outputSchema, when present, accepts a JSON string instance -- the Agent's single
//     `text` output port has no other MVP shape (docs/08-interface-spec.md §2.1 "Agent
//     的 OutputSchema 在 MVP 中必须接受 string").
//
// It does not resolve modelId against the Model Registry or allowedTools against the Tool
// Registry: runtime is pure and holds no Registry access (CLAUDE.md "Package boundaries");
// that resolution belongs to service.DefinitionService.
func ParseAgentNodeConfig(raw json.RawMessage) (AgentNodeConfig, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cfg AgentNodeConfig
	if err := dec.Decode(&cfg); err != nil {
		return AgentNodeConfig{}, &AgentConfigError{Message: "config has an unknown or malformed field: " + err.Error()}
	}

	if cfg.ModelID == "" {
		return AgentNodeConfig{}, &AgentConfigError{Field: "modelId", Message: "modelId is required"}
	}
	if cfg.MaxTurns < 1 {
		return AgentNodeConfig{}, &AgentConfigError{Field: "maxTurns", Message: "maxTurns must be >= 1"}
	}
	if cfg.TimeoutMs < 1 {
		return AgentNodeConfig{}, &AgentConfigError{Field: "timeoutMs", Message: "timeoutMs must be >= 1"}
	}

	if len(bytes.TrimSpace(cfg.ContextSchema)) > 0 {
		if _, err := compileAgentSchema(cfg.ContextSchema); err != nil {
			return AgentNodeConfig{}, &AgentConfigError{Field: "contextSchema", Message: "contextSchema is not a usable JSON Schema: " + err.Error()}
		}
	}

	if len(bytes.TrimSpace(cfg.StateSchema)) > 0 {
		if err := validateAgainstSchema(cfg.StateSchema, json.RawMessage(`{}`)); err != nil {
			return AgentNodeConfig{}, &AgentConfigError{Field: "stateSchema", Message: "stateSchema must accept the Agent Run's initial state {}: " + err.Error()}
		}
	}

	if len(bytes.TrimSpace(cfg.OutputSchema)) > 0 {
		probe, _ := json.Marshal("")
		if err := validateAgainstSchema(cfg.OutputSchema, probe); err != nil {
			return AgentNodeConfig{}, &AgentConfigError{Field: "outputSchema", Message: "outputSchema must accept a string output value: " + err.Error()}
		}
	}

	return cfg, nil
}

// compileAgentSchema confirms rawSchema is a usable JSON Schema document, with no instance
// to validate against: contextSchema describes the Agent's Context shape, which is only
// ever checked against the actual Context an Agent Run receives, never at Definition time.
func compileAgentSchema(rawSchema json.RawMessage) (*jsonschema.Schema, error) {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawSchema))
	if err != nil {
		return nil, fmt.Errorf("schema is not valid JSON: %w", err)
	}
	const schemaURL = "mem://agent-node-config-schema"
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaURL, document); err != nil {
		return nil, fmt.Errorf("schema is not usable: %w", err)
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("schema is not usable: %w", err)
	}
	return schema, nil
}
