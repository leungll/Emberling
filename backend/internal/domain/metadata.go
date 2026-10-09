package domain

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Extension metadata is the single shared description of a registered Node, Model or
// Tool. Registry validates and serves it, Compiler reads ports and Config contracts from
// it, and the API returns it verbatim. JSON tags are the wire names of the Node Metadata
// contract, so one struct serves Registry, Compiler and Studio.
//
// These types describe a registration; they hold no Executor, Adapter or credential.
// Structural interpretation of ConfigSchema, InputSchema and OutputSchema belongs to the
// JSON Schema compiler in registry, not here.

// NodeExecutionKind tells the Runtime which execution path a Node uses. It is a declared
// fact of the registration, never inferred from an execution result or a Go type.
type NodeExecutionKind string

const (
	// NodeExecutionSync returns a final result from one call.
	NodeExecutionSync NodeExecutionKind = "SYNC"
	// NodeExecutionAsync only dispatches work; the Runtime waits for a callback.
	NodeExecutionAsync NodeExecutionKind = "ASYNC"
	// NodeExecutionManagedAgent runs on the built-in Agent Runtime.
	NodeExecutionManagedAgent NodeExecutionKind = "MANAGED_AGENT"
)

func (k NodeExecutionKind) IsValid() bool {
	switch k {
	case NodeExecutionSync, NodeExecutionAsync, NodeExecutionManagedAgent:
		return true
	default:
		return false
	}
}

// PortType is the data type of one typed Node handle.
type PortType string

const (
	PortTypeText  PortType = "text"
	PortTypeImage PortType = "image"
	PortTypeJSON  PortType = "json"
	PortTypeAny   PortType = "any"
)

func (t PortType) IsValid() bool {
	switch t {
	case PortTypeText, PortTypeImage, PortTypeJSON, PortTypeAny:
		return true
	default:
		return false
	}
}

// IsCompatibleWith reports whether an Edge may carry this source port type into the given
// target port type. Compatibility is directional: an `any` target accepts every source,
// but an `any` source only feeds an `any` target, because the Compiler cannot prove an
// unconstrained value satisfies a concrete downstream port.
func (t PortType) IsCompatibleWith(target PortType) bool {
	if !t.IsValid() || !target.IsValid() {
		return false
	}
	if target == PortTypeAny {
		return true
	}
	return t == target
}

// PortMetadata is one typed handle. Name is the handle identifier used by Edge
// sourceHandle and targetHandle.
type PortMetadata struct {
	Name     string   `json:"name"`
	DataType PortType `json:"dataType"`
	Required bool     `json:"required"`
}

// UIGroup places a config field in one Properties Panel section.
type UIGroup string

const (
	UIGroupBasic           UIGroup = "BASIC"
	UIGroupModel           UIGroup = "MODEL"
	UIGroupModelParameters UIGroup = "MODEL_PARAMETERS"
)

func (g UIGroup) IsValid() bool {
	switch g {
	case UIGroupBasic, UIGroupModel, UIGroupModelParameters:
		return true
	default:
		return false
	}
}

// UIWidget is a display hint. It never changes the accepted config value: ConfigSchema
// stays the only source of field types, required-ness, defaults and enums.
type UIWidget string

const (
	UIWidgetDefault       UIWidget = "DEFAULT"
	UIWidgetTextArea      UIWidget = "TEXTAREA"
	UIWidgetPromptEditor  UIWidget = "PROMPT_EDITOR"
	UIWidgetSelect        UIWidget = "SELECT"
	UIWidgetModelSelector UIWidget = "MODEL_SELECTOR"
	// UIWidgetToolSelector offers a string-array field as a choice among Tool Registry
	// entries. Like MODEL_SELECTOR it only narrows presentation: the Definition validation
	// chain still decides whether each saved Tool name resolves.
	UIWidgetToolSelector UIWidget = "TOOL_SELECTOR"
)

func (w UIWidget) IsValid() bool {
	switch w {
	case UIWidgetDefault, UIWidgetTextArea, UIWidgetPromptEditor, UIWidgetSelect, UIWidgetModelSelector, UIWidgetToolSelector:
		return true
	default:
		return false
	}
}

// UIField describes how Studio renders one ConfigSchema property.
type UIField struct {
	// Path is the property path inside ConfigSchema. Whether the path resolves is
	// checked by the Registry against the compiled schema.
	Path  string  `json:"path"`
	Order int     `json:"order"`
	Group UIGroup `json:"group"`
	// Widget is the control hint; Capability filters the Model Selector list and must be
	// empty for every other Widget.
	Widget     UIWidget `json:"widget"`
	Capability string   `json:"capability,omitempty"`
}

// NodeUISchema carries Properties Panel presentation only. Fields absent from it fall
// back to the ConfigSchema default control.
type NodeUISchema struct {
	Fields []UIField `json:"fields"`
}

