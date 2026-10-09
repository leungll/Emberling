// Command emberling runs the Backend process: API, Runtime and Reconciler live in one
// process for the MVP. This entry point owns configuration loading, the
// startup gate and shutdown; it is the only place allowed to depend on every layer.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/adapters/mocktask"
	"github.com/leungll/Emberling/backend/internal/api"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/config/readiness"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/agent"
	"github.com/leungll/Emberling/backend/internal/nodes/imagegeneration"
	"github.com/leungll/Emberling/backend/internal/nodes/imageinput"
	"github.com/leungll/Emberling/backend/internal/nodes/mediaoutput"
	"github.com/leungll/Emberling/backend/internal/nodes/prompttemplate"
	"github.com/leungll/Emberling/backend/internal/nodes/textgeneration"
	"github.com/leungll/Emberling/backend/internal/nodes/textinput"
	"github.com/leungll/Emberling/backend/internal/nodes/textoutput"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
	"github.com/leungll/Emberling/backend/internal/tools/remotelookup"
	"github.com/leungll/Emberling/backend/internal/work"
)

// defaultWorkerCount bounds the in-process work.Pool. No config key exists for this
// (internal/config.Config has no worker-pool-size field): this is an MVP default, same
// category as work.defaultQueueCapacity and reconciler.defaultBatchLimit.
const defaultWorkerCount = 4

// defaultShutdownTimeout bounds how long the HTTP server waits for in-flight requests to
// finish once shutdown begins, before the process moves on to cancelling the Reconciler
// and stopping the work Pool.
const defaultShutdownTimeout = 10 * time.Second

// defaultTaskDispatchTimeout bounds the mocktask Adapter's HTTP client: the single external
// call an async Node's Execute makes before returning NodeResultDispatched, never
// the callback delivery itself.
const defaultTaskDispatchTimeout = 10 * time.Second

// readHeaderTimeout bounds how long a client may take to send request headers. It does
// not limit a request body or a long-lived SSE response.
const readHeaderTimeout = 10 * time.Second

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		// Errors carry the operation and the failing configuration key, never a value.
		logger.Error("backend exited", slog.String("error", err.Error()))
		os.Exit(1)
	}
	logger.Info("backend exited cleanly")
}

