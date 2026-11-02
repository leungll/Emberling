package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// AgentNodeConfig is the fixed Definition-side shape of an `agent` Node's config.
// It is a pure parse/validate result: it carries no
// execution state and is not itself the immutable AgentRun record -- that record is only
// created when a Run is created, from these same
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
	// MaxGenerationCalls optionally bounds how many calls of Tools that count toward the
	// generation limit one Agent Run may start. Nil means unlimited.
	MaxGenerationCalls *int `json:"maxGenerationCalls,omitempty"`
	// ImageModelID optionally names the image model the node's image-model Tools run
	// with. Whether it resolves, declares the right capability and is required by the
	// allowlist is decided in service, which holds the Registries.
	ImageModelID string `json:"imageModelId,omitempty"`
	// ReviewModelID optionally names the vision model the node's review Tools run with;
	// without it they apply the deterministic mock review policy. Whether it resolves and
	// declares vision input is decided in service, which holds the Registries.
	ReviewModelID string `json:"reviewModelId,omitempty"`
	// Workspace optionally binds the Agent Run to one registered repository at one commit
	// and bounds the paths it may change. Whether repositoryId is registered is decided by
	// the Compiler, which is handed the registered set; the shape is checked here.
	Workspace *AgentWorkspaceConfig `json:"workspace,omitempty"`
	// GoalPointer optionally names, as a JSON Pointer into the node's input, where the
	// task goal (PREPARE_ONLY or FIX_AND_DEPLOY) is read from when the Agent Run starts.
	GoalPointer string `json:"goalPointer,omitempty"`
	// The optional bounds of the human and release waits, all positive. Each is absent
	// when nil; the Runtime never substitutes a process default for a missing one.
	ApprovalTimeoutMs       *int `json:"approvalTimeoutMs,omitempty"`
	ReleaseConfirmTimeoutMs *int `json:"releaseConfirmTimeoutMs,omitempty"`
	VoidGraceMs             *int `json:"voidGraceMs,omitempty"`
	MaxReleaseRetries       *int `json:"maxReleaseRetries,omitempty"`
	HumanWaitTimeoutMs      *int `json:"humanWaitTimeoutMs,omitempty"`
	// OutputForm is the form outputSchema gives the FINAL output. It is derived by
	// ParseAgentNodeConfig and is not part of the saved config.
	OutputForm AgentOutputForm `json:"-"`
}

// AgentWorkspaceConfig is the Definition-side description of an Agent's workspace. It is
// frozen into the Agent Run when the Run starts; nothing here is read again afterwards.
type AgentWorkspaceConfig struct {
	RepositoryID string `json:"repositoryId"`
	// BaseCommit is the full 40-digit lowercase hexadecimal commit the workspace is read
	// at and every patch applies on.
	BaseCommit string `json:"baseCommit"`
	// AllowedPaths are the relative path prefixes inside the repository the Agent may
	// change. They are validated as written and kept in the author's order; a path the
	// Agent writes outside them is refused.
	AllowedPaths []string `json:"allowedPaths"`
	// DeployTarget optionally names the service and environment a release Tool deploys
	// to.
	DeployTarget *AgentDeployTarget `json:"deployTarget,omitempty"`
}

// AgentDeployTarget names one deployment target.
type AgentDeployTarget struct {
	Service     string `json:"service"`
	Environment string `json:"environment"`
}

// agentPositiveIntegerFields are the optional config fields that must be positive
// integers when present, keyed by wire name.
var agentPositiveIntegerFields = []string{
	"maxGenerationCalls", "approvalTimeoutMs", "releaseConfirmTimeoutMs", "voidGraceMs", "maxReleaseRetries", "humanWaitTimeoutMs",
}

