package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
)

const artifactTestSHA = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func validArtifactRef(t *testing.T) domain.ArtifactRef {
	t.Helper()
	id, err := domain.ArtifactIDForSHA256(artifactTestSHA)
	if err != nil {
		t.Fatalf("ArtifactIDForSHA256: %v", err)
	}
	return domain.ArtifactRef{ArtifactID: id, MediaType: "image/png", SizeBytes: 4, SHA256: artifactTestSHA}
}

func TestArtifactIDForSHA256_ValidDigest_UsesPrefixAndFirst32Hex(t *testing.T) {
	id, err := domain.ArtifactIDForSHA256(artifactTestSHA)
	if err != nil {
		t.Fatalf("ArtifactIDForSHA256: %v", err)
	}
	if want := "artifact_" + artifactTestSHA[:32]; id != want {
		t.Fatalf("id = %q, want %q", id, want)
	}
}

func TestArtifactIDForSHA256_MalformedDigest_IsRejected(t *testing.T) {
	for _, sha := range []string{"", "abc", strings.ToUpper(artifactTestSHA), artifactTestSHA[:63] + "g"} {
		if _, err := domain.ArtifactIDForSHA256(sha); err == nil {
			t.Fatalf("ArtifactIDForSHA256(%q) succeeded, want error", sha)
		}
	}
}

func TestArtifactRefValidate_ConsistentRef_Succeeds(t *testing.T) {
	if err := validArtifactRef(t).Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestArtifactRefValidate_InconsistentRef_IsRejected(t *testing.T) {
	cases := map[string]func(*domain.ArtifactRef){
		"id not derived from digest": func(r *domain.ArtifactRef) { r.ArtifactID = "artifact_" + strings.Repeat("0", 32) },
		"unsupported media type":     func(r *domain.ArtifactRef) { r.MediaType = "text/plain" },
		"empty content":              func(r *domain.ArtifactRef) { r.SizeBytes = 0 },
		"malformed digest":           func(r *domain.ArtifactRef) { r.SHA256 = "nothex" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			ref := validArtifactRef(t)
			mutate(&ref)
			if err := ref.Validate(); err == nil {
				t.Fatal("Validate succeeded, want error")
			}
		})
	}
}

func TestArtifactValidate_MissingCreatedAt_IsRejected(t *testing.T) {
	ref := validArtifactRef(t)
	artifact := domain.Artifact{ArtifactID: ref.ArtifactID, MediaType: ref.MediaType, SizeBytes: ref.SizeBytes, SHA256: ref.SHA256}
	if err := artifact.Validate(); err == nil {
		t.Fatal("Validate succeeded without createdAt")
	}
	artifact.CreatedAt = time.Unix(1, 0)
	if err := artifact.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if artifact.Ref() != ref {
		t.Fatalf("Ref() = %+v, want %+v", artifact.Ref(), ref)
	}
}

func TestArtifactRefValidate_IDMismatch_IsContentMismatch(t *testing.T) {
	ref := validArtifactRef(t)
	ref.ArtifactID = "artifact_" + strings.Repeat("1", 32)
	if err := ref.Validate(); !errors.Is(err, domain.ErrArtifactContentMismatch) {
		t.Fatalf("Validate error = %v, want ErrArtifactContentMismatch", err)
	}
}
