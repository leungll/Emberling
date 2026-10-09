// Package config loads deployment configuration from the process environment. It depends
// on the standard library only: configuration is read before any store, Runtime or
// transport component exists, and nothing in this package may reach back into them.
package config

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// EnvPrefix is prepended to every configuration key.
const EnvPrefix = "EMBERLING_"

// Configuration keys, one per operations configuration category. They are constants
// because deployment manifests, the readiness sequence and error messages must name
// exactly the same keys.
const (
	KeyDatabaseURL      = EnvPrefix + "DATABASE_URL"
	KeyDatabaseMaxConns = EnvPrefix + "DATABASE_MAX_CONNS"

	KeyAssetStorageRoot    = EnvPrefix + "ASSET_STORAGE_ROOT"
	KeyAssetMaxUploadBytes = EnvPrefix + "ASSET_MAX_UPLOAD_BYTES"

	KeyModelProviderBaseURL = EnvPrefix + "MODEL_PROVIDER_BASE_URL"
	KeyModelProviderAPIKey  = EnvPrefix + "MODEL_PROVIDER_API_KEY"

	KeyModelOpenAIBaseURL = EnvPrefix + "MODEL_OPENAI_BASE_URL"
	KeyModelOpenAIAPIKey  = EnvPrefix + "MODEL_OPENAI_API_KEY"
	KeyModelOpenAIModel   = EnvPrefix + "MODEL_OPENAI_MODEL"
	KeyModelOpenAITimeout = EnvPrefix + "MODEL_OPENAI_TIMEOUT"

	KeyProductionBaseURL = EnvPrefix + "PRODUCTION_BASE_URL"

	KeyCallbackBaseURL       = EnvPrefix + "CALLBACK_BASE_URL"
	KeyCallbackSigningSecret = EnvPrefix + "CALLBACK_SIGNING_SECRET"

	KeyReconcileInterval  = EnvPrefix + "RECONCILE_INTERVAL"
	KeyReconcileBatchSize = EnvPrefix + "RECONCILE_BATCH_SIZE"

	KeyPendingCallbackTTL             = EnvPrefix + "PENDING_CALLBACK_TTL"
	KeyPendingCallbackMaxPayloadBytes = EnvPrefix + "PENDING_CALLBACK_MAX_PAYLOAD_BYTES"

	KeyTraceMaxFieldBytes    = EnvPrefix + "TRACE_MAX_FIELD_BYTES"
	KeyTraceMaxResponseBytes = EnvPrefix + "TRACE_MAX_RESPONSE_BYTES"

	KeyHTTPAddr = EnvPrefix + "HTTP_ADDR"
)

// DefaultHTTPAddr is the only defaulted value: the listen address is not one of
// the required categories, while every other key must be set explicitly so that no
// deployment silently inherits an unbounded or unintended operational limit.
const DefaultHTTPAddr = ":8080"

// Config is the fully resolved deployment configuration. It is passed by value: no part
// of the process may mutate it after startup.
type Config struct {
	Database        Database
	AssetStorage    AssetStorage
	ModelProvider   ModelProvider
	OpenAIModel     OpenAIModel
	Production      Production
	Callback        Callback
	Reconciliation  Reconciliation
	PendingCallback PendingCallback
	Projection      Projection
	HTTP            HTTP
}

// Database addresses PostgreSQL, the authority for Emberling-owned execution facts.
type Database struct {
	// URL is a Secret because a libpq connection string carries the password.
	URL      Secret
	MaxConns int
}

// AssetStorage locates Asset and Execution Artifact content, and bounds what one upload
// may put there.
type AssetStorage struct {
	Root string
	// MaxUploadBytes caps a single Asset upload. It is enforced while the content
	// streams, so oversized content is refused before it is fully accepted.
	MaxUploadBytes int
}

// ModelProvider addresses the Model Provider used by the Registry.
type ModelProvider struct {
	BaseURL string
	APIKey  Secret
}

// OpenAIModel addresses the optional OpenAI-compatible Model Adapter. Either every
// required field is set and the Adapter is registered, or none is and it is not; a
// partial configuration is a startup error rather than a silently missing model.
type OpenAIModel struct {
	BaseURL string
	APIKey  Secret
	Model   string
	// Timeout bounds one HTTP request. Zero leaves the call bounded only by the caller's
	// context, such as the Agent Run deadline.
	Timeout time.Duration
}

// Enabled reports whether the OpenAI-compatible Model Adapter is configured.
func (m OpenAIModel) Enabled() bool {
	return m.BaseURL != "" || !m.APIKey.IsZero() || m.Model != ""
}

// Production addresses the optional production deployment service the deploy Tool calls.
// An empty BaseURL means the Tool is not registered.
type Production struct {
	BaseURL string
}

// Enabled reports whether the production deployment service is configured.
func (p Production) Enabled() bool {
	return p.BaseURL != ""
}

// Callback carries the externally reachable base URL and the signing Secret used to
// authenticate asynchronous Provider callbacks.
type Callback struct {
	BaseURL       string
	SigningSecret Secret
}

