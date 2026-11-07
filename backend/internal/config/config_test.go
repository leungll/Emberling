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

func TestConfig_ArtifactMaxBytes_IsOptionalWithDefault(t *testing.T) {
	cfg, err := config.Load(lookup(completeEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AssetStorage.ArtifactMaxBytes != config.DefaultArtifactMaxBytes || config.DefaultArtifactMaxBytes != 16<<20 {
		t.Errorf("artifact max bytes: want the 16 MiB default, got %d", cfg.AssetStorage.ArtifactMaxBytes)
	}

	env := completeEnv()
	env[config.KeyArtifactMaxBytes] = "1024"
	cfg, err = config.Load(lookup(env))
	if err != nil {
		t.Fatalf("Load with %s: %v", config.KeyArtifactMaxBytes, err)
	}
	if cfg.AssetStorage.ArtifactMaxBytes != 1024 {
		t.Errorf("artifact max bytes: want 1024, got %d", cfg.AssetStorage.ArtifactMaxBytes)
	}
}

func TestConfig_NodeAttemptMaxWait_IsOptionalWithDefault(t *testing.T) {
	cfg, err := config.Load(lookup(completeEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.NodeAttemptMaxWait != 24*time.Hour {
		t.Fatalf("default = %v, want 24h", cfg.NodeAttemptMaxWait)
	}

	env := completeEnv()
	env[config.KeyNodeAttemptMaxWait] = "90m"
	cfg, err = config.Load(lookup(env))
	if err != nil {
		t.Fatalf("Load with a value: %v", err)
	}
	if cfg.NodeAttemptMaxWait != 90*time.Minute {
		t.Fatalf("configured = %v, want 90m", cfg.NodeAttemptMaxWait)
	}
}

func TestConfig_NodeAttemptMaxWait_InvalidValueFailsStartup(t *testing.T) {
	for _, value := range []string{"0s", "-1h", "soon"} {
		t.Run(value, func(t *testing.T) {
			env := completeEnv()
			env[config.KeyNodeAttemptMaxWait] = value

			_, err := config.Load(lookup(env))

			var invalid *config.InvalidConfigError
			if !errors.As(err, &invalid) || invalid.Key != config.KeyNodeAttemptMaxWait {
				t.Fatalf("Load with %q: want *config.InvalidConfigError naming the key, got %v", value, err)
			}
		})
	}
}

func TestConfig_Validate_RejectsAMissingNodeAttemptMaxWait(t *testing.T) {
	cfg, err := config.Load(lookup(completeEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.NodeAttemptMaxWait = 0

	var invalid *config.InvalidConfigError
	if err := cfg.Validate(); !errors.As(err, &invalid) || invalid.Key != config.KeyNodeAttemptMaxWait {
		t.Fatalf("Validate: want *config.InvalidConfigError naming the key, got %v", err)
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
		{"artifact size limit", config.KeyArtifactMaxBytes, "0"},
		{"artifact size limit not an integer", config.KeyArtifactMaxBytes, "lots"},
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

// TestConfig_DatabaseMaxConnsAboveInt32_IsRejected pins the upper bound on the pool size:
// pgxpool takes it as an int32, so a larger value must fail validation instead of
// wrapping on conversion.
func TestConfig_DatabaseMaxConnsAboveInt32_IsRejected(t *testing.T) {
	env := completeEnv()
	env[config.KeyDatabaseMaxConns] = "2147483648"

	_, err := config.Load(lookup(env))

	var invalid *config.InvalidConfigError
	if !errors.As(err, &invalid) || invalid.Key != config.KeyDatabaseMaxConns {
		t.Fatalf("Load with %s above int32: want InvalidConfigError for that key, got %v",
			config.KeyDatabaseMaxConns, err)
	}

	env[config.KeyDatabaseMaxConns] = "2147483647"
	if _, err := config.Load(lookup(env)); err != nil {
		t.Fatalf("Load with %s at the int32 maximum: %v", config.KeyDatabaseMaxConns, err)
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

// TestConfig_OperatorToken_Optional proves a deployment without the operator credential
// loads with the approval routes disabled and the default deciding identity.
func TestConfig_OperatorToken_Optional(t *testing.T) {
	cfg, err := config.Load(lookup(completeEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Operator.Enabled() {
		t.Errorf("Operator.Enabled() = true without %s", config.KeyOperatorToken)
	}
	if cfg.Operator.Name != config.DefaultOperatorName {
		t.Errorf("Operator.Name = %q, want the default %q", cfg.Operator.Name, config.DefaultOperatorName)
	}
}

// TestConfig_OperatorToken_ShortTokenRejectedWithoutEchoingIt proves a credential shorter
// than the minimum fails validation by key, and that the error never repeats the value.
func TestConfig_OperatorToken_ShortTokenRejectedWithoutEchoingIt(t *testing.T) {
	env := completeEnv()
	const short = "short-operator-token"
	env[config.KeyOperatorToken] = short

	_, err := config.Load(lookup(env))

	var invalid *config.InvalidConfigError
	if !errors.As(err, &invalid) || invalid.Key != config.KeyOperatorToken {
		t.Fatalf("Load with a %d-byte operator token: want *config.InvalidConfigError for %s, got %v", len(short), config.KeyOperatorToken, err)
	}
	if strings.Contains(err.Error(), short) {
		t.Errorf("error message repeats the rejected credential: %q", err.Error())
	}
}

// TestConfig_OperatorToken_LoadedAndRedacted proves a long enough credential enables the
// routes, the configured name is kept, and the serialised Config never shows the token.
func TestConfig_OperatorToken_LoadedAndRedacted(t *testing.T) {
	env := completeEnv()
	const token = "operator-token-with-at-least-thirty-two-bytes"
	env[config.KeyOperatorToken] = token
	env[config.KeyOperatorName] = "release-manager"

	cfg, err := config.Load(lookup(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Operator.Enabled() || cfg.Operator.Token.Reveal() != token || cfg.Operator.Name != "release-manager" {
		t.Errorf("Operator = enabled %v name %q, want enabled with the configured name", cfg.Operator.Enabled(), cfg.Operator.Name)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if strings.Contains(string(encoded), token) {
		t.Errorf("serialised Config leaks the operator credential: %s", encoded)
	}
	if got := cfg.Operator.Token.String(); strings.Contains(got, token) {
		t.Errorf("Secret.String() leaks the operator credential: %q", got)
	}
}
