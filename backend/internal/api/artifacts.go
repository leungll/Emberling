package api

import (
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// artifactContentCacheControl lets a client cache artifact content privately for a long
// time: an artifact id is derived from its content, so the content behind it never
// changes, while "private" keeps shared caches out of generated business content.
const artifactContentCacheControl = "private, max-age=31536000, immutable"

// getArtifactContent serves GET /runs/{runId}/artifacts/{artifactId}/content: the content
// of an Execution Artifact a committed result of this Run declared. It is read-only and
// writes no row, state or Event.
//
// A Run that does not exist, an artifact the Run never declared and an artifact without
// metadata are one 404 ARTIFACT_NOT_FOUND. Content that is missing or fails its size and
// digest check is a 500 with no content: the service verifies it in full before anything
// is written here, so a corrupt object is never streamed. Range requests are not
// supported; the full content is always served.
func (d Deps) getArtifactContent(w http.ResponseWriter, r *http.Request) {
	artifact, content, err := d.Artifacts.OpenRunContent(r.Context(), chi.URLParam(r, "runId"), chi.URLParam(r, "artifactId"))
	if err != nil {
		writeError(w, d.Logger, err, codeArtifactNotFound)
		return
	}
	defer content.Close()

	w.Header().Set("Content-Type", artifact.MediaType)
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.SizeBytes, 10))
	w.Header().Set("ETag", `"`+artifact.SHA256+`"`)
	w.Header().Set("Cache-Control", artifactContentCacheControl)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, content); err != nil {
		// The status line and headers are already written, so the only honest signal left
		// is the truncated body the client will detect against Content-Length.
		d.Logger.Warn("api: artifact download interrupted",
			"artifact_id", artifact.ArtifactID, "error", err)
	}
}
