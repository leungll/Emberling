package main

import "testing"

func envOf(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestLoadConfig_NoFlag_ControlsOffWithDefaultAddr(t *testing.T) {
	cfg, err := loadConfig(envOf(nil))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.testControls || cfg.recordPath != "" || cfg.addr != defaultAddr {
		t.Fatalf("cfg = %+v, want controls off and default addr", cfg)
	}
}

func TestLoadConfig_FlagWithRecordPath_EnablesControls(t *testing.T) {
	cfg, err := loadConfig(envOf(map[string]string{
		"MOCKPROVIDER_TEST_CONTROLS": "true",
		"MOCKPROVIDER_RECORD_PATH":   "/tmp/record.jsonl",
	}))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if !cfg.testControls || cfg.recordPath != "/tmp/record.jsonl" {
		t.Fatalf("cfg = %+v, want controls on with the record path", cfg)
	}
}

func TestLoadConfig_InconsistentSettings_AreRejected(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"flag without record path": {"MOCKPROVIDER_TEST_CONTROLS": "true"},
		"record path without flag": {"MOCKPROVIDER_RECORD_PATH": "/tmp/record.jsonl"},
		"record path, flag false":  {"MOCKPROVIDER_TEST_CONTROLS": "false", "MOCKPROVIDER_RECORD_PATH": "/tmp/record.jsonl"},
		"flag not a boolean":       {"MOCKPROVIDER_TEST_CONTROLS": "yes please"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadConfig(envOf(env)); err == nil {
				t.Fatal("loadConfig() error = nil, want an error")
			}
		})
	}
}

func TestLoadConfig_PublicURLUnset_KeepsRequestOrigin(t *testing.T) {
	cfg, err := loadConfig(envOf(nil))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.publicBaseURL != "" {
		t.Fatalf("publicBaseURL = %q, want empty", cfg.publicBaseURL)
	}
}

func TestLoadConfig_PublicURLSet_IsNormalised(t *testing.T) {
	for raw, want := range map[string]string{
		"http://localhost:9101":         "http://localhost:9101",
		"http://localhost:9101/":        "http://localhost:9101",
		"https://media.example/mock//":  "https://media.example/mock",
		"https://media.example/mock/v2": "https://media.example/mock/v2",
	} {
		t.Run(raw, func(t *testing.T) {
			cfg, err := loadConfig(envOf(map[string]string{publicURLEnv: raw}))
			if err != nil {
				t.Fatalf("loadConfig() error = %v", err)
			}
			if cfg.publicBaseURL != want {
				t.Fatalf("publicBaseURL = %q, want %q", cfg.publicBaseURL, want)
			}
		})
	}
}

func TestLoadConfig_InvalidPublicURL_IsRejected(t *testing.T) {
	for name, raw := range map[string]string{
		"no scheme":        "localhost:9101",
		"relative path":    "/v1",
		"non-http scheme":  "ftp://localhost:9101",
		"no host":          "http://",
		"user information": "http://user:secret@localhost:9101",
		"query":            "http://localhost:9101?token=x",
		"fragment":         "http://localhost:9101#top",
		"unparseable":      "http://[::1",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadConfig(envOf(map[string]string{publicURLEnv: raw})); err == nil {
				t.Fatal("loadConfig() error = nil, want an error")
			}
		})
	}
}
