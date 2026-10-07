package domain

import (
	"errors"
	"fmt"
	"time"
)

// Asset is the Emberling-owned Metadata of one uploaded image. It is the half of an
// Asset PostgreSQL owns; the binary lives in Asset storage.
//
// The internal storage key is deliberately absent: it is a system Secret that must never
// reach an Event, Trace, API response or log, so it stays between the Store
// row and the storage layer and is never carried by a domain value.
type Asset struct {
	AssetID   string
	MediaType string
	SizeBytes int64
	SHA256    string
	CreatedAt time.Time
}

// Asset errors. Not-found reuses ErrNotFound, so every layer keeps one not-found test.
var (
	// ErrUnsupportedAssetMediaType rejects a media type outside the MVP image set.
	// General file upload is Phase 2.
	ErrUnsupportedAssetMediaType = errors.New("emberling: unsupported asset media type")

	// ErrAssetTooLarge rejects content above the configured upload limit. It is reported
	// while streaming, before any content is committed to a storage key.
	ErrAssetTooLarge = errors.New("emberling: asset content exceeds the upload limit")

	// ErrAssetContentMismatch rejects content that did not arrive as declared: an empty
	// upload, or a byte count that disagrees with the size the caller announced. An
	// Asset whose Metadata does not describe its binary is never committed.
	ErrAssetContentMismatch = errors.New("emberling: asset content does not match its declared size")
)

// supportedAssetMediaTypes is the MVP image set. It is a closed set on purpose: Run input
// only carries images, and a generic file type would widen the product boundary.
var supportedAssetMediaTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
}

// IsSupportedAssetMediaType reports whether an exact media type may be uploaded. The
// comparison is exact: the caller normalises parameters and casing before asking.
func IsSupportedAssetMediaType(mediaType string) bool {
	return supportedAssetMediaTypes[mediaType]
}

// Ref returns the immutable reference Run input keeps instead of binary content. It
// carries no storage location: content is reached through the Asset API, never through a
// path stored in a Run.
func (a Asset) Ref() AssetRef {
	return AssetRef{
		AssetID:   a.AssetID,
		MediaType: a.MediaType,
		SizeBytes: a.SizeBytes,
		SHA256:    a.SHA256,
	}
}

// Validate reports whether this Metadata may be committed. It is the last gate before an
// Asset row becomes a fact other Runs can reference.
func (a Asset) Validate() error {
	if a.AssetID == "" {
		return errors.New("domain: asset id is required")
	}
	if !IsSupportedAssetMediaType(a.MediaType) {
		return fmt.Errorf("domain: asset %s: media type %q: %w", a.AssetID, a.MediaType, ErrUnsupportedAssetMediaType)
	}
	if a.SizeBytes <= 0 {
		return fmt.Errorf("domain: asset %s: content is empty: %w", a.AssetID, ErrAssetContentMismatch)
	}
	if !isSHA256Hex(a.SHA256) {
		return fmt.Errorf("domain: asset %s: sha256 must be 64 lowercase hex characters", a.AssetID)
	}
	if a.CreatedAt.IsZero() {
		return fmt.Errorf("domain: asset %s: createdAt is required", a.AssetID)
	}
	return nil
}

// isSHA256Hex reports whether s is a lowercase hex SHA-256 digest. Lowercase is required
// so that an ETag or a comparison against a recomputed digest is a plain string equality.
func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}
