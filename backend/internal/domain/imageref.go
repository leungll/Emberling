package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ImageSource names which of ImageRef's three mutually exclusive branches carries the
// image (08 §2.2).
type ImageSource string

const (
	// ImageSourceAsset references an uploaded Emberling Asset through a complete AssetRef.
	ImageSourceAsset ImageSource = "ASSET"
	// ImageSourceArtifact references content the Runtime persisted itself, through a
	// complete ArtifactRef.
	ImageSourceArtifact ImageSource = "ARTIFACT"
	// ImageSourceExternal references content served by an external Provider through a
	// stable, credential-free absolute URI.
	ImageSourceExternal ImageSource = "EXTERNAL"
)

// ImageRef is the single value every `image` port carries (08 §2.2). A Provider's own
// response object never reaches a port: the Node that owns the Provider interaction
// normalises it into an ImageRef first, so a downstream node reads one shape regardless of
// where the image came from.
//
// Exactly one of the three branches is populated, selected by Source. Width and Height are
// optional and only meaningful for ImageSourceExternal; 0 means "absent", which is why
// ParseImageRef rejects an explicit 0 rather than silently accepting a Provider that
// reports an unknown dimension as zero.
//
// No member may carry a Secret, a temporarily signed URL, a Provider credential or an
// internal storage key: an ImageRef travels into NodeRun output, Run.output, Trace and the
// SSE stream.
type ImageRef struct {
	Source    ImageSource  `json:"source"`
	Asset     *AssetRef    `json:"asset,omitempty"`
	Artifact  *ArtifactRef `json:"artifact,omitempty"`
	URI       string       `json:"uri,omitempty"`
	MediaType string       `json:"mediaType,omitempty"`
	Width     int          `json:"width,omitempty"`
	Height    int          `json:"height,omitempty"`
}

// Validate reports whether r satisfies every rule of 08 §2.2. It is the only definition of
// "a valid ImageRef": a Node validates what it produces and what it consumes through this
// method, never through a shape check of its own.
func (r ImageRef) Validate() error {
	switch r.Source {
	case ImageSourceAsset:
		if err := r.rejectMembersOutside(ImageSourceAsset); err != nil {
			return err
		}
		if r.Asset == nil {
			return fmt.Errorf("image ref: source %s requires a complete asset", ImageSourceAsset)
		}
		return validateAssetMembers("asset", r.Asset.AssetID, "assetId", r.Asset.MediaType, r.Asset.SizeBytes, r.Asset.SHA256)

	case ImageSourceArtifact:
		if err := r.rejectMembersOutside(ImageSourceArtifact); err != nil {
			return err
		}
		if r.Artifact == nil {
			return fmt.Errorf("image ref: source %s requires a complete artifact", ImageSourceArtifact)
		}
		return validateAssetMembers("artifact", r.Artifact.ArtifactID, "artifactId", r.Artifact.MediaType, r.Artifact.SizeBytes, r.Artifact.SHA256)

	case ImageSourceExternal:
		if err := r.rejectMembersOutside(ImageSourceExternal); err != nil {
			return err
		}
		if err := validateAbsoluteImageURI(r.URI); err != nil {
			return err
		}
		if strings.TrimSpace(r.MediaType) == "" {
			return fmt.Errorf("image ref: source %s requires a mediaType", ImageSourceExternal)
		}
		if r.Width < 0 || r.Height < 0 {
			return fmt.Errorf("image ref: width and height must be positive when present")
		}
		return nil

	default:
		return fmt.Errorf("image ref: source %q is not one of %s, %s, %s", r.Source, ImageSourceAsset, ImageSourceArtifact, ImageSourceExternal)
	}
}

