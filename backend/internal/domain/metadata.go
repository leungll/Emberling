package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
// sourceHandle and targetHandle. Title and Description are optional display text Studio
// shows for the handle, bounded by ValidateDisplayLine; empty means none and is omitted on
// the wire. The Compiler and the Runtime never read them.
type PortMetadata struct {
	Name        string   `json:"name"`
	DataType    PortType `json:"dataType"`
	Required    bool     `json:"required"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
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
	// empty for every other Widget. A Model Selector without a Capability lists every
	// registered model, and the Backend checks the capability the selection needs on save.
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

// NodeMetadata is the registered description of one Node Type. Description is an optional
// plain sentence saying what the step does, shown by Studio and bounded by
// ValidateDisplayLine; empty means none and is omitted on the wire.
type NodeMetadata struct {
	Type          string            `json:"type"`
	DisplayName   string            `json:"displayName"`
	Description   string            `json:"description,omitempty"`
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
	// DispatchModelPath names the ConfigSchema property that holds the Model ID an ASYNC
	// Node dispatches its external task to. The Runtime reads it to find the Provider a
	// NodeRun will occupy a capacity slot of, before the node's own code runs; a Node that
	// declares none is never governed by Provider capacity. It is a registration fact
	// for the Runtime, not part of the catalogue Studio reads, so it stays off the wire.
	DispatchModelPath string `json:"-"`
	// MediaLinks says which output port values are media references and where the URL
	// that serves each and its media type are read from, so the Run can list the media
	// its steps produced and Studio can match a reference to its URL instead of building
	// one from a value. Empty means the Node Type produces no addressable media. Omitted
	// on the wire when empty.
	MediaLinks []MediaLink `json:"mediaLinks,omitempty"`
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
	if err := ValidateDisplayLine(m.Description, MaxDisplayDescriptionLength); err != nil {
		errs = append(errs, fmt.Errorf("description: %w", err))
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
	errs = append(errs, validateNodeMediaLinks(m.MediaLinks, m.Outputs)...)

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid node metadata %q: %w", m.Type, errors.Join(errs...))
}

// Declared model result capabilities. Studio filters the Model Selector by them and an
// Agent model must declare ModelCapabilityStructuredDecision, meaning it can return fixed
// JSON for the DecisionSchema. ModelCapabilityReferenceImageGeneration means the model
// synchronously generates one image from one input image and a prompt; an Agent node's
// imageModelId must declare it. ModelCapabilityVisionInput means the model accepts inline
// image parts in a user message and answers with the strict structured output every
// Agent model uses; an Agent node's reviewModelId must declare it. A capability names the result Emberling needs, not a
// Provider-native API. The list stays open: an Adapter may declare a capability this
// package does not name.
const (
	ModelCapabilityTextGeneration           = "text_generation"
	ModelCapabilityImageGeneration          = "image_generation"
	ModelCapabilityStructuredDecision       = "structured_decision"
	ModelCapabilityReferenceImageGeneration = "reference_image_generation"
	ModelCapabilityVisionInput              = "vision_input"
)

// ModelMetadata is the registered description of one stable Model ID. ConfigSchema is the
// only contract for model parameters; defaults are resolved once before a Definition
// version is frozen and are never re-injected at Run or recovery time. Description is an
// optional one-line note Studio shows with the model, bounded by ValidateModelDescription;
// empty means none and is omitted on the wire.
//
// ProviderID is the stable identifier of the asynchronous Provider that runs this model's
// tasks: the same value an Adapter stamps into the external task identity and Callback
// Binding. It is required for a model declaring ModelCapabilityImageGeneration, whose
// generation is always dispatched as an asynchronous task, and is empty, and omitted on
// the wire, for a model served only by synchronous calls. It is not a credential.
type ModelMetadata struct {
	ID           string          `json:"id"`
	DisplayName  string          `json:"displayName"`
	Description  string          `json:"description,omitempty"`
	Capabilities []string        `json:"capabilities"`
	ConfigSchema json.RawMessage `json:"configSchema"`
	ProviderID   string          `json:"providerId,omitempty"`
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
//
// Description is model-facing: it is what the model reads about the Tool in every request.
// Title is an optional short display name for Studio only, bounded by ValidateDisplayLine
// and omitted on the wire when empty; it never reaches a model request.
type ToolMetadata struct {
	Name          string            `json:"name"`
	Title         string            `json:"title,omitempty"`
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
	// UsesImageModel marks a Tool that runs with the image model selected on its Agent
	// node. Saving a Definition requires that node to select one, and each call receives
	// the Agent Run's frozen image Model ID; the Runtime never decides this by Tool Name.
	UsesImageModel bool `json:"usesImageModel"`
	// UsesReviewModel marks a Tool that reviews with the review model optionally selected
	// on its Agent node. Each call receives the Agent Run's frozen review Model ID, or none
	// when the node selected none; the Runtime never decides this by Tool Name.
	UsesReviewModel bool `json:"usesReviewModel"`
	// ReviewModelCapability names the model capability the selected review model must
	// declare for this Tool: a vision review needs inline image input, a text review needs
	// the strict structured output every Agent model gives. It is required with
	// UsesReviewModel and forbidden without it; the Runtime checks the selection against it
	// when a Definition is saved, when the Agent Run freezes the model and when a call is
	// claimed. Omitted on the wire when undeclared.
	ReviewModelCapability string `json:"reviewModelCapability,omitempty"`
	// RequiresReviewModel marks a review Tool that gives an independent second opinion: its
	// Agent node must select a review model, and that model must not be the node's own
	// decision model. A Tool that leaves it false falls back to its mock policy when the
	// node selects no review model. Only a Tool that declares UsesReviewModel may set it.
	RequiresReviewModel bool `json:"requiresReviewModel"`
	// CostSource is the stable identifier the charges of this Tool's calls are attributed
	// to when the call is bound to no Model ID, such as a task sent to a Provider that
	// names no model. A Tool that calls a registered model attributes its charges to that
	// Model ID instead and leaves it empty. It is a Runtime attribution key, not a
	// Provider identifier or a credential, and it is not part of the catalog projection.
	CostSource string `json:"-"`
	// Authorization declares the risk and reversibility the per-Action authorization
	// decision reads. A Tool that declares none is a LOW-risk Tool: every Tool registered
	// before this declaration existed keeps its behaviour, and the Runtime never infers
	// risk from a Tool Name. Omitted on the wire when undeclared.
	Authorization *ToolAuthorization `json:"authorization,omitempty"`
	// Records lists the record kinds a successful result of this Tool may carry, which the
	// result transaction persists. A result carrying a record of an undeclared kind is an
	// invalid Tool result; the Runtime decides this by the declaration, never by Tool Name.
	Records []ToolRecordKind `json:"records"`
	// UsesWorkspace marks a Tool whose calls receive the Agent Run's frozen workspace view.
	UsesWorkspace bool `json:"usesWorkspace"`
	// WorkspaceWritePath is a JSON Pointer into the call's arguments naming the repository
	// path a call of this Tool writes, for a Tool that records a WORKSPACE_PATCH. The
	// Runtime checks that path against the Agent Run's frozen allowed paths before any
	// Attempt exists and returns a SCOPE_DENIED refusal to the Agent when it falls outside
	// them. Empty means the Tool declares no single written path; the recorded patch's
	// touched paths are still checked in the result transaction. Omitted on the wire when
	// undeclared.
	WorkspaceWritePath string `json:"workspaceWritePath,omitempty"`
	// WorkspacePatchDigestPath is a JSON Pointer into the call's arguments naming the
	// workspace patch digest the call is about, for a Tool that acts on the Agent Run's
	// current patch, such as a verification. The Runtime compares it with the digest of
	// the newest recorded patch before any Attempt exists and returns a STALE_PATCH
	// refusal to the Agent when they differ, so a result can never be bound to a patch
	// the workspace has moved past. Empty means the Tool declares no such argument.
	// Omitted on the wire when undeclared.
	WorkspacePatchDigestPath string `json:"workspacePatchDigestPath,omitempty"`
	// Reconcilable marks an ASYNC Tool whose uncertain or lost external operation the
	// Runtime may query for an authoritative result after the fact. It requires a Poll
	// declaration: reconciliation is a status query, never a resend.
	Reconcilable bool `json:"reconcilable"`
	// OccupiesReleaseTarget marks a reconcilable Tool whose approved call holds the Agent
	// Run's frozen deploy target until the release settles. The approval transaction
	// acquires the occupation for a Tool that declares it; the Runtime never decides this by
	// Tool Name. Omitted on the wire when false.
	OccupiesReleaseTarget bool `json:"occupiesReleaseTarget,omitempty"`
	// ReturnUnmetRequirement makes an unmet fact requirement a definite refusal that is
	// returned to the Agent, so it can establish the missing fact and call again, instead
	// of ending the Agent Run. A Tool that leaves it false keeps the terminal behaviour.
	ReturnUnmetRequirement bool `json:"returnUnmetRequirement"`
	// CountedFailedFacts lists the fact types whose failed verdicts bound this Tool's
	// calls: once an Agent Run has collected more failed facts of one of these types than
	// its configuration allows, further calls of the Tool are refused before any Attempt
	// exists. A type the Agent Run's configuration does not bound constrains nothing. Empty
	// means the Tool's calls are never bounded by failed facts.
	CountedFailedFacts []string `json:"countedFailedFacts"`
	// MaterialPointer is a JSON Pointer into a successful result naming the material
	// object a Tool that reads text by range returns: the artifact id, its content digest
	// and the line range, with the text itself under "text" beside that object. The
	// Context Manifest records such a result as a MATERIAL_RANGE item by this declaration,
	// never by Tool Name. Empty means the Tool returns no material. Omitted on the wire
	// when undeclared.
	MaterialPointer string `json:"materialPointer,omitempty"`
	// MediaLinks says where a successful result carries each media reference and the URL
	// that serves it, so the Agent Trace can list the media a call produced and Studio can
	// match a reference to its URL instead of building one from a value. Empty means the
	// Tool produces no addressable media. Omitted on the wire when empty.
	MediaLinks []MediaLink `json:"mediaLinks,omitempty"`
	// Display lists the members of a call's arguments and result that Studio shows on a run
	// card, each under a title. Only these members are projected into the Agent Trace; the
	// whole arguments and result never are. Empty means the call shows no member. Omitted
	// on the wire when empty.
	Display []ToolDisplayField `json:"display,omitempty"`
}

// ToolAuthorizationRisk is the declared risk class of a Tool's calls.
type ToolAuthorizationRisk string

const (
	// ToolRiskLow calls are allowed without a human decision. It is also what an
	// undeclared Authorization means.
	ToolRiskLow ToolAuthorizationRisk = "LOW"
	// ToolRiskProduction calls change a production system and need a human decision.
	ToolRiskProduction ToolAuthorizationRisk = "PRODUCTION"
)

func (r ToolAuthorizationRisk) IsValid() bool {
	return r == ToolRiskLow || r == ToolRiskProduction
}

// ToolAuthorization is the declared authorization profile of one Tool.
type ToolAuthorization struct {
	Risk ToolAuthorizationRisk `json:"risk"`
	// Reversible reports whether the effect of a call can be undone by a later call.
	Reversible bool `json:"reversible"`
}

// EffectiveRisk is the risk the authorization decision reads: the declared one, or LOW
// when the Tool declares no Authorization.
func (m ToolMetadata) EffectiveRisk() ToolAuthorizationRisk {
	if m.Authorization == nil {
		return ToolRiskLow
	}
	return m.Authorization.Risk
}

// ToolRecordKind names a record a Tool result may carry for the result transaction to
// persist.
type ToolRecordKind string

const (
	// ToolRecordWorkspacePatch is a new cumulative workspace patch.
	ToolRecordWorkspacePatch ToolRecordKind = "WORKSPACE_PATCH"
	// ToolRecordChangeRequest is a change request assembled from verified facts.
	ToolRecordChangeRequest ToolRecordKind = "CHANGE_REQUEST"
)

func (k ToolRecordKind) IsValid() bool {
	return k == ToolRecordWorkspacePatch || k == ToolRecordChangeRequest
}

// DeclaresRecord reports whether the Tool's registration lists the record kind.
func (m ToolMetadata) DeclaresRecord(kind ToolRecordKind) bool {
	for _, declared := range m.Records {
		if declared == kind {
			return true
		}
	}
	return false
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
	if err := ValidateDisplayLine(m.Title, MaxDisplayTitleLength); err != nil {
		errs = append(errs, fmt.Errorf("title: %w", err))
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
	errs = append(errs, validateToolAuthorization(m.Authorization)...)
	errs = append(errs, validateToolRecords(m.Records)...)
	if m.Reconcilable && (m.ExecutionKind != ToolExecutionAsync || m.Poll == nil) {
		errs = append(errs, errors.New("reconcilable is only allowed on an ASYNC tool that declares poll"))
	}
	if m.OccupiesReleaseTarget && (!m.Reconcilable || !m.UsesWorkspace) {
		errs = append(errs, errors.New("occupiesReleaseTarget is only allowed on a reconcilable tool that uses the workspace"))
	}
	if m.WorkspaceWritePath != "" {
		if err := ValidateJSONPointer(m.WorkspaceWritePath); err != nil || !strings.HasPrefix(m.WorkspaceWritePath, "/") {
			errs = append(errs, errors.New("workspaceWritePath must be a JSON Pointer starting with \"/\""))
		}
		if !m.UsesWorkspace || !m.DeclaresRecord(ToolRecordWorkspacePatch) {
			errs = append(errs, errors.New("workspaceWritePath is only allowed on a tool that uses the workspace and records WORKSPACE_PATCH"))
		}
	}
	if m.WorkspacePatchDigestPath != "" {
		if err := ValidateJSONPointer(m.WorkspacePatchDigestPath); err != nil || !strings.HasPrefix(m.WorkspacePatchDigestPath, "/") {
			errs = append(errs, errors.New("workspacePatchDigestPath must be a JSON Pointer starting with \"/\""))
		}
		if !m.UsesWorkspace {
			errs = append(errs, errors.New("workspacePatchDigestPath is only allowed on a tool that uses the workspace"))
		}
	}
	switch {
	case m.UsesReviewModel && m.ReviewModelCapability == "":
		errs = append(errs, errors.New("usesReviewModel requires reviewModelCapability to name the capability the review model must declare"))
	case !m.UsesReviewModel && m.ReviewModelCapability != "":
		errs = append(errs, errors.New("reviewModelCapability is only allowed on a tool that uses the review model"))
	}
	if m.RequiresReviewModel && !m.UsesReviewModel {
		errs = append(errs, errors.New("requiresReviewModel is only allowed on a tool that uses the review model"))
	}
	errs = append(errs, validateCountedFailedFacts(m.CountedFailedFacts)...)
	if m.MaterialPointer != "" {
		if err := ValidateJSONPointer(m.MaterialPointer); err != nil {
			errs = append(errs, errors.New("materialPointer must be a JSON Pointer starting with \"/\""))
		}
	}
	errs = append(errs, validateToolMediaLinks(m.MediaLinks)...)
	errs = append(errs, validateToolDisplay(m.Display)...)

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid tool metadata %q: %w", m.Name, errors.Join(errs...))
}

// validateCountedFailedFacts checks the fact types a Tool's calls are bounded by: each
// is named once and none is empty.
func validateCountedFailedFacts(factTypes []string) []error {
	var errs []error
	seen := make(map[string]struct{}, len(factTypes))
	for _, factType := range factTypes {
		if factType == "" {
			errs = append(errs, errors.New("countedFailedFacts: fact type is empty"))
			continue
		}
		if _, duplicate := seen[factType]; duplicate {
			errs = append(errs, fmt.Errorf("countedFailedFacts: duplicate fact type %q", factType))
			continue
		}
		seen[factType] = struct{}{}
	}
	return errs
}

// validateToolAuthorization checks an optional authorization declaration. Absence is the
// LOW profile and needs no check.
func validateToolAuthorization(authorization *ToolAuthorization) []error {
	if authorization == nil {
		return nil
	}
	if !authorization.Risk.IsValid() {
		return []error{fmt.Errorf("authorization.risk %q is not a supported risk", authorization.Risk)}
	}
	return nil
}

// validateToolRecords checks that every declared record kind is known and listed once.
func validateToolRecords(records []ToolRecordKind) []error {
	var errs []error
	seen := make(map[ToolRecordKind]struct{}, len(records))
	for _, kind := range records {
		if !kind.IsValid() {
			errs = append(errs, fmt.Errorf("records: %q is not a supported record kind", kind))
			continue
		}
		if _, duplicate := seen[kind]; duplicate {
			errs = append(errs, fmt.Errorf("records: %q is declared more than once", kind))
		}
		seen[kind] = struct{}{}
	}
	return errs
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
		if err := ValidateDisplayLine(port.Title, MaxDisplayTitleLength); err != nil {
			errs = append(errs, fmt.Errorf("%s port %q: title %w", direction, label, err))
		}
		if err := ValidateDisplayLine(port.Description, MaxDisplayDescriptionLength); err != nil {
			errs = append(errs, fmt.Errorf("%s port %q: description %w", direction, label, err))
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
