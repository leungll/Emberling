package config

import (
	"errors"
	"fmt"
	"strings"
)

// The task gateway group: the address of the task gateway, which serves real image
// models as asynchronous tasks, and the models served through it. The Backend and the
// gateway process read the same model list value.
const (
	KeyTaskGatewayBaseURL = EnvPrefix + "TASK_GATEWAY_BASE_URL"
	KeyTaskGatewayModels  = EnvPrefix + "TASK_GATEWAY_MODELS"
)

// TaskGatewayModelIDPrefix starts every gateway Model ID, so a Definition's model id
// alone shows that its image tasks go through the gateway.
const TaskGatewayModelIDPrefix = "gateway-"

// ParseTaskGatewayModels parses the gateway model list. It has the shape of every other
// model list, a JSON array of {"id","model","displayName","description"} entries, where
// model is the upstream model name, and each id must also start with
// TaskGatewayModelIDPrefix. Errors name entry indexes and fields, never a value.
func ParseTaskGatewayModels(value string) ([]OpenAIModelEntry, error) {
	entries, reason := parseModelList(value)
	if reason != "" {
		return nil, &InvalidConfigError{Key: KeyTaskGatewayModels, Reason: reason}
	}
	if problems := gatewayIDProblems(entries); len(problems) > 0 {
		return nil, &InvalidConfigError{Key: KeyTaskGatewayModels, Reason: strings.Join(problems, "; ")}
	}
	return entries, nil
}

func gatewayIDProblems(entries []OpenAIModelEntry) []string {
	var problems []string
	for i, entry := range entries {
		if !strings.HasPrefix(entry.ID, TaskGatewayModelIDPrefix) {
			problems = append(problems, fmt.Sprintf("entry %d: id must start with %q", i, TaskGatewayModelIDPrefix))
		}
	}
	return problems
}

// TaskGateway addresses the optional task gateway. Like the other model groups, either
// both keys are set and every listed model is registered as an image_generation model of
// the gateway, or neither is and the gateway path does not exist; a partial group is a
// startup error rather than a gateway model that can never run.
type TaskGateway struct {
	BaseURL string
	Models  []OpenAIModelEntry
}

// Enabled reports whether the task gateway is configured.
func (g TaskGateway) Enabled() bool {
	return g.BaseURL != "" || len(g.Models) > 0
}

// taskGateway reads the optional task gateway group.
func (l *loader) taskGateway() TaskGateway {
	if strings.TrimSpace(l.env(KeyTaskGatewayBaseURL)) == "" && strings.TrimSpace(l.env(KeyTaskGatewayModels)) == "" {
		return TaskGateway{}
	}
	gateway := TaskGateway{BaseURL: l.requiredProviderURL(KeyTaskGatewayBaseURL)}
	if value := l.required(KeyTaskGatewayModels); value != "" {
		models, err := ParseTaskGatewayModels(value)
		var invalid *InvalidConfigError
		if errors.As(err, &invalid) {
			l.fail(KeyTaskGatewayModels, invalid.Reason)
		}
		gateway.Models = models
	}
	return gateway
}

// missingKeys lists the required keys of an enabled group that are empty.
func (g TaskGateway) missingKeys() []string {
	if !g.Enabled() {
		return nil
	}
	var missing []string
	if g.BaseURL == "" {
		missing = append(missing, KeyTaskGatewayBaseURL)
	}
	if len(g.Models) == 0 {
		missing = append(missing, KeyTaskGatewayModels)
	}
	return missing
}

// validate checks the model list of an enabled group and that no gateway Model ID
// repeats a decision or image Model ID: every model is resolved through one Model
// Registry, so an ID may name only one of them.
func (g TaskGateway) validate(decision OpenAIModel, image ImageModel) error {
	if !g.Enabled() {
		return nil
	}
	problems := append(validateModelList(g.Models), gatewayIDProblems(g.Models)...)
	taken := make(map[string]string, len(decision.Models)+len(image.Models))
	for i, model := range decision.Models {
		taken[model.ID] = fmt.Sprintf("%s entry %d", KeyModels, i)
	}
	for i, model := range image.Models {
		taken[model.ID] = fmt.Sprintf("%s entry %d", KeyImageModels, i)
	}
	for i, model := range g.Models {
		if owner, ok := taken[model.ID]; ok && model.ID != "" {
			problems = append(problems, fmt.Sprintf("entry %d: id duplicates %s", i, owner))
		}
	}
	if len(problems) > 0 {
		return &InvalidConfigError{Key: KeyTaskGatewayModels, Reason: strings.Join(problems, "; ")}
	}
	return nil
}
