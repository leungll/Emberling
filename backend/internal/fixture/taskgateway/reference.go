package taskgateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// assetContentPath is Emberling's Asset content route; the asset id fills the %s.
const assetContentPath = "/api/assets/%s/content"

// taskFailure is a task's terminal failure: a stable code and a message that carries no
// credential, callback URL, prompt, image content or upstream body.
type taskFailure struct {
	code    string
	message string
}

// acceptedReferenceMediaTypes are the reference image types the upstream image model
// accepts.
var acceptedReferenceMediaTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true}

// resolvedReference is a fetched and verified reference image.
type resolvedReference struct {
	content   []byte
	mediaType string
}

// fetchReference resolves the dispatch's reference image to bytes. An ASSET reference is
// read through Emberling's Asset content route and must match the declared size and
// SHA-256; an EXTERNAL reference is read from its URI with no header of any kind. Both
// reads are bounded by maxImageBytes.
func (s *Server) fetchReference(ctx context.Context, ref referenceWire) (resolvedReference, *taskFailure) {
	switch ref.Source {
	case sourceAsset:
		target := s.assetBaseURL + fmt.Sprintf(assetContentPath, url.PathEscape(ref.Asset.AssetID))
		content, failure := s.download(ctx, target, func(status int) *taskFailure {
			if status == http.StatusNotFound {
				return &taskFailure{code: codeReferenceNotFound, message: fmt.Sprintf("reference asset %s was not found", ref.Asset.AssetID)}
			}
			return nil
		})
		if failure != nil {
			return resolvedReference{}, failure
		}
		if int64(len(content)) != ref.Asset.SizeBytes {
			return resolvedReference{}, &taskFailure{code: codeReferenceInvalid, message: "reference size does not match the asset reference"}
		}
		sum := sha256.Sum256(content)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), ref.Asset.SHA256) {
			return resolvedReference{}, &taskFailure{code: codeReferenceInvalid, message: "reference digest does not match the asset reference"}
		}
		return checkedReference(content, ref.Asset.MediaType)

	default: // sourceExternal; the source was validated at acceptance.
		parsed, err := url.Parse(ref.URI)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return resolvedReference{}, &taskFailure{code: codeReferenceInvalid, message: "reference uri is not an absolute http or https URL"}
		}
		content, failure := s.download(ctx, ref.URI, func(int) *taskFailure { return nil })
		if failure != nil {
			return resolvedReference{}, failure
		}
		return checkedReference(content, ref.MediaType)
	}
}

func checkedReference(content []byte, mediaType string) (resolvedReference, *taskFailure) {
	if !acceptedReferenceMediaTypes[mediaType] {
		return resolvedReference{}, &taskFailure{code: codeReferenceInvalid, message: "reference media type is not PNG, JPEG or WebP"}
	}
	return resolvedReference{content: content, mediaType: mediaType}, nil
}

// download performs one bounded GET. onStatus may claim a non-2xx status with a failure
// of its own; any other non-2xx status, a transport failure and a timeout are
// REFERENCE_FETCH_FAILED. The transport error's text is never used: it names the URL.
func (s *Server) download(ctx context.Context, target string, onStatus func(int) *taskFailure) ([]byte, *taskFailure) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, &taskFailure{code: codeReferenceFetchFailed, message: "reference fetch could not be built"}
	}
	resp, err := s.referenceClient.Do(req)
	if err != nil {
		return nil, &taskFailure{code: codeReferenceFetchFailed, message: "reference fetch failed before a response arrived"}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if failure := onStatus(resp.StatusCode); failure != nil {
			return nil, failure
		}
		return nil, &taskFailure{code: codeReferenceFetchFailed, message: fmt.Sprintf("reference fetch returned HTTP %d", resp.StatusCode)}
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, s.maxImageBytes+1))
	if err != nil {
		return nil, &taskFailure{code: codeReferenceFetchFailed, message: "reference fetch failed while reading the response"}
	}
	if int64(len(content)) > s.maxImageBytes {
		return nil, &taskFailure{code: codeReferenceInvalid, message: fmt.Sprintf("reference exceeds %d bytes", s.maxImageBytes)}
	}
	return content, nil
}
