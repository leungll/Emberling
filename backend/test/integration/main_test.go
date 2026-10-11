//go:build integration

package integration

import (
	"testing"

	"github.com/leungll/Emberling/backend/test/testdb"
)

// TestMain routes the package through testdb.Main, which owns the lifetime of the shared
// PostgreSQL container the package's tests use when no database URL is configured.
func TestMain(m *testing.M) {
	testdb.Main(m)
}
