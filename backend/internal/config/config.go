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
	KeyArtifactMaxBytes    = EnvPrefix + "ARTIFACT_MAX_BYTES"

	KeyModelProviderBaseURL = EnvPrefix + "MODEL_PROVIDER_BASE_URL"
	KeyModelProviderAPIKey  = EnvPrefix + "MODEL_PROVIDER_API_KEY"

	// The OpenAI-compatible Model Adapter group: one shared endpoint, key and timeout,
	// and the list of models registered behind them.
	KeyModels       = EnvPrefix + "MODELS"
	KeyModelBaseURL = EnvPrefix + "MODEL_BASE_URL"
	KeyModelAPIKey  = EnvPrefix + "MODEL_API_KEY"
	KeyModelTimeout = EnvPrefix + "MODEL_TIMEOUT"
	// KeyModelOpenRouterRequireParameters is an optional boolean switch of the same group.
	KeyModelOpenRouterRequireParameters = EnvPrefix + "MODEL_OPENROUTER_REQUIRE_PARAMETERS"

	// Removed keys of the earlier single-model group. Setting any of them fails startup
	// and names its replacement; see removedKeys.
	KeyRemovedOpenAIBaseURL = EnvPrefix + "MODEL_OPENAI_BASE_URL"
	KeyRemovedOpenAIAPIKey  = EnvPrefix + "MODEL_OPENAI_API_KEY"
	KeyRemovedOpenAIModel   = EnvPrefix + "MODEL_OPENAI_MODEL"
	KeyRemovedOpenAIModels  = EnvPrefix + "MODEL_OPENAI_MODELS"
	KeyRemovedOpenAITimeout = EnvPrefix + "MODEL_OPENAI_TIMEOUT"

	KeyProductionBaseURL = EnvPrefix + "PRODUCTION_BASE_URL"
	KeyReleaseBaseURL    = EnvPrefix + "RELEASE_BASE_URL"

	KeyCallbackBaseURL       = EnvPrefix + "CALLBACK_BASE_URL"
	KeyCallbackSigningSecret = EnvPrefix + "CALLBACK_SIGNING_SECRET"

	KeyReconcileInterval  = EnvPrefix + "RECONCILE_INTERVAL"
	KeyReconcileBatchSize = EnvPrefix + "RECONCILE_BATCH_SIZE"

	KeyPendingCallbackTTL             = EnvPrefix + "PENDING_CALLBACK_TTL"
	KeyNodeAttemptMaxWait             = EnvPrefix + "NODE_ATTEMPT_MAX_WAIT"
	KeyPendingCallbackMaxPayloadBytes = EnvPrefix + "PENDING_CALLBACK_MAX_PAYLOAD_BYTES"

	KeyTraceMaxFieldBytes    = EnvPrefix + "TRACE_MAX_FIELD_BYTES"
	KeyTraceMaxResponseBytes = EnvPrefix + "TRACE_MAX_RESPONSE_BYTES"

	KeyHTTPAddr = EnvPrefix + "HTTP_ADDR"

	// KeyOperatorToken is the optional shared credential of the approval routes; without
	// it those routes are not served. KeyOperatorName is the identity recorded as the
	// deciding person.
	KeyOperatorToken = EnvPrefix + "OPERATOR_TOKEN"
	KeyOperatorName  = EnvPrefix + "OPERATOR_NAME"
)

// DefaultOperatorName is the deciding identity when none is configured.
const DefaultOperatorName = "demo-operator"

// MinOperatorTokenBytes is the shortest operator credential accepted.
const MinOperatorTokenBytes = 32

// DefaultHTTPAddr is the only defaulted value: the listen address is not one of
// the required categories, while every other key must be set explicitly so that no
// deployment silently inherits an unbounded or unintended operational limit.
const DefaultHTTPAddr = ":8080"

// DefaultNodeAttemptMaxWait applies when KeyNodeAttemptMaxWait is unset.
const DefaultNodeAttemptMaxWait = 24 * time.Hour

