package store

import (
	"strings"

	"github.com/google/uuid"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// UUIDGenerator produces domain-prefixed identifiers backed by UUIDv4. It lives outside
// domain so that domain keeps importing the standard library only.
type UUIDGenerator struct{}

// NewUUIDGenerator returns the production ID generator.
func NewUUIDGenerator() UUIDGenerator { return UUIDGenerator{} }

// NewID returns "<prefix>_<uuid>" with the UUID hyphens removed. Nothing routes on this
// shape; the prefix only makes an identifier readable in isolation.
func (UUIDGenerator) NewID(prefix string) string {
	raw := strings.ReplaceAll(uuid.NewString(), "-", "")
	if prefix == "" {
		return raw
	}
	return prefix + "_" + raw
}

var _ domain.IDGenerator = UUIDGenerator{}
