package main

import (
	"fmt"
	"net/http"

	"github.com/leungll/Emberling/backend/internal/adapters/externaltask"
	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/nodes/mediatranscript"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// registerMediaNodes registers the Node Types that dispatch to the media task service
// when one is configured. Without one nothing is registered, so a Definition that names
// such a node fails explicitly instead of dispatching to a service that does not exist.
// The node speaks the same task protocol as the media Tools, through the same Adapter
// over the same dispatch HTTP client, under the media service's Provider ID.
func registerMediaNodes(nodes *registry.NodeRegistry, cfg config.MediaService, client *http.Client, artifacts mediatranscript.ArtifactWriter) error {
	if !cfg.Enabled() {
		return nil
	}
	reg, err := mediatranscript.Registration(mediatranscript.Ports{
		Tasks:      externaltask.New(cfg.BaseURL, client),
		Artifacts:  artifacts,
		ProviderID: mediaProviderID,
	})
	if err != nil {
		return fmt.Errorf("register node type %s: %w", mediatranscript.NodeType, err)
	}
	if err := nodes.Register(reg); err != nil {
		return fmt.Errorf("register node type %s: %w", reg.Metadata.Type, err)
	}
	return nil
}