// Config is the fully resolved deployment configuration. It is passed by value: no part
// of the process may mutate it after startup.
type Config struct {
	Database      Database
	AssetStorage  AssetStorage
	ModelProvider ModelProvider
	OpenAIModel   OpenAIModel
	ImageModel    ImageModel
	TaskGateway   TaskGateway
	Production    Production
	Callback      Callback
	SandboxRunner SandboxRunner
	// VerifyRunner locates the sandbox runner the verify_patch Tool dispatches to; its
	// zero value leaves the Tool unregistered.
	VerifyRunner    VerifyRunner
	Reconciliation  Reconciliation
	PendingCallback PendingCallback
	Projection      Projection
	HTTP            HTTP
	Operator        Operator
	// ProviderCapacity is the in-flight task limit per asynchronous Provider id; a
	// Provider absent from it is not limited.
	ProviderCapacity map[string]int
	// NodeAttemptMaxWait bounds how long a dispatched Node Attempt with no deadline of its
	// own may wait for its Provider. Dispatch writes it onto the Attempt, so changing it
	// affects only later dispatches.
	NodeAttemptMaxWait time.Duration
	// WorkspaceRepositories maps each registered repository identifier to its absolute
	// path. Empty means no Agent workspace can be configured.
	WorkspaceRepositories map[string]string
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
	// ArtifactMaxBytes caps one Execution Artifact a Tool saves, such as a generated
	// image. It is optional and defaults to DefaultArtifactMaxBytes.
	ArtifactMaxBytes int
}

// DefaultArtifactMaxBytes is the Execution Artifact size cap when none is configured.
const DefaultArtifactMaxBytes = 16 << 20

// ModelProvider addresses the Model Provider used by the Registry.
type ModelProvider struct {
	BaseURL string
	APIKey  Secret
}

// OpenAIModel addresses the optional OpenAI-compatible Model Adapter. Either every
// required field is set and one model is registered per list entry, or none is and the
// Adapter is not registered; a partial configuration is a startup error rather than a
// silently missing model. The base URL, key and timeout are shared by every model.
type OpenAIModel struct {
	BaseURL string
	APIKey  Secret
	Models  []OpenAIModelEntry
	// Timeout bounds one HTTP request. Zero leaves the call bounded only by the caller's
	// context, such as the Agent Run deadline.
	Timeout time.Duration
	// OpenRouterRequireParameters asks OpenRouter to route each request only to upstreams
	// that honour every request parameter, so strict structured output is not sent to one
	// that ignores it. Off by default; it may be on only when the group is configured.
	OpenRouterRequireParameters bool
}

// OpenAIModelEntry is one registered model. ID is the stable Model ID a Definition
// stores; Model is the Provider-side model name, which never enters a Definition;
// DisplayName is what Studio shows. Description is the optional one-line note Studio shows
// with the model; empty means none. VisionInput declares that the model accepts inline
// images, so it may serve as an Agent node's review model; it is accepted only on a
// decision model entry.
type OpenAIModelEntry struct {
	ID          string
	Model       string
	DisplayName string
	Description string
	VisionInput bool
}

// Enabled reports whether the OpenAI-compatible Model Adapter is configured.
func (m OpenAIModel) Enabled() bool {
	return m.BaseURL != "" || !m.APIKey.IsZero() || len(m.Models) > 0
}

// Production addresses the optional production deployment service. BaseURL is the
// service the deploy Tool calls; ReleaseBaseURL is the service whose reconcilable release
// surface the deploy_change Tool calls. Each empty URL leaves its Tool unregistered, so
// a deployment can run either workload without the other's Tool.
type Production struct {
	BaseURL        string
	ReleaseBaseURL string
}

// Enabled reports whether the production deployment service of the deploy Tool is configured.
func (p Production) Enabled() bool {
	return p.BaseURL != ""
}