// rejectMembersOutside enforces the mutual exclusivity of the three branches: a member
// belonging to another branch is a malformed ref, not an ignorable extra.
func (r ImageRef) rejectMembersOutside(source ImageSource) error {
	if source != ImageSourceAsset && r.Asset != nil {
		return fmt.Errorf("image ref: source %s must not carry an asset", source)
	}
	if source != ImageSourceArtifact && r.Artifact != nil {
		return fmt.Errorf("image ref: source %s must not carry an artifact", source)
	}
	if source == ImageSourceExternal {
		return nil
	}
	// uri, mediaType, width and height describe an externally served image; an ASSET or
	// ARTIFACT branch describes its content through its own ref instead.
	if r.URI != "" {
		return fmt.Errorf("image ref: source %s must not carry a uri", source)
	}
	if r.MediaType != "" {
		return fmt.Errorf("image ref: source %s must not carry a top-level mediaType", source)
	}
	if r.Width != 0 || r.Height != 0 {
		return fmt.Errorf("image ref: source %s must not carry width or height", source)
	}
	return nil
}

// validateAssetMembers checks the four members AssetRef and ArtifactRef share. branch and
// idField name the caller's branch so the error identifies the offending member exactly.
func validateAssetMembers(branch, id, idField, mediaType string, sizeBytes int64, sha256 string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("image ref: %s requires %s", branch, idField)
	}
	if strings.TrimSpace(mediaType) == "" {
		return fmt.Errorf("image ref: %s requires mediaType", branch)
	}
	if sizeBytes <= 0 {
		return fmt.Errorf("image ref: %s requires a positive sizeBytes", branch)
	}
	if strings.TrimSpace(sha256) == "" {
		return fmt.Errorf("image ref: %s requires sha256", branch)
	}
	return nil
}

// validateAbsoluteImageURI accepts only an absolute http(s) URL with a host. A relative
// reference has no meaning outside the Provider that minted it, and any other scheme
// (data:, file:, s3:) is either inline binary content or an internal storage location -
// neither belongs on an `image` port.
func validateAbsoluteImageURI(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("image ref: source %s requires a uri", ImageSourceExternal)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("image ref: uri is not a valid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("image ref: uri must be an absolute http or https URL")
	}
	if parsed.Host == "" {
		return fmt.Errorf("image ref: uri must name a host")
	}
	return nil
}

// ParseImageRef decodes one `image` port value or one normalised Provider reference and
// validates it. Unknown members are rejected rather than dropped: a Provider-private field,
// a signed URL or a legacy `{"url":...}` object must fail loudly at the boundary instead of
// travelling on inside a value that merely looks like an ImageRef.
//
// The returned error never contains raw: a caller wraps it into a Node error that reaches
// Trace, and the payload may carry Provider content that does not belong there.
func ParseImageRef(raw json.RawMessage) (ImageRef, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ImageRef{}, fmt.Errorf("image ref: value is empty")
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire imageRefWire
	if err := decoder.Decode(&wire); err != nil {
		return ImageRef{}, fmt.Errorf("image ref: value is not an ImageRef object: %w", err)
	}
	if decoder.More() {
		return ImageRef{}, fmt.Errorf("image ref: value carries trailing content after the ImageRef object")
	}

	ref := ImageRef{
		Source:    wire.Source,
		Asset:     wire.Asset,
		Artifact:  wire.Artifact,
		URI:       wire.URI,
		MediaType: wire.MediaType,
	}
	// An explicitly present dimension must be a positive integer: 0 is this type's
	// "absent" encoding, so accepting it would turn an unknown dimension into a silent one.
	for _, dimension := range []struct {
		name  string
		value *int
		field *int
	}{
		{name: "width", value: wire.Width, field: &ref.Width},
		{name: "height", value: wire.Height, field: &ref.Height},
	} {
		if dimension.value == nil {
			continue
		}
		if *dimension.value <= 0 {
			return ImageRef{}, fmt.Errorf("image ref: %s must be a positive integer when present", dimension.name)
		}
		*dimension.field = *dimension.value
	}

	if err := ref.Validate(); err != nil {
		return ImageRef{}, err
	}
	return ref, nil
}

// imageRefWire is ImageRef's decode shape. It differs in one way only: the two optional
// dimensions are pointers, so ParseImageRef can tell "absent" from an explicit 0 that the
// contract forbids.
type imageRefWire struct {
	Source    ImageSource  `json:"source"`
	Asset     *AssetRef    `json:"asset"`
	Artifact  *ArtifactRef `json:"artifact"`
	URI       string       `json:"uri"`
	MediaType string       `json:"mediaType"`
	Width     *int         `json:"width"`
	Height    *int         `json:"height"`
}
