package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func externalRef() ImageRef {
	return ImageRef{
		Source:    ImageSourceExternal,
		URI:       "https://provider.example/images/result.png",
		MediaType: "image/png",
		Width:     1024,
		Height:    1024,
	}
}

func assetImageRef() ImageRef {
	return ImageRef{
		Source: ImageSourceAsset,
		Asset:  &AssetRef{AssetID: "asset_123", MediaType: "image/png", SizeBytes: 102400, SHA256: "abc"},
	}
}

func artifactImageRef() ImageRef {
	return ImageRef{
		Source:   ImageSourceArtifact,
		Artifact: &ArtifactRef{ArtifactID: "artifact_123", MediaType: "image/png", SizeBytes: 204800, SHA256: "abc"},
	}
}

func TestImageRef_Validate_AcceptsEachBranch(t *testing.T) {
	withoutDimensions := externalRef()
	withoutDimensions.Width, withoutDimensions.Height = 0, 0

	for name, ref := range map[string]ImageRef{
		"asset":                    assetImageRef(),
		"artifact":                 artifactImageRef(),
		"external":                 externalRef(),
		"external no width/height": withoutDimensions,
	} {
		t.Run(name, func(t *testing.T) {
			if err := ref.Validate(); err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
		})
	}
}

func TestImageRef_Validate_RejectsInvalidSource(t *testing.T) {
	for name, source := range map[string]ImageSource{"empty": "", "unknown": "URL", "lowercase": "asset"} {
		t.Run(name, func(t *testing.T) {
			ref := externalRef()
			ref.Source = source
			if err := ref.Validate(); err == nil {
				t.Fatalf("Validate() error = nil, want an error for source %q", source)
			}
		})
	}
}

// TestImageRef_Validate_RejectsMixedBranches pins 08 §2.2's "三个分支互斥": a member that
// belongs to another branch may not travel with the declared one.
func TestImageRef_Validate_RejectsMixedBranches(t *testing.T) {
	assetWithURI := assetImageRef()
	assetWithURI.URI = "https://provider.example/images/result.png"

	assetWithArtifact := assetImageRef()
	assetWithArtifact.Artifact = artifactImageRef().Artifact

	assetWithMediaType := assetImageRef()
	assetWithMediaType.MediaType = "image/png"

	assetWithDimensions := assetImageRef()
	assetWithDimensions.Width = 1024

	externalWithAsset := externalRef()
	externalWithAsset.Asset = assetImageRef().Asset

	externalWithArtifact := externalRef()
	externalWithArtifact.Artifact = artifactImageRef().Artifact

	artifactWithURI := artifactImageRef()
	artifactWithURI.URI = "https://provider.example/images/result.png"

	for name, ref := range map[string]ImageRef{
		"asset carries uri":         assetWithURI,
		"asset carries artifact":    assetWithArtifact,
		"asset carries mediaType":   assetWithMediaType,
		"asset carries width":       assetWithDimensions,
		"external carries asset":    externalWithAsset,
		"external carries artifact": externalWithArtifact,
		"artifact carries uri":      artifactWithURI,
	} {
		t.Run(name, func(t *testing.T) {
			if err := ref.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want an error for a ref mixing two branches")
			}
		})
	}
}

func TestImageRef_Validate_RejectsIncompleteAssetAndArtifact(t *testing.T) {
	missingAsset := assetImageRef()
	missingAsset.Asset = nil

	emptyAssetID := assetImageRef()
	emptyAssetID.Asset = &AssetRef{MediaType: "image/png", SizeBytes: 1, SHA256: "abc"}

	emptyAssetSHA := assetImageRef()
	emptyAssetSHA.Asset = &AssetRef{AssetID: "asset_123", MediaType: "image/png", SizeBytes: 1}

	zeroAssetSize := assetImageRef()
	zeroAssetSize.Asset = &AssetRef{AssetID: "asset_123", MediaType: "image/png", SHA256: "abc"}

	missingArtifact := artifactImageRef()
	missingArtifact.Artifact = nil

	emptyArtifactMediaType := artifactImageRef()
	emptyArtifactMediaType.Artifact = &ArtifactRef{ArtifactID: "artifact_123", SizeBytes: 1, SHA256: "abc"}

	for name, ref := range map[string]ImageRef{
		"asset absent":               missingAsset,
		"asset without assetId":      emptyAssetID,
		"asset without sha256":       emptyAssetSHA,
		"asset without sizeBytes":    zeroAssetSize,
		"artifact absent":            missingArtifact,
		"artifact without mediaType": emptyArtifactMediaType,
	} {
		t.Run(name, func(t *testing.T) {
			if err := ref.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want an error for an incomplete branch payload")
			}
		})
	}
}

