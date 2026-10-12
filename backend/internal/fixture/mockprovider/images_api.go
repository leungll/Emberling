package mockprovider

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strconv"
	"strings"
)

// imagesAPIPath is the OpenRouter-shaped Images API route. A caller configured with this
// Provider's origin plus /api/v1 as its Images API base URL reaches it as <base>/images.
const imagesAPIPath = "/api/v1/images"

// imagesAPIScenario starts the scenario summary of every Images API request in the record,
// which tells its lines apart from POST /v1/assets lines of the same image kind.
const imagesAPIScenario = "api=images"

// maxImagesAPIRequestBytes bounds one Images API request. The body carries the reference
// photo as a base64 data URL, so it is far larger than the other request bodies here.
const maxImagesAPIRequestBytes = 32 << 20

// imagesAPIStatusDirective, at the end of the requested model name, makes the request
// fail with the given HTTP status in the 400-599 range. The model name is deployment
// configuration, so a test selects the failure by configuring an image model, without
// any field the real Images API would not carry and without reading the prompt.
const imagesAPIStatusDirective = ":mock-status-"

// imagesAPIFailureDetail is the fixed detail of a failure answer. It stands for an
// upstream response body, which a caller must never copy into its own errors or logs.
const imagesAPIFailureDetail = "mock provider: upstream image failure detail"

// imagesAPICost is the charge every successful answer reports in usage.cost.
const imagesAPICost = 0.004

// imagesAPIRequest is the part of an Images API request this Provider reads. The prompt,
// the reference image and the Authorization header are neither validated nor recorded.
type imagesAPIRequest struct {
	Model string `json:"model"`
	N     *int   `json:"n"`
}

type imagesAPIAnswer struct {
	Model string              `json:"model"`
	Data  []imagesAPIImage    `json:"data"`
	Usage imagesAPIAnswerCost `json:"usage"`
}

type imagesAPIImage struct {
	B64JSON string `json:"b64_json"`
}

type imagesAPIAnswerCost struct {
	Cost float64 `json:"cost"`
}

type imagesAPIError struct {
	Error imagesAPIErrorDetail `json:"error"`
}

type imagesAPIErrorDetail struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// handleImagesAPI simulates one OpenRouter Images API call: it answers exactly one
// deterministic gradient PNG as data[0].b64_json, with the requested model and a fixed
// usage.cost. Each request is recorded under the image kind, so the image barrier can
// hold it and a test can count the calls that reached the Provider.
func (s *Server) handleImagesAPI(w http.ResponseWriter, r *http.Request) {
	arrival := recordEntry{Kind: kindImage, recordDetail: recordDetail{Scenario: imagesAPIScenario}}
	var req imagesAPIRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxImagesAPIRequestBytes)).Decode(&req); err != nil {
		s.reject(w, arrival, http.StatusBadRequest, "request body is not a valid images request")
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		s.reject(w, arrival, http.StatusBadRequest, "model is required")
		return
	}
	if req.N != nil && *req.N != 1 {
		s.reject(w, arrival, http.StatusBadRequest, "n must be 1")
		return
	}
	status, err := imagesAPIStatus(req.Model)
	if err != nil {
		s.reject(w, arrival, http.StatusBadRequest, err.Error())
		return
	}
	arrival.Scenario = imagesAPIScenario + ",status=" + strconv.Itoa(status)
	if err := s.admit(r, arrival); err != nil {
		s.respondHeldError(w, kindImage, "", err)
		return
	}

	if status != http.StatusOK {
		s.recordResponse(kindImage, "", status, false)
		writeJSON(w, status, imagesAPIError{Error: imagesAPIErrorDetail{Code: status, Message: imagesAPIFailureDetail}})
		return
	}
	encoded, err := gradientPNG(req.Model)
	if err != nil {
		s.recordResponse(kindImage, "", http.StatusInternalServerError, false)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "image could not be encoded"})
		return
	}
	contentSum := sha256.Sum256(encoded)
	s.recordMedia(recordEntry{Event: recordGenerated, Kind: kindImage, recordDetail: recordDetail{
		SHA256: hex.EncodeToString(contentSum[:]),
	}})
	s.recordResponse(kindImage, "", http.StatusOK, false)
	writeJSON(w, http.StatusOK, imagesAPIAnswer{
		Model: req.Model,
		Data:  []imagesAPIImage{{B64JSON: base64.StdEncoding.EncodeToString(encoded)}},
		Usage: imagesAPIAnswerCost{Cost: imagesAPICost},
	})
}

// imagesAPIStatus returns the status a request for model is answered with: 200, or the
// failure status its name's directive selects.
func imagesAPIStatus(model string) (int, error) {
	_, directive, found := strings.Cut(model, imagesAPIStatusDirective)
	if !found {
		return http.StatusOK, nil
	}
	status, err := strconv.Atoi(directive)
	if err != nil || status < 400 || status > 599 {
		return 0, errors.New("model status directive must name a status from 400 to 599")
	}
	return status, nil
}

// gradientPNG renders a square gradient whose blue channel is derived from seed, so
// different models answer visibly different images and the same model always the same.
func gradientPNG(seed string) ([]byte, error) {
	sum := sha256.Sum256([]byte(seed))
	canvas := image.NewRGBA(image.Rect(0, 0, generatedImageSide, generatedImageSide))
	step := uint8(256 / generatedImageSide)
	for y := 0; y < generatedImageSide; y++ {
		for x := 0; x < generatedImageSide; x++ {
			canvas.SetRGBA(x, y, color.RGBA{R: uint8(x) * step, G: uint8(y) * step, B: sum[0], A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, errors.New("encode gradient image")
	}
	return buf.Bytes(), nil
}