// Palette groups. Category is a display label rather than a Runtime enum: it groups
// cards in the Node Palette and takes no part in scheduling or validation.
const (
	NodeCategoryInput          = "Input"
	NodeCategoryPromptAndModel = "Prompt & Model"
	NodeCategoryAgent          = "Agent"
	NodeCategoryOutput         = "Output"
)

// NodeMetadata is the registered description of one Node Type.
type NodeMetadata struct {
	Type          string            `json:"type"`
	DisplayName   string            `json:"displayName"`
	Category      string            `json:"category"`
	ExecutionKind NodeExecutionKind `json:"executionKind"`
	Inputs        []PortMetadata    `json:"inputs"`
	Outputs       []PortMetadata    `json:"outputs"`
	ConfigSchema  json.RawMessage   `json:"configSchema"`
	UISchema      NodeUISchema      `json:"uiSchema"`
	SideEffect    SideEffectPolicy  `json:"sideEffect"`
	// Poll declares that the Executor can query the Provider for an async task's status,
	// and bounds how often and how many times the Runtime may ask. Nil means the Node
	// Type is never polled; only an ASYNC Node may declare it.
	Poll *PollPolicy `json:"poll,omitempty"`
	// FactInputs lists the execution fact types of the current Run this Node Type reads.
	// Empty means the Node reads no execution facts.
	FactInputs []string `json:"factInputs"`
}

// Validate checks the structural rules a registration must satisfy before the Backend can
// become ready. It reports every violation at once so a broken registration does not need
// one restart per mistake. JSON Schema semantics, UI path resolution and Binding
// agreement stay with the Registry, which owns the schema compiler and the Binding.
func (m NodeMetadata) Validate() error {
	var errs []error

	if m.Type == "" {
		errs = append(errs, errors.New("type is empty"))
	}
	if !m.ExecutionKind.IsValid() {
		errs = append(errs, fmt.Errorf("executionKind %q is not a registered execution kind", m.ExecutionKind))
	}
	errs = append(errs, validatePorts("input", m.Inputs)...)
	errs = append(errs, validatePorts("output", m.Outputs)...)
	if err := validateSchemaField("configSchema", m.ConfigSchema); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, validateUISchema(m.UISchema)...)
	errs = append(errs, validateSideEffect(m.SideEffect)...)
	errs = append(errs, validatePollPolicy(m.Poll, m.ExecutionKind == NodeExecutionAsync)...)
	errs = append(errs, validateFactInputs(m.FactInputs)...)

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid node metadata %q: %w", m.Type, errors.Join(errs...))
}

// Declared model result capabilities. Studio filters the Model Selector by them and an
// Agent model must declare ModelCapabilityStructuredDecision, meaning it can return fixed
// JSON for the DecisionSchema. A capability names the result Emberling needs, not a
// Provider-native API. The list stays open: an Adapter may declare a capability this
// package does not name.
const (
	ModelCapabilityTextGeneration     = "text_generation"
	ModelCapabilityImageGeneration    = "image_generation"
	ModelCapabilityStructuredDecision = "structured_decision"
)

// ModelMetadata is the registered description of one stable Model ID. ConfigSchema is the
// only contract for model parameters; defaults are resolved once before a Definition
// version is frozen and are never re-injected at Run or recovery time.
type ModelMetadata struct {
	ID           string          `json:"id"`
	DisplayName  string          `json:"displayName"`
	Capabilities []string        `json:"capabilities"`
	ConfigSchema json.RawMessage `json:"configSchema"`
}

// PollPolicy bounds Provider status polling for one async registration. Both bounds come
// from the registration itself: the Runtime never substitutes a process default, and
// polling never extends an Attempt's deadline, which still ends a task that no poll or
// callback resolved.
type PollPolicy struct {
	// IntervalMs is the minimum delay between two status queries for one Attempt.
	IntervalMs int64 `json:"intervalMs"`
	// MaxPolls is the most status queries the Runtime issues for one Attempt.
	MaxPolls int `json:"maxPolls"`
}

// validatePollPolicy checks an optional poll declaration. Polling only queries a task an
// async dispatch already created, so a declaration on any other execution kind is a
// registration error rather than something to ignore.
func validatePollPolicy(policy *PollPolicy, async bool) []error {
	if policy == nil {
		return nil
	}
	var errs []error
	if !async {
		errs = append(errs, errors.New("poll is only allowed with executionKind ASYNC"))
	}
	if policy.IntervalMs <= 0 {
		errs = append(errs, fmt.Errorf("poll.intervalMs must be greater than 0, got %d", policy.IntervalMs))
	}
	if policy.MaxPolls <= 0 {
		errs = append(errs, fmt.Errorf("poll.maxPolls must be greater than 0, got %d", policy.MaxPolls))
	}
	return errs
}

// ToolExecutionKind tells the Agent Runtime whether a Tool call finishes in one
// invocation or waits for a callback.
type ToolExecutionKind string

const (
	ToolExecutionSync  ToolExecutionKind = "SYNC"
	ToolExecutionAsync ToolExecutionKind = "ASYNC"
)

