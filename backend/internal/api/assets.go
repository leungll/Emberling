package api

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// multipartFramingSlack is how much of an upload body is allowed to be multipart framing
// (boundaries, part headers) rather than content. The real content limit is enforced by
// the storage layer while streaming; this only keeps an oversized body from being read in
// full, and must not turn an upload of exactly the allowed size into a 413.
const multipartFramingSlack = 8 << 10

// assetMultipartMemoryBytes is how much of an upload net/http keeps in memory before
// spilling the remainder to a temporary file it owns and removes. Content is copied out
// of that part into Asset storage, which is the only place it survives.
const assetMultipartMemoryBytes = 1 << 20

// assetFilePartName is the single accepted part of POST /assets (one image per upload).
// Another name, no part, or more than one part is a malformed request, not an upload of
// the first thing that happens to look like a file.
const assetFilePartName = "file"

// assetContentCacheControl lets a client cache downloaded content, privately. An Asset is
// immutable — replacing content creates a new Asset — so a long lifetime is
// safe, while "private" keeps shared caches out of business content.
const assetContentCacheControl = "private, max-age=31536000, immutable"

// assetMetadataDTO is GET /assets/{assetId}. It is the interface AssetRef plus the
// creation time Asset Metadata records. The internal storage key is absent by
// construction: it is a system Secret and never leaves the store boundary.
type assetMetadataDTO struct {
	AssetID   string    `json:"assetId"`
	MediaType string    `json:"mediaType"`
	SizeBytes int64     `json:"sizeBytes"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"createdAt"`
}

func toAssetMetadataDTO(a domain.Asset) assetMetadataDTO {
	return assetMetadataDTO{
		AssetID:   a.AssetID,
		MediaType: a.MediaType,
		SizeBytes: a.SizeBytes,
		SHA256:    a.SHA256,
		CreatedAt: a.CreatedAt,
	}
}

// uploadAsset implements POST /assets: one multipart/form-data file part named "file",
// answered with the immutable AssetRef only after both the content and its Metadata are
// committed.
//
// The declared part size is passed to the service as a cross-check only; the recorded
// size and digest always come from the bytes that actually landed.
func (d Deps) uploadAsset(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, d.AssetMaxUploadBytes+multipartFramingSlack)

	if err := r.ParseMultipartForm(assetMultipartMemoryBytes); err != nil {
		// The parse error is not surfaced: it can quote part headers and body fragments.
		writeError(w, d.Logger, mapMultipartError(err), "")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	files := r.MultipartForm.File[assetFilePartName]
	if len(files) != 1 || len(r.MultipartForm.File) != 1 || len(r.MultipartForm.Value) != 0 {
		writeError(w, d.Logger, &badRequestError{
			message: `upload must carry exactly one multipart file part named "file"`,
		}, "")
		return
	}
	header := files[0]

	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		writeError(w, d.Logger, &badRequestError{
			message: "the file part must declare a Content-Type",
		}, "")
		return
	}

	content, err := header.Open()
	if err != nil {
		writeError(w, d.Logger, err, "")
		return
	}
	defer content.Close()

	ref, err := d.Assets.Upload(r.Context(), mediaType, content, header.Size)
	if err != nil {
		writeError(w, d.Logger, err, codeAssetNotFound)
		return
	}
	writeJSON(w, http.StatusCreated, ref)
}

// getAsset implements GET /assets/{assetId}: Asset Metadata, without the storage key.
func (d Deps) getAsset(w http.ResponseWriter, r *http.Request) {
	asset, err := d.Assets.Get(r.Context(), chi.URLParam(r, "assetId"))
	if err != nil {
		writeError(w, d.Logger, err, codeAssetNotFound)
		return
	}
	writeJSON(w, http.StatusOK, toAssetMetadataDTO(asset))
}

// getAssetContent is the controlled download the interface contract allows instead of a
// short-lived signed URL: the Backend streams the content itself, so no storage location
// or bearer token ever reaches a client. The digest is the ETag because an Asset is
// immutable.
func (d Deps) getAssetContent(w http.ResponseWriter, r *http.Request) {
	asset, content, err := d.Assets.OpenContent(r.Context(), chi.URLParam(r, "assetId"))
	if err != nil {
		writeError(w, d.Logger, err, codeAssetNotFound)
		return
	}
	defer content.Close()

	w.Header().Set("Content-Type", asset.MediaType)
	w.Header().Set("Content-Length", strconv.FormatInt(asset.SizeBytes, 10))
	w.Header().Set("ETag", `"`+asset.SHA256+`"`)
	w.Header().Set("Cache-Control", assetContentCacheControl)
	w.Header().Set("Content-Disposition", "inline")
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, content); err != nil {
		// The status line and headers are already written, so the only honest signal left
		// is the truncated body the client will detect against Content-Length.
		d.Logger.Warn("api: asset download interrupted",
			"asset_id", asset.AssetID, "error", err)
	}
}

// mapMultipartError classifies a failure to parse an upload body. A body that outgrew the
// request limit is 413 PAYLOAD_TOO_LARGE; anything else is a malformed request.
func mapMultipartError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return domain.ErrAssetTooLarge
	}
	return &badRequestError{message: "request body is not a valid multipart/form-data upload"}
}
