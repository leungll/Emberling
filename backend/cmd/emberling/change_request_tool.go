package main

import (
	"fmt"

	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/changerequest"
)

// registerChangeRequestTool registers the create_change_request Tool when a verification
// runner is configured. The Tool requires the fact verify_patch produces, so it is enabled
// together with that Tool: without a runner no patch can be verified and no change request
// can be created, and a Definition that names the Tool fails explicitly. The Tool keeps each
// request's rendered content in artifacts.
func registerChangeRequestTool(tools *registry.ToolRegistry, cfg config.VerifyRunner, artifacts changerequest.ArtifactTextStore) error {
	if !cfg.Enabled() {
		return nil
	}
	if err := tools.Register(changerequest.Registration(artifacts)); err != nil {
		return fmt.Errorf("register tool %s: %w", changerequest.ToolName, err)
	}
	return nil
}
