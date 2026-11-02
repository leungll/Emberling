package config

import (
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
