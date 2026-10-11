package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/leungll/Emberling/backend/internal/fixture/sandboxrunner"
)

// imageDockerfile is the sandbox image definition, relative to this package.
var imageDockerfile = filepath.Join("..", "..", "..", "deploy", "Dockerfile.sandboxsupervisor")

func TestSupervisorImage_EveryStage_UsesDockerBackendPinnedImage(t *testing.T) {
	content, err := os.ReadFile(imageDockerfile)
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	stages := regexp.MustCompile(`(?m)^FROM\s+(?:--platform=(\S+)\s+)?(\S+)`).FindAllStringSubmatch(string(content), -1)
	if len(stages) == 0 {
		t.Fatal("Dockerfile has no FROM line")
	}
	for _, stage := range stages {
		if stage[1] != "linux/amd64" || stage[2] != sandboxrunner.DefaultImage {
			t.Errorf("stage FROM --platform=%s %s, want --platform=linux/amd64 %s", stage[1], stage[2], sandboxrunner.DefaultImage)
		}
	}
}
