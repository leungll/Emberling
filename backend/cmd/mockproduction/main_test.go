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
		"MOCKPRODUCTION_ADDR":          ":19102",
		"MOCKPRODUCTION_TEST_CONTROLS": "true",
		"MOCKPRODUCTION_RECORD_PATH":   "/tmp/record.jsonl",
	}))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if !cfg.testControls || cfg.recordPath != "/tmp/record.jsonl" || cfg.addr != ":19102" {
		t.Fatalf("cfg = %+v, want controls on with the record path and addr", cfg)
	}
}

func TestLoadConfig_InconsistentSettings_AreRejected(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"flag without record path": {"MOCKPRODUCTION_TEST_CONTROLS": "true"},
		"record path without flag": {"MOCKPRODUCTION_RECORD_PATH": "/tmp/record.jsonl"},
		"record path, flag false":  {"MOCKPRODUCTION_TEST_CONTROLS": "false", "MOCKPRODUCTION_RECORD_PATH": "/tmp/record.jsonl"},
		"flag not a boolean":       {"MOCKPRODUCTION_TEST_CONTROLS": "yes please"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadConfig(envOf(env)); err == nil {
				t.Fatal("loadConfig() error = nil, want an error")
			}
		})
	}
}
