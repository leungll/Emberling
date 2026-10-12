package service

import (
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

func declaredArtifact(t *testing.T, sha string) domain.ArtifactRef {
	t.Helper()
	id, err := domain.ArtifactIDForSHA256(sha)
	if err != nil {
		t.Fatalf("ArtifactIDForSHA256: %v", err)
	}
	return domain.ArtifactRef{ArtifactID: id, MediaType: "image/png", SizeBytes: 10, SHA256: sha}
}

func TestValidateDeclaredArtifacts_DistinctValid_Accepted(t *testing.T) {
	artifacts := []domain.ArtifactRef{
		declaredArtifact(t, strings.Repeat("a1", 32)),
		declaredArtifact(t, strings.Repeat("b2", 32)),
	}
	if err := validateDeclaredArtifacts(artifacts, "generate_image"); err != nil {
		t.Fatalf("validateDeclaredArtifacts = %v, want nil", err)
	}
}

func TestValidateDeclaredArtifacts_SameArtifactTwice_Rejected(t *testing.T) {
	ref := declaredArtifact(t, strings.Repeat("c3", 32))
	if err := validateDeclaredArtifacts([]domain.ArtifactRef{ref, ref}, "generate_image"); err == nil {
		t.Fatal("validateDeclaredArtifacts = nil, want an error for a repeated declaration")
	}
}
