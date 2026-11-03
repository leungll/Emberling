// Command sandboxrunner serves the sandbox runner (internal/fixture/sandboxrunner) as a standalone
// process, the form deploy/compose.yaml starts. All behaviour lives in that package; this
// command owns the configuration, the startup cleanup and redelivery, and the
// graceful-shutdown sequence.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/leungll/Emberling/backend/internal/fixture/mockcontrol"
	"github.com/leungll/Emberling/backend/internal/fixture/sandboxrunner"
)

// defaultAddr matches the port deploy/compose.yaml maps for the sandboxrunner service.
const defaultAddr = ":9103"

// Backend names SANDBOXRUNNER_BACKEND accepts.
const (
	backendMock   = "mock"
	backendDocker = "docker"
)

// shutdownTimeout bounds how long main waits for in-flight requests, tests and callbacks
// once a shutdown signal arrives; tests still running then are stopped.
const shutdownTimeout = 10 * time.Second

// readHeaderTimeout bounds how long a client may take to send request headers.
const readHeaderTimeout = 10 * time.Second

// startupTimeout bounds removing leftover sandbox containers at startup.
const startupTimeout = time.Minute

// config is the process configuration read from the environment.
type config struct {
	addr    string
	backend string
	// testControls enables the barrier, the request record and result redelivery. It is
	// off unless explicitly enabled.
	testControls bool
	// recordPath is the request record file; required exactly when testControls is on.
	recordPath  string
	image       string
	testTimeout time.Duration
	// verifyImage is the verification image the docker backend runs patch verifications
	// in; empty leaves the verification routes off under the docker backend. The mock
	// backend always serves them from the mock verification backend.
	verifyImage string
	// repositories is the raw `<id>=<absolute path>` list of repositories verifications
	// may export from; parsed by parseRepositories.
	repositories  string
	verifyTimeout time.Duration
}

// repositoryIDPattern is the shape of a repository identifier, the rule the Runtime applies
// to its registered workspace repositories.
var repositoryIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// parseRepositories parses SANDBOXRUNNER_REPOSITORIES: a comma-separated list of
// `<repository id>=<absolute path>` entries. The error names the entry, never its value.
func parseRepositories(value string) (map[string]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	repositories := make(map[string]string)
	for i, entry := range strings.Split(value, ",") {
		id, repoPath, found := strings.Cut(strings.TrimSpace(entry), "=")
		id, repoPath = strings.TrimSpace(id), strings.TrimSpace(repoPath)
		switch {
		case !found || id == "" || repoPath == "":
			return nil, fmt.Errorf("SANDBOXRUNNER_REPOSITORIES entry %d: must be <repository id>=<absolute path>", i)
		case !repositoryIDPattern.MatchString(id):
			return nil, fmt.Errorf("SANDBOXRUNNER_REPOSITORIES entry %d: repository id must match %s", i, repositoryIDPattern)
		case !filepath.IsAbs(repoPath):
			return nil, fmt.Errorf("SANDBOXRUNNER_REPOSITORIES entry %d: path must be absolute", i)
		}
		if _, duplicate := repositories[id]; duplicate {
			return nil, fmt.Errorf("SANDBOXRUNNER_REPOSITORIES entry %d: repository id repeats an earlier entry", i)
		}
		repositories[id] = filepath.Clean(repoPath)
	}
	return repositories, nil
}

// validImageReference reports whether image is one digest-pinned reference that cannot be
// read as a flag or split into several arguments.
func validImageReference(image string) bool {
	return strings.Contains(image, "@sha256:") && !strings.HasPrefix(image, "-") && !strings.ContainsFunc(image, unicode.IsSpace)
}

