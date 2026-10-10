package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/config"
)

func envOf(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

var fullEnv = map[string]string{
	config.KeyDatabaseURL:      "postgres://user:secret@db.invalid:5432/emberling",
	config.KeyAssetStorageRoot: "/var/lib/emberling/assets",
}

func TestParseOptions_Defaults_DryRunWithDayGrace(t *testing.T) {
	opts, err := parseOptions(nil, envOf(fullEnv), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseOptions: %v", err)
	}
	if opts.delete {
		t.Errorf("delete = true, want a dry run by default")
	}
	if opts.grace != 24*time.Hour {
		t.Errorf("grace = %s, want 24h", opts.grace)
	}
}

func TestParseOptions_Flags_AreApplied(t *testing.T) {
	opts, err := parseOptions([]string{"--grace", "90m", "--delete"}, envOf(fullEnv), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseOptions: %v", err)
	}
	if !opts.delete || opts.grace != 90*time.Minute {
		t.Errorf("options = %+v, want delete with a 90m grace", opts)
	}
}

func TestRun_InvalidUsage_ExitsWithUsageCode(t *testing.T) {
	cases := map[string]struct {
		args []string
		env  map[string]string
	}{
		"zero grace":       {args: []string{"--grace", "0s"}, env: fullEnv},
		"negative grace":   {args: []string{"--grace", "-1h"}, env: fullEnv},
		"unknown flag":     {args: []string{"--force"}, env: fullEnv},
		"stray argument":   {args: []string{"now"}, env: fullEnv},
		"no database url":  {env: map[string]string{config.KeyAssetStorageRoot: "/tmp/x"}},
		"no storage root":  {env: map[string]string{config.KeyDatabaseURL: fullEnv[config.KeyDatabaseURL]}},
		"bad database url": {env: map[string]string{config.KeyDatabaseURL: "://secret@", config.KeyAssetStorageRoot: "/tmp/x"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), tc.args, envOf(tc.env), &stdout, &stderr, time.Now)
			if code != exitUsage {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, exitUsage, stderr.String())
			}
			if strings.Contains(stderr.String(), "secret") {
				t.Errorf("stderr exposes the connection string: %q", stderr.String())
			}
		})
	}
}