func TestImageRef_Validate_RejectsInvalidExternalFields(t *testing.T) {
	cases := map[string]func(*ImageRef){
		"no uri":            func(r *ImageRef) { r.URI = "" },
		"relative uri":      func(r *ImageRef) { r.URI = "/v1/images/a.png" },
		"scheme-less uri":   func(r *ImageRef) { r.URI = "provider.example/a.png" },
		"non http scheme":   func(r *ImageRef) { r.URI = "ftp://provider.example/a.png" },
		"data uri":          func(r *ImageRef) { r.URI = "data:image/png;base64,iVBORw0KGgo=" },
		"no host":           func(r *ImageRef) { r.URI = "https:///a.png" },
		"no mediaType":      func(r *ImageRef) { r.MediaType = "" },
		"negative width":    func(r *ImageRef) { r.Width = -1 },
		"negative height":   func(r *ImageRef) { r.Height = -1 },
		"blank mediaType":   func(r *ImageRef) { r.MediaType = "   " },
		"whitespace in uri": func(r *ImageRef) { r.URI = "   " },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			ref := externalRef()
			mutate(&ref)
			if err := ref.Validate(); err == nil {
				t.Fatalf("Validate() error = nil, want an error; ref = %+v", ref)
			}
		})
	}
}

// TestParseImageRef_RejectsLegacyProviderShape is the regression this type exists for: the
// pre-ImageRef `{"url":...}` object a Provider used to publish verbatim is not an ImageRef
// and must not reach an `image` port (08 §2.2).
func TestParseImageRef_RejectsLegacyProviderShape(t *testing.T) {
	legacy := `{"url":"https://provider.example/images/result.png","mediaType":"image/png","width":1024}`
	if _, err := ParseImageRef(json.RawMessage(legacy)); err == nil {
		t.Fatal("ParseImageRef() error = nil, want an error for the legacy {\"url\":...} shape")
	}
}

func TestParseImageRef_RejectsUnknownFields(t *testing.T) {
	cases := map[string]string{
		"provider private top level": `{"source":"EXTERNAL","uri":"https://p.example/a.png","mediaType":"image/png","providerJobId":"job_1"}`,
		"signed url member":          `{"source":"EXTERNAL","uri":"https://p.example/a.png","mediaType":"image/png","signedUrl":"https://p.example/a.png?sig=x"}`,
		"unknown asset member":       `{"source":"ASSET","asset":{"assetId":"asset_1","mediaType":"image/png","sizeBytes":1,"sha256":"abc","storageKey":"local/abc"}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseImageRef(json.RawMessage(raw)); err == nil {
				t.Fatal("ParseImageRef() error = nil, want an error for an unknown member")
			}
		})
	}
}

func TestParseImageRef_RejectsNonObjectAndNonPositiveDimensions(t *testing.T) {
	cases := map[string]string{
		"string":       `"https://provider.example/a.png"`,
		"array":        `[]`,
		"empty object": `{}`,
		"null":         `null`,
		"zero width":   `{"source":"EXTERNAL","uri":"https://p.example/a.png","mediaType":"image/png","width":0}`,
		"zero height":  `{"source":"EXTERNAL","uri":"https://p.example/a.png","mediaType":"image/png","height":0}`,
		"float width":  `{"source":"EXTERNAL","uri":"https://p.example/a.png","mediaType":"image/png","width":10.5}`,
		"trailing":     `{"source":"EXTERNAL","uri":"https://p.example/a.png","mediaType":"image/png"} {}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseImageRef(json.RawMessage(raw)); err == nil {
				t.Fatalf("ParseImageRef(%s) error = nil, want an error", raw)
			}
		})
	}
}

// TestParseImageRef_CanonicalEncodingDropsAbsentBranches proves a parsed ref re-encodes to
// exactly the declared branch: a node may publish json.Marshal(ref) without leaking an
// empty `asset`, `artifact` or `width` member into the port value.
func TestParseImageRef_CanonicalEncodingDropsAbsentBranches(t *testing.T) {
	cases := map[string]struct{ raw, want string }{
		"external with width": {
			raw:  `{"mediaType":"image/png","width":1024,"uri":"https://p.example/a.png","source":"EXTERNAL"}`,
			want: `{"source":"EXTERNAL","uri":"https://p.example/a.png","mediaType":"image/png","width":1024}`,
		},
		"asset": {
			raw:  `{"source":"ASSET","asset":{"sha256":"abc","assetId":"asset_1","sizeBytes":102400,"mediaType":"image/png"}}`,
			want: `{"source":"ASSET","asset":{"assetId":"asset_1","mediaType":"image/png","sizeBytes":102400,"sha256":"abc"}}`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ref, err := ParseImageRef(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("ParseImageRef() error = %v, want nil", err)
			}
			encoded, err := json.Marshal(ref)
			if err != nil {
				t.Fatalf("Marshal() error = %v, want nil", err)
			}
			if string(encoded) != tc.want {
				t.Fatalf("canonical encoding = %s, want %s", encoded, tc.want)
			}
			// The canonical encoding is itself a valid ImageRef: re-parsing is a no-op.
			if _, err := ParseImageRef(encoded); err != nil {
				t.Fatalf("re-parsing the canonical encoding: error = %v, want nil", err)
			}
		})
	}
}

// TestParseImageRef_ErrorNamesTheOffendingRule keeps the parse error usable in a Trace-safe
// node error without echoing the payload back.
func TestParseImageRef_ErrorNamesTheOffendingRule(t *testing.T) {
	_, err := ParseImageRef(json.RawMessage(`{"source":"EXTERNAL","mediaType":"image/png"}`))
	if err == nil {
		t.Fatal("ParseImageRef() error = nil, want an error for EXTERNAL without uri")
	}
	if !strings.Contains(err.Error(), "uri") {
		t.Fatalf("ParseImageRef() error = %v, want it to name the missing member", err)
	}
}