// baseCommitPattern is a full lowercase hexadecimal git object name.
var baseCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

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
// fixed `agent` config shape. Beyond the struct shape itself
// (unknown fields rejected), it checks the three invariants ConfigSchema alone cannot
// express:
//
//   - maxTurns and timeoutMs are both present and >= 1 (AgentRun invariants).
//   - maxGenerationCalls, when present, is an integer >= 1. It has no upper bound and is
//     not tied to maxTurns; absent means unlimited.
//   - contextSchema, stateSchema and outputSchema, when present, are each a compilable
//     JSON Schema document.
//   - stateSchema, when present, accepts `{}` -- an Agent Run's State Version 0
//     ("State Version 0 = {}").
//   - outputSchema, when present, has one of the two output forms: the text form accepts
//     the string "", and the object form declares the root type "object" and stays
//     inside the strict output subset. Absent, the Agent has the text form. Either way
//     the FINAL output reaches the Agent's single `text` port.
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
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && slices.Contains(agentPositiveIntegerFields, typeErr.Field) {
			return AgentNodeConfig{}, &AgentConfigError{Field: typeErr.Field, Message: typeErr.Field + " must be an integer"}
		}
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
	for _, field := range []struct {
		name  string
		value *int
	}{
		{"maxGenerationCalls", cfg.MaxGenerationCalls},
		{"approvalTimeoutMs", cfg.ApprovalTimeoutMs},
		{"releaseConfirmTimeoutMs", cfg.ReleaseConfirmTimeoutMs},
		{"voidGraceMs", cfg.VoidGraceMs},
		{"maxReleaseRetries", cfg.MaxReleaseRetries},
		{"humanWaitTimeoutMs", cfg.HumanWaitTimeoutMs},
	} {
		if field.value != nil && *field.value < 1 {
			return AgentNodeConfig{}, &AgentConfigError{Field: field.name, Message: field.name + " must be >= 1 when present"}
		}
	}
	if cfg.Workspace != nil {
		if err := validateAgentWorkspace(*cfg.Workspace); err != nil {
			return AgentNodeConfig{}, err
		}
	}
	if cfg.GoalPointer != "" {
		if err := domain.ValidateJSONPointer(cfg.GoalPointer); err != nil {
			return AgentNodeConfig{}, &AgentConfigError{Field: "goalPointer", Message: "goalPointer is not a valid JSON Pointer: " + err.Error()}
		}
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

	cfg.OutputForm = AgentOutputText
	if len(bytes.TrimSpace(cfg.OutputSchema)) > 0 {
		form, err := classifyAgentOutputSchema(cfg.OutputSchema)
		if err != nil {
			return AgentNodeConfig{}, err
		}
		cfg.OutputForm = form
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

// validateAgentWorkspace checks the shape of a workspace declaration. Whether the
// repository is registered is the Compiler's check, because only the assembly layer knows
// the registered set.
func validateAgentWorkspace(ws AgentWorkspaceConfig) error {
	if ws.RepositoryID == "" {
		return &AgentConfigError{Field: "workspace", Message: "workspace.repositoryId is required"}
	}
	if !baseCommitPattern.MatchString(ws.BaseCommit) {
		return &AgentConfigError{Field: "workspace", Message: "workspace.baseCommit must be 40 lowercase hexadecimal digits"}
	}
	if len(ws.AllowedPaths) == 0 {
		return &AgentConfigError{Field: "workspace", Message: "workspace.allowedPaths must name at least one path"}
	}
	seen := make(map[string]struct{}, len(ws.AllowedPaths))
	for i, allowed := range ws.AllowedPaths {
		normalised, err := normaliseAllowedPath(allowed)
		if err != nil {
			return &AgentConfigError{Field: "workspace", Message: fmt.Sprintf("workspace.allowedPaths[%d]: %s", i, err)}
		}
		if _, duplicate := seen[normalised]; duplicate {
			return &AgentConfigError{Field: "workspace", Message: fmt.Sprintf("workspace.allowedPaths[%d] repeats an earlier path", i)}
		}
		seen[normalised] = struct{}{}
	}
	if ws.DeployTarget != nil && (ws.DeployTarget.Service == "" || ws.DeployTarget.Environment == "") {
		return &AgentConfigError{Field: "workspace", Message: "workspace.deployTarget needs both service and environment"}
	}
	return nil
}

// normaliseAllowedPath checks one allowed path prefix and returns its canonical form: a
// relative, slash-separated path inside the repository, with no "." or ".." segment and no
// trailing slash. The repository root itself is not an allowed prefix.
func normaliseAllowedPath(allowed string) (string, error) {
	switch {
	case allowed == "":
		return "", errors.New("path is empty")
	case strings.HasPrefix(allowed, "/"):
		return "", errors.New("path must be relative, not start with \"/\"")
	case strings.ContainsRune(allowed, '\\'):
		return "", errors.New("path must use \"/\" as its separator")
	}
	for _, segment := range strings.Split(allowed, "/") {
		if segment == "." || segment == ".." {
			return "", errors.New("path must not contain \".\" or \"..\" segments")
		}
	}
	cleaned := path.Clean(allowed)
	if cleaned == "." || cleaned == "" {
		return "", errors.New("path must name a location inside the repository")
	}
	return cleaned, nil
}

// AgentGoal is the task goal an Agent Run is frozen to: prepare the change only, or fix
// and deploy it. The goal decides whether a release Tool is available to the Agent.
type AgentGoal string

const (
	AgentGoalPrepareOnly  AgentGoal = "PREPARE_ONLY"
	AgentGoalFixAndDeploy AgentGoal = "FIX_AND_DEPLOY"
)

func (g AgentGoal) IsValid() bool {
	return g == AgentGoalPrepareOnly || g == AgentGoalFixAndDeploy
}

// ResolveAgentGoal reads the Agent Run's goal from the node input at config.GoalPointer.
// It is a pure decision made once, in the transaction that starts the Agent Run; the
// result is frozen there and never re-read. A config without a goal pointer, a pointer
// that does not resolve in the input, or a value that is not one of the two goals is a
// configuration failure of the goalPointer field, reported as an AgentConfigError: the
// Runtime never substitutes a default goal.
func ResolveAgentGoal(config AgentNodeConfig, nodeInput json.RawMessage) (AgentGoal, error) {
	if config.GoalPointer == "" {
		return "", &AgentConfigError{Field: "goalPointer", Message: "goalPointer is not configured, so no goal can be read from the input"}
	}
	if err := domain.ValidateJSONPointer(config.GoalPointer); err != nil {
		return "", &AgentConfigError{Field: "goalPointer", Message: "goalPointer is not a valid JSON Pointer: " + err.Error()}
	}
	var doc any
	if err := json.Unmarshal(nodeInput, &doc); err != nil {
		return "", &AgentConfigError{Field: "goalPointer", Message: "the node input is not valid JSON, so goalPointer cannot be resolved"}
	}
	value, found := lookupJSONPointer(doc, domain.JSONPointerTokens(config.GoalPointer))
	if !found {
		return "", &AgentConfigError{Field: "goalPointer", Message: fmt.Sprintf("goalPointer %q does not resolve in the node input", config.GoalPointer)}
	}
	text, isString := value.(string)
	goal := AgentGoal(text)
	if !isString || !goal.IsValid() {
		return "", &AgentConfigError{Field: "goalPointer", Message: fmt.Sprintf("the value at goalPointer %q is not one of %s or %s", config.GoalPointer, AgentGoalPrepareOnly, AgentGoalFixAndDeploy)}
	}
	return goal, nil
}