func (k ToolExecutionKind) IsValid() bool {
	return k == ToolExecutionSync || k == ToolExecutionAsync
}

// ToolMetadata is the registered description of one stable Tool Name. It is the only
// source of the Tool's schemas, execution mode and side effect policy; the Runtime never
// infers them from an Executor's Go type or from a returned result.
type ToolMetadata struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	InputSchema   json.RawMessage   `json:"inputSchema"`
	OutputSchema  json.RawMessage   `json:"outputSchema"`
	SideEffect    SideEffectPolicy  `json:"sideEffect"`
	ExecutionKind ToolExecutionKind `json:"executionKind"`
	// Poll has the same meaning as NodeMetadata.Poll; only an ASYNC Tool may declare it.
	Poll *PollPolicy `json:"poll,omitempty"`
	// Produces declares the fact a successful result of this Tool establishes. Nil means
	// the Tool produces no fact.
	Produces *FactProduction `json:"produces,omitempty"`
	// Requires lists the committed facts an Action of this Tool needs before it is
	// claimed. Empty means the Tool is not guarded by any fact.
	Requires []FactRequirement `json:"requires"`
	// CountsTowardGenerationLimit marks a Tool whose calls count against an Agent's
	// generation call limit.
	CountsTowardGenerationLimit bool `json:"countsTowardGenerationLimit"`
}

// Validate mirrors NodeMetadata.Validate for Tool registrations. Executor presence and
// agreement with ExecutionKind stay with the Registry.
func (m ToolMetadata) Validate() error {
	var errs []error

	if m.Name == "" {
		errs = append(errs, errors.New("name is empty"))
	}
	if !m.ExecutionKind.IsValid() {
		errs = append(errs, fmt.Errorf("executionKind %q is not a registered execution kind", m.ExecutionKind))
	}
	if err := validateSchemaField("inputSchema", m.InputSchema); err != nil {
		errs = append(errs, err)
	}
	if err := validateSchemaField("outputSchema", m.OutputSchema); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, validateSideEffect(m.SideEffect)...)
	errs = append(errs, validatePollPolicy(m.Poll, m.ExecutionKind == ToolExecutionAsync)...)
	errs = append(errs, validateToolFactDeclarations(m.Produces, m.Requires)...)

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid tool metadata %q: %w", m.Name, errors.Join(errs...))
}

// validatePorts checks one port list. Port names identify Edge handles, so a duplicate
// name would make an Edge ambiguous.
func validatePorts(direction string, ports []PortMetadata) []error {
	var errs []error
	seen := make(map[string]struct{}, len(ports))
	for i, port := range ports {
		label := port.Name
		if label == "" {
			label = fmt.Sprintf("#%d", i)
			errs = append(errs, fmt.Errorf("%s port %s: name is empty", direction, label))
		} else if _, duplicate := seen[port.Name]; duplicate {
			errs = append(errs, fmt.Errorf("%s port %q: duplicate name", direction, port.Name))
		} else {
			seen[port.Name] = struct{}{}
		}
		if !port.DataType.IsValid() {
			errs = append(errs, fmt.Errorf("%s port %s: dataType %q is not a supported port type", direction, label, port.DataType))
		}
	}
	return errs
}

// validateUISchema checks presentation metadata only. A field path that does not exist in
// ConfigSchema is rejected by the Registry, which compiles that schema.
func validateUISchema(schema NodeUISchema) []error {
	var errs []error
	for i, field := range schema.Fields {
		label := field.Path
		if label == "" {
			label = fmt.Sprintf("#%d", i)
			errs = append(errs, fmt.Errorf("uiSchema field %s: path is empty", label))
		}
		if !field.Group.IsValid() {
			errs = append(errs, fmt.Errorf("uiSchema field %s: group %q is not a supported group", label, field.Group))
		}
		if !field.Widget.IsValid() {
			errs = append(errs, fmt.Errorf("uiSchema field %s: widget %q is not a supported widget", label, field.Widget))
		}
		if field.Capability != "" && field.Widget != UIWidgetModelSelector {
			errs = append(errs, fmt.Errorf("uiSchema field %s: capability is only allowed with widget %s", label, UIWidgetModelSelector))
		}
	}
	return errs
}

// validateSchemaField checks that a declared schema is present and syntactically valid
// JSON. Whether it is a usable JSON Schema is decided by the Registry's schema compiler.
func validateSchemaField(field string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("%s is empty", field)
	}
	if !json.Valid(raw) {
		return fmt.Errorf("%s is not valid JSON", field)
	}
	return nil
}

func validateSideEffect(policy SideEffectPolicy) []error {
	var errs []error
	if !policy.Kind.IsValid() {
		errs = append(errs, fmt.Errorf("sideEffect.kind %q is not a supported kind", policy.Kind))
	}
	if !policy.Idempotency.IsValid() {
		errs = append(errs, fmt.Errorf("sideEffect.idempotency %q is not a supported mode", policy.Idempotency))
	}
	return errs
}
