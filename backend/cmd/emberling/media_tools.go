package main

import (
	"fmt"
	"net/http"

	"github.com/leungll/Emberling/backend/internal/adapters/externaltask"
	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/externaltool"
)

// mediaProviderID is the stable Provider ID the media service's task identities carry in
// every Callback Binding. It is not a credential.
const mediaProviderID = "media-service"

// registerMediaTools registers the Tools that dispatch to the media task service when one
// is configured. Without one nothing is registered, so a Definition that names such a Tool
// fails explicitly instead of dispatching to a service that does not exist. Every Tool is
// built from its externaltool declaration over one shared task client.
func registerMediaTools(tools *registry.ToolRegistry, cfg config.MediaService, client *http.Client) error {
	if !cfg.Enabled() {
		return nil
	}
	registrations, err := mediaToolRegistrations(externaltask.New(cfg.BaseURL, client))
	if err != nil {
		return err
	}
	for _, reg := range registrations {
		if err := tools.Register(reg); err != nil {
			return fmt.Errorf("register tool %s: %w", reg.Metadata.Name, err)
		}
	}
	return nil
}

// mediaToolSpecs lists the declarations of the Tools that dispatch to the media service.
// The declarations themselves live in the externaltool package; the list is empty until
// the first video Tool is declared.
func mediaToolSpecs() []externaltool.Spec {
	return nil
}

// mediaToolRegistrations builds one ToolRegistration per media Tool declaration over the
// given task client, under the media service's Provider ID.
func mediaToolRegistrations(tasks externaltool.TaskClient) ([]registry.ToolRegistration, error) {
	specs := mediaToolSpecs()
	registrations := make([]registry.ToolRegistration, 0, len(specs))
	for _, spec := range specs {
		spec.ProviderID = mediaProviderID
		reg, err := externaltool.Registration(spec, tasks)
		if err != nil {
			return nil, fmt.Errorf("register tool %s: %w", spec.Metadata.Name, err)
		}
		registrations = append(registrations, reg)
	}
	return registrations, nil
}
