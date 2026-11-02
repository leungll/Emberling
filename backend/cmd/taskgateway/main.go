// Command taskgateway serves the task gateway (internal/fixture/taskgateway) as a
// standalone demo process: it accepts image generation tasks over the Mock Provider's
// task protocol and serves each through one real image model behind the OpenRouter Images
// API. All task behaviour lives in that package; this command reads the configuration,
// builds one Images API Adapter per gateway model, bridges it to the gateway's upstream
// port and owns the graceful-shutdown sequence.
//
// The upstream API key exists only in this process's environment and travels only in the
// Images API request's Authorization header. Configuration errors name keys, never values.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/openrouterimage"
	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/fixture/taskgateway"
)

// Process-only keys; the shared keys come from internal/config.
const (
	keyAddr            = "TASKGATEWAY_ADDR"
	keyWorkers         = "TASKGATEWAY_WORKERS"
	keyQueueSize       = "TASKGATEWAY_QUEUE_SIZE"
	keyMaxTaskDuration = "TASKGATEWAY_MAX_TASK_DURATION"
	keyMaxImageBytes   = "TASKGATEWAY_MAX_IMAGE_BYTES"
	keyImageStoreBytes = "TASKGATEWAY_IMAGE_STORE_BYTES"
	keyBaseURL         = config.EnvPrefix + "BASE_URL"
	keyPublicURL       = config.EnvPrefix + "TASK_GATEWAY_PUBLIC_URL"
)

// Defaults for the optional keys.
const (
	defaultAddr            = ":9104"
	defaultWorkers         = 4
	defaultQueueSize       = 64
	defaultMaxTaskDuration = 15 * time.Minute
	defaultMaxImageBytes   = 16 << 20
	defaultImageStoreBytes = 256 << 20
)

// shutdownTimeout bounds how long main waits for in-flight requests and callback
// deliveries once a shutdown signal arrives.
const shutdownTimeout = 10 * time.Second

// readHeaderTimeout bounds how long a client may take to send request headers.
const readHeaderTimeout = 10 * time.Second

// referenceTimeout bounds one reference image download.
const referenceTimeout = time.Minute

// gatewayConfig is the process configuration. apiKey is never logged or formatted.
type gatewayConfig struct {
	addr            string
	imageBaseURL    string
	apiKey          string
	imageTimeout    time.Duration
	models          []config.OpenAIModelEntry
	assetBaseURL    string
	publicURL       string
	workers         int
	queueSize       int
	maxTaskDuration time.Duration
	maxImageBytes   int64
	imageStoreBytes int64
}

// configError lists every missing and invalid key at once, by name only.
type configError struct {
	missing []string
	invalid []string
}

func (e *configError) Error() string {
	var parts []string
	if len(e.missing) > 0 {
		parts = append(parts, "missing required environment variables: "+strings.Join(e.missing, ", "))
	}
	if len(e.invalid) > 0 {
		parts = append(parts, "invalid environment variables: "+strings.Join(e.invalid, ", "))
	}
	return "taskgateway: " + strings.Join(parts, "; ")
}

// loadConfig reads the configuration through getenv. A missing required key or an
// unusable value refuses startup.
func loadConfig(getenv func(string) string) (gatewayConfig, error) {
	problems := &configError{}
	value := func(key string) string { return strings.TrimSpace(getenv(key)) }
	required := func(key string) string {
		v := value(key)
		if v == "" {
			problems.missing = append(problems.missing, key)
		}
		return v
	}
	requiredURL := func(key string) string {
		v := required(key)
		if v == "" {
			return ""
		}
		parsed, err := url.Parse(v)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			problems.invalid = append(problems.invalid, key)
			return ""
		}
		return strings.TrimRight(v, "/")
	}
	positiveInt := func(key string, fallback int64) int64 {
		v := value(key)
		if v == "" {
			return fallback
		}
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil || parsed <= 0 {
			problems.invalid = append(problems.invalid, key)
			return 0
		}
		return parsed
	}
	duration := func(key string, fallback time.Duration) time.Duration {
		v := value(key)
		if v == "" {
			return fallback
		}
		parsed, err := time.ParseDuration(v)
		if err != nil || parsed <= 0 {
			problems.invalid = append(problems.invalid, key)
			return 0
		}
		return parsed
	}

	cfg := gatewayConfig{
		addr:            value(keyAddr),
		imageBaseURL:    requiredURL(config.KeyImageBaseURL),
		apiKey:          required(config.KeyImageAPIKey),
		imageTimeout:    duration(config.KeyImageTimeout, config.DefaultImageTimeout),
		assetBaseURL:    requiredURL(keyBaseURL),
		publicURL:       requiredURL(keyPublicURL),
		workers:         int(positiveInt(keyWorkers, defaultWorkers)),
		queueSize:       int(positiveInt(keyQueueSize, defaultQueueSize)),
		maxTaskDuration: duration(keyMaxTaskDuration, defaultMaxTaskDuration),
		maxImageBytes:   positiveInt(keyMaxImageBytes, defaultMaxImageBytes),
		imageStoreBytes: positiveInt(keyImageStoreBytes, defaultImageStoreBytes),
	}
	if cfg.addr == "" {
		cfg.addr = defaultAddr
	}
	if models := required(config.KeyTaskGatewayModels); models != "" {
		entries, err := config.ParseTaskGatewayModels(models)
		if err != nil {
			problems.invalid = append(problems.invalid, config.KeyTaskGatewayModels)
		}
		cfg.models = entries
	}
	if len(problems.missing) > 0 || len(problems.invalid) > 0 {
		return gatewayConfig{}, problems
	}
	return cfg, nil
}