// loadConfig reads SANDBOXRUNNER_ADDR, SANDBOXRUNNER_BACKEND, SANDBOXRUNNER_TEST_CONTROLS,
// SANDBOXRUNNER_RECORD_PATH, SANDBOXRUNNER_IMAGE, SANDBOXRUNNER_TEST_TIMEOUT,
// SANDBOXRUNNER_VERIFY_IMAGE, SANDBOXRUNNER_REPOSITORIES and SANDBOXRUNNER_VERIFY_TIMEOUT
// through getenv. A record path without test controls is rejected rather than ignored, and
// an image not pinned by digest is rejected, so a tag move cannot change what runs. The
// verification image and the repositories belong to the docker backend and come together:
// one without the other is a misconfiguration, not a runner that silently verifies nothing.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		addr:          getenv("SANDBOXRUNNER_ADDR"),
		backend:       getenv("SANDBOXRUNNER_BACKEND"),
		recordPath:    getenv("SANDBOXRUNNER_RECORD_PATH"),
		image:         getenv("SANDBOXRUNNER_IMAGE"),
		testTimeout:   sandboxrunner.DefaultTestTimeout,
		verifyImage:   getenv("SANDBOXRUNNER_VERIFY_IMAGE"),
		repositories:  strings.TrimSpace(getenv("SANDBOXRUNNER_REPOSITORIES")),
		verifyTimeout: sandboxrunner.DefaultVerificationTimeout,
	}
	if cfg.addr == "" {
		cfg.addr = defaultAddr
	}
	if cfg.backend == "" {
		cfg.backend = backendMock
	}
	if cfg.backend != backendMock && cfg.backend != backendDocker {
		return config{}, fmt.Errorf("SANDBOXRUNNER_BACKEND must be %q or %q", backendMock, backendDocker)
	}
	if cfg.image == "" {
		cfg.image = sandboxrunner.DefaultImage
	}
	// An image is passed to docker create as one argument; a leading dash or whitespace
	// would let it be read as a flag or split, so neither is accepted.
	if !validImageReference(cfg.image) {
		return config{}, errors.New("SANDBOXRUNNER_IMAGE must be one image reference pinned by digest (name@sha256:...)")
	}
	if cfg.verifyImage != "" && !validImageReference(cfg.verifyImage) {
		return config{}, errors.New("SANDBOXRUNNER_VERIFY_IMAGE must be one image reference pinned by digest (name@sha256:...)")
	}
	if _, err := parseRepositories(cfg.repositories); err != nil {
		return config{}, err
	}
	switch {
	case cfg.verifyImage != "" && cfg.backend != backendDocker:
		return config{}, errors.New("SANDBOXRUNNER_VERIFY_IMAGE is set but SANDBOXRUNNER_BACKEND is not docker")
	case cfg.verifyImage != "" && cfg.repositories == "":
		return config{}, errors.New("SANDBOXRUNNER_REPOSITORIES is required when SANDBOXRUNNER_VERIFY_IMAGE is set")
	case cfg.verifyImage == "" && cfg.repositories != "":
		return config{}, errors.New("SANDBOXRUNNER_REPOSITORIES is set but SANDBOXRUNNER_VERIFY_IMAGE is not")
	}
	if raw := getenv("SANDBOXRUNNER_VERIFY_TIMEOUT"); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout <= 0 {
			return config{}, errors.New("SANDBOXRUNNER_VERIFY_TIMEOUT must be a positive duration such as 5m")
		}
		cfg.verifyTimeout = timeout
	}
	if raw := getenv("SANDBOXRUNNER_TEST_TIMEOUT"); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout <= 0 {
			return config{}, errors.New("SANDBOXRUNNER_TEST_TIMEOUT must be a positive duration such as 120s")
		}
		cfg.testTimeout = timeout
	}
	if raw := getenv("SANDBOXRUNNER_TEST_CONTROLS"); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return config{}, fmt.Errorf("SANDBOXRUNNER_TEST_CONTROLS must be a boolean: %w", err)
		}
		cfg.testControls = enabled
	}
	switch {
	case cfg.testControls && cfg.recordPath == "":
		return config{}, errors.New("SANDBOXRUNNER_RECORD_PATH is required when SANDBOXRUNNER_TEST_CONTROLS is enabled")
	case !cfg.testControls && cfg.recordPath != "":
		return config{}, errors.New("SANDBOXRUNNER_RECORD_PATH is set but SANDBOXRUNNER_TEST_CONTROLS is not enabled")
	}
	return cfg, nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("sandboxrunner: " + err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var backend sandboxrunner.Backend = sandboxrunner.NewMockBackend()
	var opts []sandboxrunner.Option
	if cfg.backend == backendMock {
		opts = append(opts, sandboxrunner.WithVerification(sandboxrunner.NewMockVerificationBackend()))
	}
	if cfg.backend == backendDocker {
		docker := sandboxrunner.NewDockerBackend(cfg.image, cfg.testTimeout, sandboxrunner.ExecRunner{})
		startupCtx, cancel := context.WithTimeout(ctx, startupTimeout)
		removed, err := docker.RemoveOrphans(startupCtx)
		cancel()
		if err != nil {
			return err
		}
		logger.Info("sandboxrunner: docker backend ready", slog.String("image", cfg.image), slog.Int("orphans_removed", removed))
		backend = docker
		if cfg.verifyImage != "" {
			repositories, err := parseRepositories(cfg.repositories)
			if err != nil {
				return err
			}
			verifier, err := sandboxrunner.NewDockerVerifier(sandboxrunner.NewLocalRepositories(repositories), cfg.verifyImage, cfg.verifyTimeout, sandboxrunner.ExecRunner{})
			if err != nil {
				return err
			}
			opts = append(opts, sandboxrunner.WithVerification(verifier))
			logger.Info("sandboxrunner: verification enabled", slog.String("verify_image", cfg.verifyImage), slog.Int("repositories", len(repositories)))
		}
	}

	var (
		record *mockcontrol.Record
		store  *sandboxrunner.RedeliveryStore
		stored []sandboxrunner.StoredResult
	)
	if cfg.testControls {
		record, err = mockcontrol.OpenRecord(cfg.recordPath)
		if err != nil {
			return fmt.Errorf("open request record: %w", err)
		}
		defer closeLogged(logger, "request record", record.Close)
		store, stored, err = sandboxrunner.OpenRedeliveryStore(sandboxrunner.RedeliveryPath(cfg.recordPath))
		if err != nil {
			return err
		}
		defer closeLogged(logger, "redelivery store", store.Close)
		opts = append(opts, sandboxrunner.WithTestControls(record, store))
		logger.Info("sandboxrunner: test controls enabled", slog.String("record_path", cfg.recordPath), slog.Int("results_to_redeliver", len(stored)))
	}

	server := sandboxrunner.NewServer(backend, opts...)
	server.Redeliver(stored)
	httpServer := &http.Server{Addr: cfg.addr, Handler: server, ReadHeaderTimeout: readHeaderTimeout}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("sandboxrunner: listening", slog.String("addr", cfg.addr), slog.String("backend", cfg.backend))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("sandboxrunner: shutdown signal received")
	case err := <-serveErr:
		if err != nil {
			logger.Error("sandboxrunner: listen error", slog.String("error", err.Error()))
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	// Held requests are rejected first: both shutdowns below wait for them otherwise.
	server.StopHolding()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("sandboxrunner: http shutdown error", slog.String("error", err.Error()))
	}
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("sandboxrunner: test shutdown error", slog.String("error", err.Error()))
	}
	return nil
}

func closeLogged(logger *slog.Logger, name string, closeFn func() error) {
	if err := closeFn(); err != nil {
		logger.Error("sandboxrunner: close "+name, slog.String("error", err.Error()))
	}
}