// Reconciliation bounds the recovery scan.
type Reconciliation struct {
	Interval  time.Duration
	BatchSize int
}

// PendingCallback bounds how long an unmatched callback is retained and how large an
// accepted callback body may be.
type PendingCallback struct {
	TTL             time.Duration
	MaxPayloadBytes int
}

// Projection bounds Trace and API rendering only. It never truncates authoritative
// Execution State.
type Projection struct {
	MaxFieldBytes    int
	MaxResponseBytes int
}

// HTTP configures the API listener.
type HTTP struct {
	Addr string
}

// MissingConfigError reports required keys that were absent or empty. It lists key names
// only; a configuration value is never included, because some of them are Secrets.
type MissingConfigError struct {
	Keys []string
}

func (e *MissingConfigError) Error() string {
	return fmt.Sprintf("config: missing required environment variables: %s", strings.Join(e.Keys, ", "))
}

// InvalidConfigError reports a key whose value could not be used. The offending value is
// deliberately excluded from the message for the same reason as above.
type InvalidConfigError struct {
	Key    string
	Reason string
}

func (e *InvalidConfigError) Error() string {
	return fmt.Sprintf("config: invalid value for %s: %s", e.Key, e.Reason)
}

// Load reads configuration through env, which is injected so that tests never mutate the
// process environment. Every required key is reported at once: an operator fixing a
// deployment should not have to restart to discover the next missing key.
func Load(env func(string) string) (Config, error) {
	if env == nil {
		return Config{}, errors.New("config: env lookup function is required")
	}
	l := &loader{env: env}

	cfg := Config{
		Database: Database{
			URL:      Secret{value: l.required(KeyDatabaseURL)},
			MaxConns: l.requiredInt(KeyDatabaseMaxConns),
		},
		AssetStorage: AssetStorage{
			Root:           l.required(KeyAssetStorageRoot),
			MaxUploadBytes: l.requiredInt(KeyAssetMaxUploadBytes),
		},
		ModelProvider: ModelProvider{
			BaseURL: l.requiredURL(KeyModelProviderBaseURL),
			APIKey:  Secret{value: l.required(KeyModelProviderAPIKey)},
		},
		OpenAIModel: l.openAIModel(),
		Production: Production{
			BaseURL: l.optionalProviderURL(KeyProductionBaseURL),
		},
		Callback: Callback{
			BaseURL:       l.requiredURL(KeyCallbackBaseURL),
			SigningSecret: Secret{value: l.required(KeyCallbackSigningSecret)},
		},
		Reconciliation: Reconciliation{
			Interval:  l.requiredDuration(KeyReconcileInterval),
			BatchSize: l.requiredInt(KeyReconcileBatchSize),
		},
		PendingCallback: PendingCallback{
			TTL:             l.requiredDuration(KeyPendingCallbackTTL),
			MaxPayloadBytes: l.requiredInt(KeyPendingCallbackMaxPayloadBytes),
		},
		Projection: Projection{
			MaxFieldBytes:    l.requiredInt(KeyTraceMaxFieldBytes),
			MaxResponseBytes: l.requiredInt(KeyTraceMaxResponseBytes),
		},
		HTTP: HTTP{
			Addr: l.optional(KeyHTTPAddr, DefaultHTTPAddr),
		},
	}

	if err := l.err(); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate re-checks the invariants Load enforces. The readiness sequence runs it as its
// own step so that a Config assembled by any other path cannot make the process ready.
func (c Config) Validate() error {
	var missing []string
	for _, item := range []struct {
		key   string
		empty bool
	}{
		{KeyDatabaseURL, c.Database.URL.IsZero()},
		{KeyAssetStorageRoot, c.AssetStorage.Root == ""},
		{KeyModelProviderBaseURL, c.ModelProvider.BaseURL == ""},
		{KeyModelProviderAPIKey, c.ModelProvider.APIKey.IsZero()},
		{KeyCallbackBaseURL, c.Callback.BaseURL == ""},
		{KeyCallbackSigningSecret, c.Callback.SigningSecret.IsZero()},
		{KeyHTTPAddr, c.HTTP.Addr == ""},
	} {
		if item.empty {
			missing = append(missing, item.key)
		}
	}
	if c.OpenAIModel.Enabled() {
		for _, item := range []struct {
			key   string
			empty bool
		}{
			{KeyModelOpenAIBaseURL, c.OpenAIModel.BaseURL == ""},
			{KeyModelOpenAIAPIKey, c.OpenAIModel.APIKey.IsZero()},
			{KeyModelOpenAIModel, c.OpenAIModel.Model == ""},
		} {
			if item.empty {
				missing = append(missing, item.key)
			}
		}
	}
	if len(missing) > 0 {
		return &MissingConfigError{Keys: missing}
	}

	for _, item := range []struct {
		key   string
		value int
	}{
		{KeyDatabaseMaxConns, c.Database.MaxConns},
		{KeyAssetMaxUploadBytes, c.AssetStorage.MaxUploadBytes},
		{KeyReconcileBatchSize, c.Reconciliation.BatchSize},
		{KeyPendingCallbackMaxPayloadBytes, c.PendingCallback.MaxPayloadBytes},
		{KeyTraceMaxFieldBytes, c.Projection.MaxFieldBytes},
		{KeyTraceMaxResponseBytes, c.Projection.MaxResponseBytes},
	} {
		if item.value <= 0 {
			return &InvalidConfigError{Key: item.key, Reason: "must be a positive integer"}
		}
	}
	// pgxpool sizes its pool with an int32.
	if c.Database.MaxConns > math.MaxInt32 {
		return &InvalidConfigError{Key: KeyDatabaseMaxConns, Reason: "must not exceed 2147483647"}
	}

	for _, item := range []struct {
		key   string
		value time.Duration
	}{
		{KeyReconcileInterval, c.Reconciliation.Interval},
		{KeyPendingCallbackTTL, c.PendingCallback.TTL},
	} {
		if item.value <= 0 {
			return &InvalidConfigError{Key: item.key, Reason: "must be a positive duration"}
		}
	}
	if c.OpenAIModel.Timeout < 0 {
		return &InvalidConfigError{Key: KeyModelOpenAITimeout, Reason: "must be a positive duration"}
	}
	if c.OpenAIModel.Timeout > 0 && !c.OpenAIModel.Enabled() {
		return &InvalidConfigError{Key: KeyModelOpenAITimeout, Reason: "set only together with " + KeyModelOpenAIBaseURL}
	}
	return nil
}

// loader accumulates every problem in one pass instead of failing on the first key.
type loader struct {
	env     func(string) string
	missing []string
	invalid error
}

func (l *loader) err() error {
	if len(l.missing) > 0 {
		return &MissingConfigError{Keys: l.missing}
	}
	return l.invalid
}

func (l *loader) optional(key, fallback string) string {
	if value := strings.TrimSpace(l.env(key)); value != "" {
		return value
	}
	return fallback
}

func (l *loader) required(key string) string {
	value := strings.TrimSpace(l.env(key))
	if value == "" {
		l.missing = append(l.missing, key)
	}
	return value
}

func (l *loader) requiredURL(key string) string {
	value := l.required(key)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		l.fail(key, "must be an absolute http or https URL")
		return ""
	}
	return strings.TrimSuffix(value, "/")
}

