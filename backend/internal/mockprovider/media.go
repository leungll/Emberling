package mockprovider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Media record events. Each names one simulated generation the Provider performed, so a
// script can tell which external media work actually happened apart from what Emberling
// committed about it.
const (
	// recordGenerated is written when POST /v1/assets generates an image.
	recordGenerated = "generated"
	// recordVideoDispatched is written when POST /v1/tasks accepts a video task.
	recordVideoDispatched = "video_dispatched"
)

// kindImage marks a synchronous image generation request. It has no barrier kind filter
// of its own, so only a pause of every kind holds it.
const kindImage = "image"

// mediaVideo is the POST /v1/tasks `media` value that makes a task a video generation.
const mediaVideo = "video"

// maxImageRequestBytes bounds a POST /v1/assets body; a valid one names one photo and one
// settings digest.
const maxImageRequestBytes = 16 << 10

// generatedImageSide is the edge length, in pixels, of every generated image.
const generatedImageSide = 16

// generatedIDPattern is the shape of a generated asset id, and therefore of the only
// names GET /v1/assets/{name}.png serves.
var generatedIDPattern = regexp.MustCompile(`^img_[0-9a-f]{16}$`)

// imageRequest is the POST /v1/assets body.
type imageRequest struct {
	// PhotoAssetID names the source photo; it is only an identifier to this Provider.
	PhotoAssetID string `json:"photoAssetId"`
	// SettingsDigest is the caller's digest of the generation settings. Together with
	// PhotoAssetID it fixes the generated asset id, so an identical request generates the
	// same asset.
	SettingsDigest string `json:"settingsDigest"`
	// Outcome "failed" makes the generation fail definitely, with no asset generated.
	Outcome string `json:"outcome,omitempty"`
}

// imageResponse is the 200 answer to POST /v1/assets.
type imageResponse struct {
	AssetID  string `json:"assetId"`
	ImageURL string `json:"imageUrl"`
}

// handleGenerateImage simulates a synchronous external image generation. It answers with a
// deterministic asset id and a credential-free URL under GET /v1/assets/{id}.png that
// serves that asset's image.
func (s *Server) handleGenerateImage(w http.ResponseWriter, r *http.Request) {
	arrival := recordEntry{Kind: kindImage}
	var req imageRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxImageRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		s.reject(w, arrival, http.StatusBadRequest, "request body is not a valid image request")
		return
	}
	if strings.TrimSpace(req.PhotoAssetID) == "" || strings.TrimSpace(req.SettingsDigest) == "" {
		s.reject(w, arrival, http.StatusBadRequest, "photoAssetId and settingsDigest are required")
		return
	}
	failed := req.Outcome == outcomeFailedName
	if !failed && req.Outcome != "" && req.Outcome != outcomeSucceededName {
		s.reject(w, arrival, http.StatusBadRequest, `outcome must be "succeeded" or "failed"`)
		return
	}
	if failed {
		arrival.Scenario = "outcome=" + outcomeFailedName
	} else {
		arrival.Scenario = "outcome=" + outcomeSucceededName
	}
	if err := s.admit(r, arrival); err != nil {
		s.respondHeldError(w, kindImage, "", err)
		return
	}

	if failed {
		s.recordResponse(kindImage, "", http.StatusUnprocessableEntity, false)
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "mock provider: image generation failed"})
		return
	}

	assetID := generatedAssetID(req.PhotoAssetID, req.SettingsDigest)
	s.recordMedia(recordEntry{Event: recordGenerated, Kind: kindImage, AssetID: assetID})
	s.recordResponse(kindImage, "", http.StatusOK, false)
	writeJSON(w, http.StatusOK, imageResponse{
		AssetID:  assetID,
		ImageURL: requestBaseURL(r) + "/v1/assets/" + assetID + ".png",
	})
}

// handleAsset serves the image of one generated asset. The colour is derived from the
// asset id, so different assets are visibly different and the same asset always renders
// the same. Like the fixed image route it takes no credential and stores nothing.
func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	id, isPNG := strings.CutSuffix(chi.URLParam(r, "name"), ".png")
	if !isPNG || !generatedIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	encoded, err := generatedPNG(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "image could not be encoded"})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(encoded)))
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(imageMaxAgeSeconds))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

// generatedAssetID derives the asset id from the photo and settings digest alone.
func generatedAssetID(photoAssetID, settingsDigest string) string {
	sum := sha256.Sum256([]byte(photoAssetID + "\x00" + settingsDigest))
	return "img_" + hex.EncodeToString(sum[:8])
}

// generatedPNG renders a solid square coloured from the asset id.
func generatedPNG(id string) ([]byte, error) {
	sum := sha256.Sum256([]byte(id))
	fill := color.RGBA{R: sum[0], G: sum[1], B: sum[2], A: 0xff}
	canvas := image.NewRGBA(image.Rect(0, 0, generatedImageSide, generatedImageSide))
	for y := 0; y < generatedImageSide; y++ {
		for x := 0; x < generatedImageSide; x++ {
			canvas.SetRGBA(x, y, fill)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, errors.New("encode generated image")
	}
	return buf.Bytes(), nil
}

// videoSucceededPayload is the callback body a video task reports on success: the URL of
// the generated video, named after the task.
func videoSucceededPayload(baseURL, externalTaskID string) json.RawMessage {
	sum := sha256.Sum256([]byte(externalTaskID))
	encoded, err := json.Marshal(map[string]string{
		"status":   "SUCCEEDED",
		"videoUrl": baseURL + "/v1/videos/vid_" + hex.EncodeToString(sum[:8]) + ".mp4",
	})
	if err != nil {
		return succeededPayload
	}
	return encoded
}

// requestBaseURL is the scheme and host the caller used to reach this Provider. A URL
// built from it is reachable by whoever can reach the Provider under that same name.
func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// recordMedia appends one media line. It is a no-op without test controls.
func (s *Server) recordMedia(entry recordEntry) {
	if s.controls == nil {
		return
	}
	_, _ = s.controls.record.append(entry)
}
