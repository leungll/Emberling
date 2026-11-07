package main

import (
	"net/http"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/externaltask"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/externaltool"
)

func TestRegisterMediaTools_NotConfigured_RegistersNothing(t *testing.T) {
	tools := registry.NewToolRegistry()
	if err := registerMediaTools(tools, config.MediaService{}, &http.Client{}, asset.NewArtifactStore(t.TempDir(), 1<<20)); err != nil {
		t.Fatalf("registerMediaTools() error = %v", err)
	}
	if n := len(tools.ListMetadata()); n != 0 {
		t.Fatalf("%d tools registered without a configured media service, want none", n)
	}
}

func TestRegisterMediaTools_Configured_RegistersEveryDeclaredToolCleanly(t *testing.T) {
	tools := registry.NewToolRegistry()
	artifacts := asset.NewArtifactStore(t.TempDir(), 1<<20)
	if err := registerMediaTools(tools, config.MediaService{BaseURL: "http://media:9200"}, &http.Client{}, artifacts); err != nil {
		t.Fatalf("registerMediaTools() error = %v", err)
	}
	registrations, err := mediaToolRegistrations(externaltask.New("http://media:9200", &http.Client{}), artifacts)
	if err != nil {
		t.Fatalf("mediaToolRegistrations() error = %v", err)
	}
	if got, want := len(tools.ListMetadata()), len(registrations); got != want {
		t.Fatalf("registered %d tools, want the %d declared media tools", got, want)
	}
	for _, name := range []string{externaltool.RunChecksToolName, externaltool.RenderTimelineToolName} {
		if _, ok := tools.Get(name); !ok {
			t.Errorf("tool %s is not registered", name)
		}
	}
	if err := registry.ValidateFactDeclarations(tools.ListMetadata(), nil); err != nil {
		t.Errorf("ValidateFactDeclarations() error = %v", err)
	}
}