// ReleaseEnabled reports whether the release surface of the deploy_change Tool is configured.
func (p Production) ReleaseEnabled() bool {
	return p.ReleaseBaseURL != ""
}

// Callback carries the externally reachable base URL and the signing Secret used to
// authenticate asynchronous Provider callbacks.
type Callback struct {
	BaseURL       string
	SigningSecret Secret
}

// Operator authenticates the approval routes. Token is a Secret and optional: when it is
// not configured the Backend serves no approval route. Name is the fixed identity recorded
// as the deciding person.
type Operator struct {
	Token Secret
	Name  string
}

// Enabled reports whether the approval routes are configured.
func (o Operator) Enabled() bool { return !o.Token.IsZero() }

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
	l.checkRemovedKeys()

	cfg := Config{
		Database: Database{
			URL:      Secret{value: l.required(KeyDatabaseURL)},
			MaxConns: l.requiredInt(KeyDatabaseMaxConns),
		},
		AssetStorage: AssetStorage{
			Root:             l.required(KeyAssetStorageRoot),
			MaxUploadBytes:   l.requiredInt(KeyAssetMaxUploadBytes),
			ArtifactMaxBytes: l.optionalInt(KeyArtifactMaxBytes, DefaultArtifactMaxBytes),
		},
		ModelProvider: ModelProvider{
			BaseURL: l.requiredURL(KeyModelProviderBaseURL),
			APIKey:  Secret{value: l.required(KeyModelProviderAPIKey)},
		},
		OpenAIModel: l.openAIModel(),
		ImageModel:  l.imageModel(),
		TaskGateway: l.taskGateway(),
		Production: Production{
			BaseURL:        l.optionalProviderURL(KeyProductionBaseURL),
			ReleaseBaseURL: l.optionalProviderURL(KeyReleaseBaseURL),
		},
		Callback: Callback{
			BaseURL:       l.requiredURL(KeyCallbackBaseURL),
			SigningSecret: Secret{value: l.required(KeyCallbackSigningSecret)},
		},
		SandboxRunner: l.sandboxRunner(),
		VerifyRunner:  l.verifyRunner(),
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
		Operator: Operator{
			Token: Secret{value: l.optional(KeyOperatorToken, "")},
			Name:  l.optional(KeyOperatorName, DefaultOperatorName),
		},
		ProviderCapacity:      l.providerCapacity(),
		NodeAttemptMaxWait:    l.durationOr(KeyNodeAttemptMaxWait, DefaultNodeAttemptMaxWait),
		WorkspaceRepositories: l.workspaceRepositories(),
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
			{KeyModelBaseURL, c.OpenAIModel.BaseURL == ""},
			{KeyModelAPIKey, c.OpenAIModel.APIKey.IsZero()},
			{KeyModels, len(c.OpenAIModel.Models) == 0},
		} {
			if item.empty {
				missing = append(missing, item.key)
			}
		}
	}
	missing = append(missing, c.ImageModel.missingKeys()...)
	missing = append(missing, c.TaskGateway.missingKeys()...)
	if len(missing) > 0 {
		return &MissingConfigError{Keys: missing}
	}

	for _, item := range []struct {
		key   string
		value int
	}{
		{KeyDatabaseMaxConns, c.Database.MaxConns},
		{KeyAssetMaxUploadBytes, c.AssetStorage.MaxUploadBytes},
		{KeyArtifactMaxBytes, c.AssetStorage.ArtifactMaxBytes},
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
		{KeyNodeAttemptMaxWait, c.NodeAttemptMaxWait},
	} {
		if item.value <= 0 {
			return &InvalidConfigError{Key: item.key, Reason: "must be a positive duration"}
		}
	}
	if c.OpenAIModel.Timeout < 0 {
		return &InvalidConfigError{Key: KeyModelTimeout, Reason: "must be a positive duration"}
	}
	if c.OpenAIModel.Timeout > 0 && !c.OpenAIModel.Enabled() {
		return &InvalidConfigError{Key: KeyModelTimeout, Reason: "set only together with " + KeyModelBaseURL}
	}
	if c.OpenAIModel.OpenRouterRequireParameters && !c.OpenAIModel.Enabled() {
		return &InvalidConfigError{Key: KeyModelOpenRouterRequireParameters, Reason: "set only together with " + KeyModels}
	}
	if c.OpenAIModel.Enabled() {
		if problems := validateModelList(c.OpenAIModel.Models); len(problems) > 0 {
			return &InvalidConfigError{Key: KeyModels, Reason: strings.Join(problems, "; ")}
		}
	}
	if err := c.ImageModel.validate(c.OpenAIModel); err != nil {
		return err
	}
	if c.Operator.Enabled() && len(c.Operator.Token.Reveal()) < MinOperatorTokenBytes {
		return &InvalidConfigError{Key: KeyOperatorToken, Reason: fmt.Sprintf("must be at least %d bytes", MinOperatorTokenBytes)}
	}
	if c.Operator.Name == "" {
		return &InvalidConfigError{Key: KeyOperatorName, Reason: "must not be empty"}
	}
	if err := validateProviderCapacity(c.ProviderCapacity); err != nil {
		return err
	}
	if err := validateWorkspaceRepositories(c.WorkspaceRepositories); err != nil {
		return err
	}
	return c.TaskGateway.validate(c.OpenAIModel, c.ImageModel)
}

