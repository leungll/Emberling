//go:build integration

// Package testdb provisions a real PostgreSQL database for integration tests.
//
// A mock repository cannot prove a transaction, lock or constraint guarantee, so these
// tests never fall back to an in-memory store and never skip. When no database and no
// container runtime are available the test fails with instructions for both options.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
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
		adminURL = startContainer(t)
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

func startContainer(t *testing.T) string {
	t.Helper()

	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, containerImage,
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("emberling"),
		tcpostgres.WithPassword("emberling"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("testdb: no PostgreSQL available.\n"+
			"Set %s to an existing PostgreSQL 18 server, or make a container runtime "+
			"available so the %s module can start one.\ncontainer start error: %v",
			EnvDatabaseURL, containerImage, err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Logf("testdb: terminate container: %v", err)
		}
	})

	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("testdb: container connection string: %v", err)
	}
	return url
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
