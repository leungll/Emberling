package config_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/config"
)

// completeEnv returns a valid deployment environment. Tests remove or corrupt one key at
// a time so that a failure names the key under test.
func completeEnv() map[string]string {
	return map[string]string{
		config.KeyDatabaseURL:                    "postgres://emberling:pw@db:5432/emberling?sslmode=disable",
		config.KeyDatabaseMaxConns:               "10",
		config.KeyAssetStorageRoot:               "/var/lib/emberling/assets",
		config.KeyAssetMaxUploadBytes:            "16777216",
		config.KeyModelProviderBaseURL:           "https://provider.example/v1",
		config.KeyModelProviderAPIKey:            "sk-test-key",
		config.KeyCallbackBaseURL:                "https://emberling.example",
		config.KeyCallbackSigningSecret:          "signing-secret",
		config.KeyReconcileInterval:              "5s",
		config.KeyReconcileBatchSize:             "100",
		config.KeyPendingCallbackTTL:             "24h",
		config.KeyPendingCallbackMaxPayloadBytes: "262144",
		config.KeyTraceMaxFieldBytes:             "8192",
		config.KeyTraceMaxResponseBytes:          "1048576",
	}
}

func lookup(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

func TestConfig_MissingRequired_ReturnsMissingKeys(t *testing.T) {
	env := completeEnv()
	delete(env, config.KeyDatabaseURL)
	delete(env, config.KeyCallbackSigningSecret)
	delete(env, config.KeyTraceMaxResponseBytes)

	_, err := config.Load(lookup(env))

	var missing *config.MissingConfigError
	if !errors.As(err, &missing) {
		t.Fatalf("Load with missing keys: want *config.MissingConfigError, got %v", err)
	}
	want := []string{config.KeyDatabaseURL, config.KeyCallbackSigningSecret, config.KeyTraceMaxResponseBytes}
	for _, key := range want {
		if !containsKey(missing.Keys, key) {
			t.Errorf("missing keys %v: want to contain %s", missing.Keys, key)
		}
	}
	if len(missing.Keys) != len(want) {
		t.Errorf("missing keys: want %d, got %v", len(want), missing.Keys)
	}
}

func TestConfig_MissingRequired_ErrorExcludesValues(t *testing.T) {
	env := completeEnv()
	delete(env, config.KeyDatabaseURL)

	_, err := config.Load(lookup(env))
	if err == nil {
		t.Fatal("Load with missing key: want error, got nil")
	}
	for _, secret := range []string{"sk-test-key", "signing-secret", "pw@db"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error message leaks a configured value: %q", err.Error())
		}
	}
}

func TestConfig_CompleteEnvironment_LoadsEveryCategory(t *testing.T) {
	cfg, err := config.Load(lookup(completeEnv()))
	if err != nil {
		t.Fatalf("Load with complete environment: %v", err)
	}

	if got := cfg.Database.URL.Reveal(); !strings.HasPrefix(got, "postgres://") {
		t.Errorf("database URL: want the configured value, got %q", got)
	}
	if cfg.Database.MaxConns != 10 {
		t.Errorf("database max conns: want 10, got %d", cfg.Database.MaxConns)
	}
	if cfg.AssetStorage.Root != "/var/lib/emberling/assets" {
		t.Errorf("asset storage root: got %q", cfg.AssetStorage.Root)
	}
	if cfg.ModelProvider.BaseURL != "https://provider.example/v1" {
		t.Errorf("model provider base URL: got %q", cfg.ModelProvider.BaseURL)
	}
	if cfg.Callback.BaseURL != "https://emberling.example" {
		t.Errorf("callback base URL: got %q", cfg.Callback.BaseURL)
	}
	if cfg.Reconciliation.Interval != 5*time.Second || cfg.Reconciliation.BatchSize != 100 {
		t.Errorf("reconciliation: got %v / %d", cfg.Reconciliation.Interval, cfg.Reconciliation.BatchSize)
	}
	if cfg.PendingCallback.TTL != 24*time.Hour || cfg.PendingCallback.MaxPayloadBytes != 262144 {
		t.Errorf("pending callback: got %v / %d", cfg.PendingCallback.TTL, cfg.PendingCallback.MaxPayloadBytes)
	}
	if cfg.Projection.MaxFieldBytes != 8192 || cfg.Projection.MaxResponseBytes != 1048576 {
		t.Errorf("projection: got %d / %d", cfg.Projection.MaxFieldBytes, cfg.Projection.MaxResponseBytes)
	}
	if cfg.HTTP.Addr != config.DefaultHTTPAddr {
		t.Errorf("http addr: want the documented default %q, got %q", config.DefaultHTTPAddr, cfg.HTTP.Addr)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate on a loaded Config: %v", err)
	}
}

func TestConfig_NonPositiveBound_ReturnsInvalidConfigError(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		bad  string
	}{
		{"reconcile batch size", config.KeyReconcileBatchSize, "0"},
		{"reconcile interval", config.KeyReconcileInterval, "-1s"},
		{"pending callback payload limit", config.KeyPendingCallbackMaxPayloadBytes, "0"},
		{"trace field limit", config.KeyTraceMaxFieldBytes, "-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := completeEnv()
			env[tc.key] = tc.bad

			_, err := config.Load(lookup(env))

			var invalid *config.InvalidConfigError
			if !errors.As(err, &invalid) {
				t.Fatalf("Load with %s=%s: want *config.InvalidConfigError, got %v", tc.key, tc.bad, err)
			}
			if invalid.Key != tc.key {
				t.Errorf("invalid key: want %s, got %s", tc.key, invalid.Key)
			}
			if strings.Contains(invalid.Error(), tc.bad) && tc.bad != "0" {
				t.Errorf("error message repeats the rejected value: %q", invalid.Error())
			}
		})
	}
}

func TestConfig_NonHTTPCallbackURL_IsRejected(t *testing.T) {
	env := completeEnv()
	env[config.KeyCallbackBaseURL] = "emberling.example"

	_, err := config.Load(lookup(env))

	var invalid *config.InvalidConfigError
	if !errors.As(err, &invalid) || invalid.Key != config.KeyCallbackBaseURL {
		t.Fatalf("Load with a relative callback URL: want InvalidConfigError for %s, got %v",
			config.KeyCallbackBaseURL, err)
	}
}

func TestConfig_MarshalJSON_RedactsEverySecret(t *testing.T) {
	cfg, err := config.Load(lookup(completeEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	for _, secret := range []string{"sk-test-key", "signing-secret", "pw@db"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("serialised Config leaks %q: %s", secret, encoded)
		}
	}
}

func containsKey(keys []string, want string) bool {
	for _, key := range keys {
		if key == want {
			return true
		}
	}
	return false
}