// imageGenerator bridges the gateway's upstream port to one Images API Adapter: one
// GenerateImage call per task, with the decoded image read into memory, which the
// Adapter already bounds.
type imageGenerator struct {
	provider *openrouterimage.Provider
}

// upstreamCallFailed is the label of a failure that is not an Adapter error.
const upstreamCallFailed = "upstream image call failed"

func (g imageGenerator) Generate(ctx context.Context, req taskgateway.GenerateRequest) (taskgateway.GeneratedImage, error) {
	image, err := g.provider.GenerateImage(ctx, openrouterimage.ImageRequest{
		Prompt:             req.Prompt,
		ReferenceImage:     req.ReferenceImage,
		ReferenceMediaType: req.ReferenceMediaType,
	})
	if err != nil {
		return taskgateway.GeneratedImage{}, upstreamFailure(err)
	}
	content, err := io.ReadAll(image.Content)
	if err != nil {
		return taskgateway.GeneratedImage{}, upstreamFailure(err)
	}
	return taskgateway.GeneratedImage{MediaType: image.MediaType, Content: content, Cost: image.Cost}, nil
}

// upstreamFailure keeps only the Adapter's fixed label, which never carries the key, the
// prompt or the upstream body, and drops any wrapped cause.
func upstreamFailure(err error) error {
	var adapterErr *openrouterimage.Error
	if errors.As(err, &adapterErr) {
		return errors.New(adapterErr.Error())
	}
	return errors.New(upstreamCallFailed)
}

// newGateway builds one upstream Adapter per gateway model and the gateway Server.
func newGateway(cfg gatewayConfig, logger *slog.Logger) (*taskgateway.Server, error) {
	imageClient := &http.Client{Timeout: cfg.imageTimeout}
	models := make(map[string]taskgateway.ImageGenerator, len(cfg.models))
	for i, entry := range cfg.models {
		provider, err := openrouterimage.New(openrouterimage.Config{
			ModelID:       entry.ID,
			DisplayName:   entry.DisplayName,
			Description:   entry.Description,
			BaseURL:       cfg.imageBaseURL,
			APIKey:        cfg.apiKey,
			Model:         entry.Model,
			HTTPClient:    imageClient,
			MaxImageBytes: cfg.maxImageBytes,
		})
		if err != nil {
			return nil, fmt.Errorf("taskgateway: configure model entry %d: %w", i, err)
		}
		models[entry.ID] = imageGenerator{provider: provider}
	}
	return taskgateway.NewServer(taskgateway.Config{
		Models:          models,
		AssetBaseURL:    cfg.assetBaseURL,
		PublicBaseURL:   cfg.publicURL,
		Workers:         cfg.workers,
		QueueSize:       cfg.queueSize,
		MaxTaskDuration: cfg.maxTaskDuration,
		MaxImageBytes:   cfg.maxImageBytes,
		ImageStoreBytes: cfg.imageStoreBytes,
		ReferenceClient: &http.Client{Timeout: referenceTimeout},
		CallbackClient:  &http.Client{},
		Logger:          logger,
	})
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		logger.Error("taskgateway: invalid configuration", slog.String("error", err.Error()))
		os.Exit(1)
	}
	server, err := newGateway(cfg, logger)
	if err != nil {
		logger.Error("taskgateway: startup failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	httpServer := &http.Server{Addr: cfg.addr, Handler: server, ReadHeaderTimeout: readHeaderTimeout}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("taskgateway: listening", slog.String("addr", cfg.addr), slog.Int("models", len(cfg.models)))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("taskgateway: shutdown signal received")
	case err := <-serveErr:
		if err != nil {
			logger.Error("taskgateway: listen error", slog.String("error", err.Error()))
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	// No new task arrives once the listener is closed; then the workers stop and the
	// callback deliveries on the wire get the rest of the shutdown window.
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("taskgateway: http shutdown error", slog.String("error", err.Error()))
	}
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("taskgateway: gateway shutdown error", slog.String("error", err.Error()))
	}
}
