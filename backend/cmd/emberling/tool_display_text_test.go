package main

import (
	"net/http"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/tools/changerequest"
	"github.com/leungll/Emberling/backend/internal/tools/deploy"
	"github.com/leungll/Emberling/backend/internal/tools/deployrelease"
	"github.com/leungll/Emberling/backend/internal/tools/externaltool"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
	"github.com/leungll/Emberling/backend/internal/tools/generatevideo"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
	"github.com/leungll/Emberling/backend/internal/tools/readmaterial"
	"github.com/leungll/Emberling/backend/internal/tools/remotelookup"
	"github.com/leungll/Emberling/backend/internal/tools/reviewartifact"
	"github.com/leungll/Emberling/backend/internal/tools/reviewasset"
	"github.com/leungll/Emberling/backend/internal/tools/reviewpatch"
	"github.com/leungll/Emberling/backend/internal/tools/sandboxtest"
	"github.com/leungll/Emberling/backend/internal/tools/verifypatch"
	"github.com/leungll/Emberling/backend/internal/tools/workspace"
)

// builtinToolMetadata returns the registered Metadata of every built-in Tool. Several
// Tools are only registered when their service is configured, so this list reads each
// declaration directly with inert ports instead of going through the process wiring;
// nothing here is executed.
func builtinToolMetadata() []domain.ToolMetadata {
	client := &http.Client{}
	metadata := []domain.ToolMetadata{
		lookup.Registration().Metadata,
		readmaterial.Registration(readmaterial.Ports{}).Metadata,
		remotelookup.Registration("http://mock-provider.test", client).Metadata,
		generateimage.Registration(nil, nil, nil).Metadata,
		reviewasset.Registration(reviewasset.Ports{}).Metadata,
		generatevideo.Registration("http://mock-provider.test", client).Metadata,
		deploy.Registration("http://mock-production.test", client).Metadata,
		deployrelease.Registration("http://mock-production.test", client).Metadata,
		sandboxtest.Registration("http://sandbox-runner.test", client).Metadata,
		verifypatch.Registration("http://verify-runner.test", client, nil).Metadata,
		reviewartifact.Registration(reviewartifact.Ports{}).Metadata,
		changerequest.Registration(nil).Metadata,
		reviewpatch.Registration(reviewpatch.Ports{}).Metadata,
	}
	for _, spec := range externaltool.MediaSpecs(nil) {
		metadata = append(metadata, spec.Metadata)
	}
	for _, reg := range workspace.Registrations(nil) {
		metadata = append(metadata, reg.Metadata)
	}
	return metadata
}

// TestBuiltinTools_CarryDisplayTitle proves that Studio can name every built-in Tool
// without a per-name fallback table, and that the display title stays within the bound
// every registration is validated against at startup.
func TestBuiltinTools_CarryDisplayTitle(t *testing.T) {
	seen := map[string]bool{}
	for _, meta := range builtinToolMetadata() {
		if seen[meta.Name] {
			t.Errorf("tool %q is listed twice", meta.Name)
		}
		seen[meta.Name] = true
		if meta.Title == "" {
			t.Errorf("tool %q has no display title", meta.Name)
			continue
		}
		if err := domain.ValidateDisplayLine(meta.Title, domain.MaxDisplayTitleLength); err != nil {
			t.Errorf("tool %q display title: %v", meta.Name, err)
		}
	}
	wantNames := []string{
		lookup.ToolName, readmaterial.ToolName, remotelookup.ToolName, generateimage.ToolName,
		reviewasset.ToolName, generatevideo.ToolName, deploy.ToolName, deployrelease.ToolName,
		sandboxtest.ToolName, verifypatch.ToolName, reviewartifact.ToolName, changerequest.ToolName,
		reviewpatch.ToolName, externaltool.RunChecksToolName, externaltool.RenderTimelineToolName,
		workspace.ReadFileToolName, workspace.ListFilesToolName, workspace.WriteFileToolName,
	}
	for _, name := range wantNames {
		if !seen[name] {
			t.Errorf("built-in tool %q is missing from the display-title check", name)
		}
	}
	if len(seen) != len(wantNames) {
		t.Errorf("checked %d tools, want the %d built-in tools", len(seen), len(wantNames))
	}
}
