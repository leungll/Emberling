package main

import (
	"net/http"
	"testing"

	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/nodes/mediatranscript"
	"github.com/leungll/Emberling/backend/internal/registry"
)

func TestRegisterMediaNodes_NotConfigured_RegistersNothing(t *testing.T) {
	nodes := registry.NewNodeRegistry()
	if err := registerMediaNodes(nodes, config.MediaService{}, &http.Client{}, asset.NewArtifactStore(t.TempDir(), 1<<20)); err != nil {
		t.Fatalf("registerMediaNodes() error = %v", err)
	}
	if n := len(nodes.ListMetadata()); n != 0 {
		t.Fatalf("%d nodes registered without a configured media service, want none", n)
	}
}

func TestRegisterMediaNodes_Configured_RegistersTheTranscriptNodeUnderTheMediaProvider(t *testing.T) {
	nodes := registry.NewNodeRegistry()
	if err := registerMediaNodes(nodes, config.MediaService{BaseURL: "http://media:9200"}, &http.Client{}, asset.NewArtifactStore(t.TempDir(), 1<<20)); err != nil {
		t.Fatalf("registerMediaNodes() error = %v", err)
	}
	md, ok := nodes.NodeMetadata(mediatranscript.NodeType)
	if !ok {
		t.Fatalf("node type %s is not registered", mediatranscript.NodeType)
	}
	if md.Poll == nil || md.DispatchModelPath != "" {
		t.Fatalf("metadata = %+v, want a poll policy and no dispatch model", md)
	}
}