// loader accumulates every problem in one pass instead of failing on the first key.
type loader struct {
	env     func(string) string
	removed []RemovedKey
	missing []string
	invalid error
}

// err reports removed keys first: a deployment still using one is configured for an older
// release, and the remaining errors would only describe the consequences of that.
func (l *loader) err() error {
	if len(l.removed) > 0 {
		return &RemovedConfigError{Keys: l.removed}
	}
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
	keys := []string{KeyModelBaseURL, KeyModelAPIKey, KeyModels}
	enabled := false
	for _, key := range keys {
		if strings.TrimSpace(l.env(key)) != "" {
			enabled = true
		}
	}
	timeout := l.optionalDuration(KeyModelTimeout)
	requireParameters := l.optionalBool(KeyModelOpenRouterRequireParameters)
	if !enabled {
		if timeout > 0 {
			l.fail(KeyModelTimeout, "set only together with "+KeyModelBaseURL)
		}
		// A switch turned on for a group that is not configured is a misconfiguration,
		// not a no-op; an explicit false changes nothing and is accepted.
		if requireParameters {
			l.fail(KeyModelOpenRouterRequireParameters, "set only together with "+KeyModels)
		}
		return OpenAIModel{}
	}
	return OpenAIModel{
		BaseURL:                     l.requiredProviderURL(KeyModelBaseURL),
		APIKey:                      Secret{value: l.required(KeyModelAPIKey)},
		Models:                      l.modelList(KeyModels),
		Timeout:                     timeout,
		OpenRouterRequireParameters: requireParameters,
	}
}

// optionalBool reads a boolean that defaults to false. It accepts what strconv.ParseBool
// accepts; the parse error is not passed on because its text quotes the value.
func (l *loader) optionalBool(key string) bool {
	value := strings.TrimSpace(l.env(key))
	if value == "" {
		return false
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		l.fail(key, "must be a boolean such as true or false")
		return false
	}
	return parsed
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

// durationOr reads an optional duration whose absence means fallback. An invalid value is
// recorded by optionalDuration and fails the load, so its fallback is never used.
func (l *loader) durationOr(key string, fallback time.Duration) time.Duration {
	if strings.TrimSpace(l.env(key)) == "" {
		return fallback
	}
	return l.optionalDuration(key)
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

func (l *loader) optionalInt(key string, fallback int) int {
	value := strings.TrimSpace(l.env(key))
	if value == "" {
		return fallback
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
