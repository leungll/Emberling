package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/leungll/Emberling/backend/internal/adapters/mocktask"
	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// taskGatewayProviderID is the stable Provider ID of the task gateway: what its models
// declare, what its task instance stamps into every external task identity and so what
// a Callback Binding of a gateway task records. It is not a credential.
const taskGatewayProviderID = "task-gateway-v1"

// registerTaskGateway registers the gateway's models and returns the task instance that
// dispatches to the gateway, or nil when the gateway is not configured, in which case
// nothing is registered and the image_generation node has the Mock Provider's instance
// only. config.Load has already refused a partial group and a colliding or malformed
// model list; the Registry refuses an ID already taken.
//
// The instance speaks the Mock Provider's protocol without its scenarios: the gateway is
// a real Provider, so it receives the prompt verbatim and no fabricated result.
func registerTaskGateway(ctx context.Context, models *registry.ModelRegistry, cfg config.TaskGateway, client *http.Client) (*mocktask.Adapter, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	declared := make([]mocktask.TaskModel, 0, len(cfg.Models))
	for _, entry := range cfg.Models {
		declared = append(declared, mocktask.TaskModel{ID: entry.ID, DisplayName: entry.DisplayName, Description: entry.Description})
	}
	if err := models.Register(ctx, mocktask.NewTaskModels(taskGatewayProviderID, declared)); err != nil {
		return nil, fmt.Errorf("register task gateway models: %w", err)
	}
	return mocktask.New(cfg.BaseURL, client, mocktask.WithProviderID(taskGatewayProviderID), mocktask.WithoutMockScenarios()), nil
}
