package main

import (
	"fmt"

	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/reviewartifact"
)

// registerReviewArtifactTool registers the review_artifact Tool under the same condition
// as the media Tools: the review only has a place in the video workflow whose timelines
// those Tools produce, so a process without a media service registers nothing and a
// Definition naming the review fails explicitly. The Tool resolves the Agent Run's frozen
// review model through the Model Registry and reads the timeline and the transcript
// through the same Run-scoped artifact read every material-reading Tool uses.
func registerReviewArtifactTool(tools *registry.ToolRegistry, cfg config.MediaService, models *registry.ModelRegistry, artifacts runArtifactOpener) error {
	if !cfg.Enabled() {
		return nil
	}
	ports := reviewartifact.Ports{Models: models, Materials: materialReader{artifacts: artifacts}}
	if err := tools.Register(reviewartifact.Registration(ports)); err != nil {
		return fmt.Errorf("register tool %s: %w", reviewartifact.ToolName, err)
	}
	return nil
}