func run(logger *slog.Logger) error {
	// Signals are trapped before any resource is acquired, so a stop request during
	// startup interrupts the gate instead of being observed after it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	pool, err := openPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	clock := domain.SystemClock{}

	uow := postgres.NewUnitOfWork(pool)
	nodeRegistry := registry.NewNodeRegistry()
	modelRegistry := registry.NewModelRegistry()
	toolRegistry := registry.NewToolRegistry()
	compiler := runtime.NewCompiler(nodeRegistry, clock)

	queue := work.NewQueue(0)
	notifier := api.NewHub()
	assetStore := asset.NewStore(cfg.AssetStorage.Root, int64(cfg.AssetStorage.MaxUploadBytes))

	serviceDeps := service.Deps{
		UoW:      uow,
		Nodes:    nodeRegistry,
		Models:   modelRegistry,
		Tools:    toolRegistry,
		Compiler: compiler,
		Clock:    clock,
		IDs:      store.NewUUIDGenerator(),
		Assets:   assetStore,
		Queue:    queue,
		Notifier: notifier,
		Logger:   logger,
		Callback: service.CallbackConfig{
			BaseURL:       cfg.Callback.BaseURL,
			SigningSecret: []byte(cfg.Callback.SigningSecret.Reveal()),
			PendingTTL:    cfg.PendingCallback.TTL,
		},
	}

	definitionService := service.NewDefinitionService(serviceDeps)
	catalogService := service.NewCatalogService(serviceDeps)
	queryService := service.NewQueryService(serviceDeps)
	executionService := service.NewExecutionService(serviceDeps)
	assetService := service.NewAssetService(serviceDeps)

	workPool := work.NewPool(queue, executionService, defaultWorkerCount, work.Hooks{}, logger)

	rec := reconciler.New(reconciler.Config{
		UoW:        uow,
		Executor:   executionService,
		Clock:      clock,
		Interval:   cfg.Reconciliation.Interval,
		BatchLimit: cfg.Reconciliation.BatchSize,
		Logger:     logger,
	})

	// The Reconciler and the work Pool each own a long-running goroutine; both are
	// bounded by reconcilerCtx so the shutdown sequence below can stop them explicitly,
	// independently of the process's own signal context (which is already done by the
	// time shutdown runs).
	reconcilerCtx, cancelReconciler := context.WithCancel(context.Background())
	defer cancelReconciler()

	// reconcilerDone closes when rec.Run's own goroutine returns. The shutdown sequence
	// waits on it after cancelling reconcilerCtx and before workPool.Stop()/pool.Close():
	// without this, a RunOnce pass already in flight when shutdown begins could still be
	// reading through uow after the pgxpool it depends on started closing.
	reconcilerDone := make(chan struct{})

	probe := readiness.NewProbe()
	checks := readiness.Checks{
		// Step 1: the database must be reachable and migrated before anything else.
		Database: func(ctx context.Context) error {
			if err := pool.Ping(ctx); err != nil {
				return fmt.Errorf("ping database: %w", err)
			}
			return postgres.Migrate(ctx, pool)
		},
		// Step 2: required configuration and Secrets are present and usable.
		Configuration: func(context.Context) error { return cfg.Validate() },
		// Step 3: every Node Type, Tool and the Mock Model Provider must register
		// cleanly. A Registry that fails this must not let the Backend become ready
		// (registry.ModelRegistry.Register's own doc comment).
		Registry: func(ctx context.Context) error {
			if err := modelRegistry.Register(ctx, mockmodel.NewProvider()); err != nil {
				return fmt.Errorf("register mock model provider: %w", err)
			}
			taskClient := &http.Client{Timeout: defaultTaskDispatchTimeout}
			// TODO: Move built-in node registration into a dedicated package or helper so adding
			// new node types does not require editing the process entry point.
			registrations := []registry.NodeRegistration{
				textinput.Registration(),
				imageinput.Registration(),
				prompttemplate.Registration(),
				textoutput.Registration(),
				mediaoutput.Registration(),
				textgeneration.Registration(modelRegistry),
				imagegeneration.Registration(modelRegistry, mocktask.New(cfg.ModelProvider.BaseURL, taskClient)),
				agent.Registration(),
			}
			for _, reg := range registrations {
				if err := nodeRegistry.Register(reg); err != nil {
					return fmt.Errorf("register node type: %w", err)
				}
			}
			if err := toolRegistry.Register(lookup.Registration()); err != nil {
				return fmt.Errorf("register tool: %w", err)
			}
			if err := toolRegistry.Register(remotelookup.Registration(cfg.ModelProvider.BaseURL, taskClient)); err != nil {
				return fmt.Errorf("register tool: %w", err)
			}
			// Fact declarations may name a producer Tool registered after its consumer, so
			// they are checked only once every registration is in.
			return registry.ValidateFactDeclarations(toolRegistry.ListMetadata(), nodeRegistry.ListMetadata())
		},
		// Step 4: the Asset storage volume must exist and accept the write, read and
		// delete an upload performs. A Backend that cannot do this must not become
		// ready, because an accepted upload would have nowhere to land.
		AssetStorage: assetStore.VerifyReadWrite,
		// Step 5: start the Reconciler's ticker loop. It must be running before the
		// Backend accepts requests, so a crash recovered only by the Reconciler cannot
		// be missed by a request that arrives before the first tick.
		Reconciler: func(ctx context.Context) error {
			go func() {
				defer close(reconcilerDone)
				rec.Run(reconcilerCtx)
			}()
			workPool.Start(reconcilerCtx)
			return nil
		},
		// Step 6: one full reconciliation pass must complete before the process accepts
		// requests, so recovery of work left behind by a previous process is known to be
		// underway, not merely scheduled.
		FirstScan: func(ctx context.Context) error {
			_, err := rec.RunOnce(ctx)
			return err
		},
	}

	if err := probe.Run(ctx, checks, &stepLogger{logger: logger}); err != nil {
		return err
	}

	router := api.NewRouter(api.Deps{
		Definitions:             definitionService,
		Catalog:                 catalogService,
		Query:                   queryService,
		Execution:               executionService,
		Assets:                  assetService,
		Readiness:               probe,
		Notifier:                notifier,
		Clock:                   clock,
		Logger:                  logger,
		CallbackMaxPayloadBytes: cfg.PendingCallback.MaxPayloadBytes,
		AssetMaxUploadBytes:     int64(cfg.AssetStorage.MaxUploadBytes),
	})

	httpServer := &http.Server{Addr: cfg.HTTP.Addr, Handler: router, ReadHeaderTimeout: readHeaderTimeout}
	serveErrs := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrs <- err
			return
		}
		serveErrs <- nil
	}()

	logger.Info("backend ready",
		slog.String("http_addr", cfg.HTTP.Addr),
		slog.Int("reconcile_batch_size", cfg.Reconciliation.BatchSize),
		slog.Duration("reconcile_interval", cfg.Reconciliation.Interval),
	)

	serverAlreadyStopped := false
	select {
	case <-ctx.Done():
	case err := <-serveErrs:
		serverAlreadyStopped = true
		if err != nil {
			logger.Error("http server failed", slog.String("error", err.Error()))
		}
	}

	// Shutdown order (fixed): stop accepting new
	// requests first, then cancel the Reconciler, then stop the work Pool, and only then
	// let the deferred pool.Close() tear down the database pool. Post-COMMIT work that
	// never ran is recovered by the Reconciler on the next process's own startup gate.
	probe.BeginShutdown()
	logger.Info("backend draining")

	if !serverAlreadyStopped {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), defaultShutdownTimeout)
		defer cancelShutdown()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("http server shutdown", slog.String("error", err.Error()))
		}
		<-serveErrs
	}

	cancelReconciler()
	<-reconcilerDone
	workPool.Stop()

	return nil
}

func openPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.Database.URL.Reveal())
	if err != nil {
		// The parse error can quote the connection string, which carries the password.
		return nil, errors.New("open database pool: " + config.KeyDatabaseURL + " is not a valid connection string")
	}
	poolCfg.MaxConns = int32(cfg.Database.MaxConns) //nolint:gosec // config validation bounds MaxConns to (0, math.MaxInt32]

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}
	return pool, nil
}

// stepLogger records startup gate progress. Unwired steps are logged explicitly so an
// operator can tell "passed" from "not part of this build".
type stepLogger struct {
	logger *slog.Logger
}

func (s *stepLogger) StepPassed(step readiness.Step, wired bool) {
	s.logger.Info("readiness step passed",
		slog.String("step", string(step)), slog.Bool("wired", wired))
}

func (s *stepLogger) StepFailed(step readiness.Step, err error) {
	s.logger.Error("readiness step failed",
		slog.String("step", string(step)), slog.String("error", err.Error()))
}
