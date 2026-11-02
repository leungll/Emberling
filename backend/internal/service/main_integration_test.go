//go:build integration

package service

import (
	"testing"

	"github.com/leungll/Emberling/backend/test/testdb"
)

// TestMain routes the package's integration tests through testdb.Main, which owns the
// lifetime of the shared PostgreSQL container used when no database URL is configured.
func TestMain(m *testing.M) {
	testdb.Main(m)
}
