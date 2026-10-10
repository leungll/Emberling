package domain

import (
	"errors"
	"fmt"
	"time"
)

// artifactIDDigestChars is how much of the content digest an Execution Artifact id
// carries. 32 hex characters (128 bits) keep ids short while making two different
// contents sharing an id practically impossible; the full digest is still stored and
// compared, so a collision would surface as a conflict rather than silent reuse.
const artifactIDDigestChars = 32

// ErrArtifactContentMismatch rejects an Execution Artifact whose identity, digest, size
// or media type disagree with each other or with content already committed under the
// same id.
var ErrArtifactContentMismatch = errors.New("emberling: artifact does not match its content")

// Artifact is the Emberling-owned Metadata of one immutable binary an Execution produced,
// such as a generated image. Like Asset, the binary lives in content storage and the
// storage key is deliberately absent from this value: it is a system Secret that stays
// between the Store row and the storage layer.
type Artifact struct {
	ArtifactID string
	MediaType  string
	SizeBytes  int64
	SHA256     string
	CreatedAt  time.Time
}

// ArtifactIDForSHA256 derives the content-addressed Execution Artifact id from a
// lowercase hex SHA-256 digest. Identical bytes always yield the same id, which is what
// lets two Runs producing the same content share one stored object and one row.
func ArtifactIDForSHA256(sha256 string) (string, error) {
	if !isSHA256Hex(sha256) {
		return "", errors.New("domain: artifact sha256 must be 64 lowercase hex characters")
	}
	return IDPrefixArtifact + "_" + sha256[:artifactIDDigestChars], nil
}

// Ref returns the immutable reference results carry instead of binary content.
func (a Artifact) Ref() ArtifactRef {
	return ArtifactRef{
		ArtifactID: a.ArtifactID,
		MediaType:  a.MediaType,
		SizeBytes:  a.SizeBytes,
		SHA256:     a.SHA256,
	}
}

// Validate reports whether this Metadata may be committed.
func (a Artifact) Validate() error {
	if err := a.Ref().Validate(); err != nil {
		return err
	}
	if a.CreatedAt.IsZero() {
		return fmt.Errorf("domain: artifact %s: createdAt is required", a.ArtifactID)
	}
	return nil
}

// Validate reports whether an ArtifactRef is internally consistent: a supported image
// media type, a positive size, a well-formed digest, and an id derived from that digest.
// A ref that fails here never becomes an execution_artifacts row.
func (r ArtifactRef) Validate() error {
	if !IsSupportedAssetMediaType(r.MediaType) {
		return fmt.Errorf("domain: artifact %s: media type %q: %w", r.ArtifactID, r.MediaType, ErrUnsupportedAssetMediaType)
	}
	if r.SizeBytes <= 0 {
		return fmt.Errorf("domain: artifact %s: content is empty: %w", r.ArtifactID, ErrArtifactContentMismatch)
	}
	want, err := ArtifactIDForSHA256(r.SHA256)
	if err != nil {
		return fmt.Errorf("domain: artifact %s: %w", r.ArtifactID, err)
	}
	if r.ArtifactID != want {
		return fmt.Errorf("domain: artifact %s: id is not derived from its sha256: %w", r.ArtifactID, ErrArtifactContentMismatch)
	}
	return nil
}

// ErrArtifactTooLarge rejects produced content above the configured artifact limit. It
// is reported while streaming, before any content is committed to a storage key.
var ErrArtifactTooLarge = errors.New("emberling: artifact content exceeds the size limit")
