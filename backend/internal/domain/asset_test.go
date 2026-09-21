package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
)

func validAsset() domain.Asset {
	return domain.Asset{
		AssetID:   "asset_1",
		MediaType: "image/png",
		SizeBytes: 12,
		SHA256:    strings.Repeat("ab", 32),
		CreatedAt: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC),
	}
}

func TestAsset_Ref_CarriesTheFourPublicFields(t *testing.T) {
	a := validAsset()

	ref := a.Ref()

	want := domain.AssetRef{
		AssetID:   a.AssetID,
		MediaType: a.MediaType,
		SizeBytes: a.SizeBytes,
		SHA256:    a.SHA256,
	}
	if ref != want {
		t.Errorf("Ref(): got %+v, want %+v", ref, want)
	}
}

func TestAssetMediaType_UnsupportedType_IsRejected(t *testing.T) {
	for _, mediaType := range []string{"image/png", "image/jpeg", "image/webp"} {
		if !domain.IsSupportedAssetMediaType(mediaType) {
			t.Errorf("IsSupportedAssetMediaType(%q): want true", mediaType)
		}
	}
	for _, mediaType := range []string{"", "image/gif", "application/pdf", "text/plain", "IMAGE/PNG "} {
		if domain.IsSupportedAssetMediaType(mediaType) {
			t.Errorf("IsSupportedAssetMediaType(%q): want false", mediaType)
		}
	}
}

func TestAssetValidate_UnsupportedMediaType_ReturnsUnsupportedMediaTypeError(t *testing.T) {
	a := validAsset()
	a.MediaType = "application/pdf"

	err := a.Validate()

	if !errors.Is(err, domain.ErrUnsupportedAssetMediaType) {
		t.Fatalf("Validate(): want ErrUnsupportedAssetMediaType, got %v", err)
	}
}

func TestAssetValidate_BadSHA256_IsRejected(t *testing.T) {
	for name, sha := range map[string]string{
		"empty":     "",
		"short":     "abcd",
		"uppercase": strings.Repeat("AB", 32),
		"non-hex":   strings.Repeat("zz", 32),
	} {
		a := validAsset()
		a.SHA256 = sha
		if err := a.Validate(); err == nil {
			t.Errorf("Validate() with %s sha256: want error, got nil", name)
		}
	}
}

func TestAssetValidate_EmptyContent_IsRejected(t *testing.T) {
	a := validAsset()
	a.SizeBytes = 0

	err := a.Validate()

	if !errors.Is(err, domain.ErrAssetContentMismatch) {
		t.Fatalf("Validate() with zero size: want ErrAssetContentMismatch, got %v", err)
	}
}

func TestAssetValidate_CompleteAsset_Passes(t *testing.T) {
	if err := validAsset().Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
}
