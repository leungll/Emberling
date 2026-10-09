package main

import (
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/sandboxrunner"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestLoadConfig_Unset_UsesMockBackendDefaults(t *testing.T) {
	cfg, err := loadConfig(env(nil))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	want := config{addr: ":9103", backend: "mock", image: sandboxrunner.DefaultImage, testTimeout: 120 * time.Second}
	if cfg != want {
		t.Fatalf("config = %+v, want %+v", cfg, want)
	}
}

func TestLoadConfig_DockerWithControls_ReadsEveryKey(t *testing.T) {
	image := "registry.example/go@sha256:" + strings.Repeat("a", 64)
	cfg, err := loadConfig(env(map[string]string{
		"SANDBOXRUNNER_ADDR":          ":9999",
		"SANDBOXRUNNER_BACKEND":       "docker",
		"SANDBOXRUNNER_TEST_CONTROLS": "true",
		"SANDBOXRUNNER_RECORD_PATH":   "/out/record.jsonl",
		"SANDBOXRUNNER_IMAGE":         image,
		"SANDBOXRUNNER_TEST_TIMEOUT":  "45s",
	}))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	want := config{addr: ":9999", backend: "docker", testControls: true, recordPath: "/out/record.jsonl", image: image, testTimeout: 45 * time.Second}
	if cfg != want {
		t.Fatalf("config = %+v, want %+v", cfg, want)
	}
}

func TestLoadConfig_InvalidValues_Rejected(t *testing.T) {
	cases := map[string]map[string]string{
		"unknown backend":            {"SANDBOXRUNNER_BACKEND": "podman"},
		"image without digest":       {"SANDBOXRUNNER_IMAGE": "golang:1.27-alpine"},
		"image read as a flag":       {"SANDBOXRUNNER_IMAGE": "--privileged@sha256:" + strings.Repeat("a", 64)},
		"image with whitespace":      {"SANDBOXRUNNER_IMAGE": "go@sha256:" + strings.Repeat("a", 64) + " --privileged"},
		"unparsable timeout":         {"SANDBOXRUNNER_TEST_TIMEOUT": "soon"},
		"non-positive timeout":       {"SANDBOXRUNNER_TEST_TIMEOUT": "0s"},
		"unparsable controls":        {"SANDBOXRUNNER_TEST_CONTROLS": "maybe"},
		"controls without record":    {"SANDBOXRUNNER_TEST_CONTROLS": "true"},
		"record without controls":    {"SANDBOXRUNNER_RECORD_PATH": "/out/record.jsonl"},
		"record with controls false": {"SANDBOXRUNNER_TEST_CONTROLS": "false", "SANDBOXRUNNER_RECORD_PATH": "/out/r"},
	}
	for name, values := range cases {
		if _, err := loadConfig(env(values)); err == nil {
			t.Fatalf("%s: load config succeeded, want an error", name)
		}
	}
}
