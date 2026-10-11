//go:build integration

// Package testdb provisions a real PostgreSQL database for integration tests.
//
// A mock repository cannot prove a transaction, lock or constraint guarantee, so these
// tests never fall back to an in-memory store and never skip. When no database and no
// container runtime are available the test fails with instructions for both options.
//
// Isolation comes from the per-test database, not from the server: every Open call
// creates its own database and drops it when the test finishes. The server is therefore
// shared. When EMBERLING_TEST_DATABASE_URL is unset, a test binary starts at most one
// PostgreSQL container, on the first Open call, and every later call in that process
// reuses it. The container belongs to the test binary rather than to any single test, so
// a package that opens databases this way must route TestMain through Main, which
// terminates the container after all of the package's tests have finished. A failed
// start is remembered, and every later Open call fails with the same instructions.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/leungll/Emberling/backend/internal/store/postgres"
)

// EnvDatabaseURL names the environment variable that points at an existing PostgreSQL
// server. The referenced database is only used to create and drop per-test databases;
// migrations never run against it.
const EnvDatabaseURL = "EMBERLING_TEST_DATABASE_URL"

const containerImage = "postgres:18"

// sharedServer is the process-wide PostgreSQL container used when EnvDatabaseURL is unset.
// once makes the lazy start safe for parallel tests; url and err record its single outcome.
var sharedServer struct {
	once      sync.Once
	container *tcpostgres.PostgresContainer
	url       string
	err       error
}

// mainRunning reports whether the test binary entered through Main, which owns the
// shared container's lifetime. It is set before any test runs and never changes again.
var mainRunning bool

// Main runs the package's tests and then terminates the shared PostgreSQL container if one
// was started. Call it from TestMain in every package that uses Open or OpenUnmigrated.
// Termination happens before os.Exit, which would otherwise skip it.
func Main(m *testing.M) {
	mainRunning = true
	code := m.Run()
	if err := terminateSharedServer(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "testdb: terminate container: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// Open returns a pool connected to a freshly created, migrated database that is dropped
// when the test finishes. Each call gets its own database, so tests never observe each
// other's rows.
func Open(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool := OpenUnmigrated(t)
	if err := postgres.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("testdb: migrate: %v", err)
	}
	return pool
}

// OpenUnmigrated returns a pool on a fresh, empty database so that migration behaviour
// itself can be tested.
func OpenUnmigrated(t *testing.T) *pgxpool.Pool {
	t.Helper()

	adminURL := os.Getenv(EnvDatabaseURL)
	if adminURL == "" {
		adminURL = sharedContainerURL(t)
	}

	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("testdb: connect to the server named by %s: %v", EnvDatabaseURL, err)
	}
	defer func() { _ = admin.Close(ctx) }()

	name := "emberling_test_" + randomSuffix(t)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("testdb: create database %s: %v", name, err)
	}
	t.Cleanup(func() { dropDatabase(t, adminURL, name) })

	testURL, err := replaceDatabase(adminURL, name)
	if err != nil {
		t.Fatalf("testdb: build url for database %s: %v", name, err)
	}

	pool, err := pgxpool.New(ctx, testURL)
	if err != nil {
		t.Fatalf("testdb: open pool on database %s: %v", name, err)
	}
	t.Cleanup(pool.Close)

	return pool
}

func dropDatabase(t *testing.T, adminURL, name string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Logf("testdb: reconnect to drop database %s: %v", name, err)
		return
	}
	defer func() { _ = conn.Close(ctx) }()

	stmt := "DROP DATABASE IF EXISTS " + pgx.Identifier{name}.Sanitize() + " WITH (FORCE)"
	if _, err := conn.Exec(ctx, stmt); err != nil {
		t.Logf("testdb: drop database %s: %v", name, err)
	}
}

// sharedContainerURL returns the admin URL of the process-wide container, starting it on
// the first call. Without Main nothing would terminate the container, so the call fails
// instead of leaking it.
func sharedContainerURL(t *testing.T) string {
	t.Helper()

	if !mainRunning {
		t.Fatalf("testdb: %s is unset and this package has no TestMain that calls "+
			"testdb.Main, so a shared container would never be terminated.\n"+
			"Add func TestMain(m *testing.M) { testdb.Main(m) } to the package, or set %s.",
			EnvDatabaseURL, EnvDatabaseURL)
	}

	sharedServer.once.Do(func() {
		sharedServer.container, sharedServer.url, sharedServer.err = startContainer()
	})
	if sharedServer.err != nil {
		t.Fatalf("testdb: no PostgreSQL available.\n"+
			"Set %s to an existing PostgreSQL 18 server, or make a container runtime "+
			"available so the %s module can start one.\ncontainer start error: %v",
			EnvDatabaseURL, containerImage, sharedServer.err)
	}
	return sharedServer.url
}

// startContainer starts the PostgreSQL container and returns its admin URL. A container
// that started but cannot be used is terminated here, so a failure leaves nothing behind.
func startContainer() (*tcpostgres.PostgresContainer, string, error) {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, containerImage,
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("emberling"),
		tcpostgres.WithPassword("emberling"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		if container != nil {
			_ = container.Terminate(context.Background())
		}
		return nil, "", err
	}

	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(context.Background())
		return nil, "", fmt.Errorf("container connection string: %w", err)
	}
	return container, url, nil
}

// terminateSharedServer stops the shared container if this process started one. It runs
// only after every test has finished, so no per-test database is still in use.
func terminateSharedServer() error {
	if sharedServer.container == nil {
		return nil
	}
	return sharedServer.container.Terminate(context.Background())
}

func randomSuffix(t *testing.T) string {
	t.Helper()

	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("testdb: random suffix: %v", err)
	}
	return hex.EncodeToString(buf)
}

// replaceDatabase rewrites the database path of a PostgreSQL URL, preserving host,
// credentials and TLS mode.
func replaceDatabase(rawURL, database string) (string, error) {
	cfg, err := pgx.ParseConfig(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse database url: %w", err)
	}

	credentials := cfg.User
	if cfg.Password != "" {
		credentials += ":" + cfg.Password
	}
	sslMode := "disable"
	if cfg.TLSConfig != nil {
		sslMode = "require"
	}
	return fmt.Sprintf("postgres://%s@%s:%d/%s?sslmode=%s",
		credentials, cfg.Host, cfg.Port, database, sslMode), nil
}
