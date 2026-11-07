package main

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/externaltool"
	"github.com/leungll/Emberling/backend/internal/tools/reviewartifact"
)

// unservedArtifacts is a runArtifactOpener that is never asked: registration must not
// read anything.
type unservedArtifacts struct{}

func (unservedArtifacts) OpenRunContent(context.Context, string, string) (domain.Artifact, io.ReadCloser, error) {
	return domain.Artifact{}, nil, domain.ErrNotFound
}

func TestRegisterReviewArtifactTool_Configured_RegistersBesideTheMediaTools(t *testing.T) {
	tools := registry.NewToolRegistry()
	cfg := config.MediaService{BaseURL: "http://media:9200"}
	if err := registerMediaTools(tools, cfg, &http.Client{}, asset.NewArtifactStore(t.TempDir(), 1<<20)); err != nil {
		t.Fatalf("registerMediaTools() error = %v", err)
	}
	if err := registerReviewArtifactTool(tools, cfg, registry.NewModelRegistry(), unservedArtifacts{}); err != nil {
		t.Fatalf("registerReviewArtifactTool() error = %v", err)
	}
	reg, ok := tools.Get(reviewartifact.ToolName)
	if !ok {
		t.Fatalf("tool %q not registered", reviewartifact.ToolName)
	}
	m := reg.Metadata
	if m.ExecutionKind != domain.ToolExecutionSync || !m.UsesReviewModel || !m.RequiresReviewModel ||
		m.ReviewModelCapability != domain.ModelCapabilityStructuredDecision || m.Produces == nil || m.Produces.FactType != externaltool.FactTypeContentReviewed {
		t.Errorf("metadata = %+v, want a synchronous review producing %s with a required independent review model", m, externaltool.FactTypeContentReviewed)
	}
	if err := registry.ValidateFactDeclarations(tools.ListMetadata(), nil); err != nil {
		t.Errorf("ValidateFactDeclarations() error = %v", err)
	}
}

func TestRegisterReviewArtifactTool_NotConfigured_RegistersNothing(t *testing.T) {
	tools := registry.NewToolRegistry()
	if err := registerReviewArtifactTool(tools, config.MediaService{}, registry.NewModelRegistry(), unservedArtifacts{}); err != nil {
		t.Fatalf("registerReviewArtifactTool() error = %v", err)
	}
	if _, ok := tools.Get(reviewartifact.ToolName); ok {
		t.Fatalf("tool %q registered without a configured media service", reviewartifact.ToolName)
	}
}
