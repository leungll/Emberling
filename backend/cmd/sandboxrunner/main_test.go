package main

import (
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/fixture/sandboxrunner"
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

// axSettings is a complete ax backend configuration.
func axSettings() map[string]string {
	return map[string]string{
		"SANDBOXRUNNER_BACKEND":          "ax",
		"SANDBOXRUNNER_IMAGE":            "registry.local/sandboxsupervisor@sha256:" + strings.Repeat("b", 64),
		"SANDBOXRUNNER_AX_ATESPACE":      "emberling-sandbox",
		"SANDBOXRUNNER_AX_ROUTER_URL":    "http://127.0.0.1:18080",
		"SANDBOXRUNNER_AX_CLI":           "/opt/ax/bin/ax",
		"SANDBOXRUNNER_AX_READY_TIMEOUT": "90s",
		"SANDBOXRUNNER_TEST_TIMEOUT":     "300s",
	}
}

func TestLoadConfig_AXBackend_ReadsEveryKey(t *testing.T) {
	cfg, err := loadConfig(env(axSettings()))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	want := config{
		addr: ":9103", backend: "ax", image: axSettings()["SANDBOXRUNNER_IMAGE"], testTimeout: 300 * time.Second,
		axAtespace: "emberling-sandbox", axRouterURL: "http://127.0.0.1:18080", axCLI: "/opt/ax/bin/ax", axReadyTimeout: 90 * time.Second,
	}
	if cfg != want {
		t.Fatalf("config = %+v, want %+v", cfg, want)
	}
}

func TestLoadConfig_AXBackendMissingSetting_FailsNamingKeyOnly(t *testing.T) {
	for _, key := range []string{
		"SANDBOXRUNNER_IMAGE", "SANDBOXRUNNER_AX_ATESPACE", "SANDBOXRUNNER_AX_ROUTER_URL",
		"SANDBOXRUNNER_AX_CLI", "SANDBOXRUNNER_AX_READY_TIMEOUT",
	} {
		values := axSettings()
		delete(values, key)
		_, err := loadConfig(env(values))
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("without %s: error = %v, want one naming the key", key, err)
		}
		for other, value := range values {
			if other != "SANDBOXRUNNER_BACKEND" && strings.Contains(err.Error(), value) {
				t.Fatalf("without %s: error %q echoes the value of %s", key, err, other)
			}
		}
	}

	invalid := map[string]string{
		"SANDBOXRUNNER_AX_ATESPACE":      "Not_A_Label",
		"SANDBOXRUNNER_AX_ROUTER_URL":    "router.internal:8080",
		"SANDBOXRUNNER_AX_READY_TIMEOUT": "0s",
	}
	for key, value := range invalid {
		values := axSettings()
		values[key] = value
		_, err := loadConfig(env(values))
		if err == nil || !strings.Contains(err.Error(), key) || strings.Contains(err.Error(), value) {
			t.Fatalf("%s=%q: error = %v, want one naming the key but not the value", key, value, err)
		}
	}
}

func TestLoadConfig_AXBackendImageNotPinnedByDigest_Fails(t *testing.T) {
	for _, image := range []string{"registry.local/sandboxsupervisor:v1", "-x@sha256:" + strings.Repeat("c", 64)} {
		values := axSettings()
		values["SANDBOXRUNNER_IMAGE"] = image
		if _, err := loadConfig(env(values)); err == nil || !strings.Contains(err.Error(), "SANDBOXRUNNER_IMAGE") {
			t.Fatalf("image %q: error = %v, want a digest-pinning failure", image, err)
		}
	}
}

func TestLoadConfig_UnknownBackend_ListsMockDockerAndAX(t *testing.T) {
	_, err := loadConfig(env(map[string]string{"SANDBOXRUNNER_BACKEND": "podman"}))
	if err == nil {
		t.Fatal("load config succeeded for an unknown backend")
	}
	for _, name := range []string{`"mock"`, `"docker"`, `"ax"`} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error %q does not list %s", err, name)
		}
	}
}