// openAIModel reads the optional OpenAI-compatible Model Adapter group. When none of its
// required keys is set the group is disabled; once any is set, every required key is.
func (l *loader) openAIModel() OpenAIModel {
	keys := []string{KeyModelOpenAIBaseURL, KeyModelOpenAIAPIKey, KeyModelOpenAIModel}
	enabled := false
	for _, key := range keys {
		if strings.TrimSpace(l.env(key)) != "" {
			enabled = true
		}
	}
	timeout := l.optionalDuration(KeyModelOpenAITimeout)
	if !enabled {
		if timeout > 0 {
			l.fail(KeyModelOpenAITimeout, "set only together with "+KeyModelOpenAIBaseURL)
		}
		return OpenAIModel{}
	}
	return OpenAIModel{
		BaseURL: l.requiredProviderURL(KeyModelOpenAIBaseURL),
		APIKey:  Secret{value: l.required(KeyModelOpenAIAPIKey)},
		Model:   l.required(KeyModelOpenAIModel),
		Timeout: timeout,
	}
}

// requiredProviderURL is requiredURL that additionally refuses user information, a query
// and a fragment: the value is a base for request paths and appears in error messages, so
// it may not carry a credential of its own.
func (l *loader) requiredProviderURL(key string) string {
	value := l.requiredURL(key)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		l.fail(key, "must not carry user information, a query or a fragment")
		return ""
	}
	return value
}

// optionalProviderURL is requiredProviderURL for a key that may be absent: an unset key
// yields "", a set one must be a usable base URL.
func (l *loader) optionalProviderURL(key string) string {
	if strings.TrimSpace(l.env(key)) == "" {
		return ""
	}
	return l.requiredProviderURL(key)
}

func (l *loader) optionalDuration(key string) time.Duration {
	value := strings.TrimSpace(l.env(key))
	if value == "" {
		return 0
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		l.fail(key, "must be a Go duration such as 5s or 24h")
		return 0
	}
	if parsed <= 0 {
		l.fail(key, "must be a positive duration")
		return 0
	}
	return parsed
}

func (l *loader) requiredInt(key string) int {
	value := l.required(key)
	if value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		l.fail(key, "must be an integer")
		return 0
	}
	if parsed <= 0 {
		l.fail(key, "must be a positive integer")
		return 0
	}
	return parsed
}

func (l *loader) requiredDuration(key string) time.Duration {
	value := l.required(key)
	if value == "" {
		return 0
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		l.fail(key, "must be a Go duration such as 5s or 24h")
		return 0
	}
	if parsed <= 0 {
		l.fail(key, "must be a positive duration")
		return 0
	}
	return parsed
}

// fail records the first invalid value. The value itself is never captured.
func (l *loader) fail(key, reason string) {
	if l.invalid == nil {
		l.invalid = &InvalidConfigError{Key: key, Reason: reason}
	}
}
