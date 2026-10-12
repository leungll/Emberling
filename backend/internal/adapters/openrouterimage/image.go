package openrouterimage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// sniffBytes is how much of the decoded image is inspected before the image is handed
// to the caller.
const sniffBytes = 512

// ImageRequest is one generation: a prompt and one reference image. AspectRatio,
// Resolution and Seed are optional and are sent only when set; the Adapter neither
// rewrites nor drops them, so a value the upstream model rejects fails the call.
type ImageRequest struct {
	Prompt             string
	AspectRatio        string
	Resolution         string
	Seed               *int64
	ReferenceImage     []byte
	ReferenceMediaType string
}

// Image is the single generated image. Content yields the decoded bytes; MediaType is
// sniffed from them, never taken from the Provider's declaration. A decoding failure or a
// size over the cap that only shows while streaming surfaces as a read error from
// Content, never as a short successful read, so the caller must discard everything it
// read when Content fails.
//
// Model and Cost are what the Provider reported about the call, kept only for
// traceability: Model is the upstream model name and Cost the call's charge. Each is
// left empty when the answer omits it or reports it outside its bounds, and neither ever
// fails an image the Provider already generated.
type Image struct {
	MediaType string
	Content   io.Reader
	Model     string
	Cost      *float64
}

type imagesRequest struct {
	Model           string           `json:"model"`
	Prompt          string           `json:"prompt"`
	N               int              `json:"n"`
	AspectRatio     string           `json:"aspect_ratio,omitempty"`
	Resolution      string           `json:"resolution,omitempty"`
	Seed            *int64           `json:"seed,omitempty"`
	InputReferences []inputReference `json:"input_references"`
}

type inputReference struct {
	Type     string   `json:"type"`
	ImageURL imageURL `json:"image_url"`
}

type imageURL struct {
	URL string `json:"url"`
}

// imagesResponse keeps model and usage raw, so that a member of an unexpected type or
// range is dropped by reportedModel and reportedCost instead of failing the decode.
type imagesResponse struct {
	Data []struct {
		B64JSON *string `json:"b64_json"`
	} `json:"data"`
	Model json.RawMessage `json:"model"`
	Usage json.RawMessage `json:"usage"`
}

// GenerateImage performs exactly one Images API call for request, asking for one image.
func (p *Provider) GenerateImage(ctx context.Context, request ImageRequest) (Image, error) {
	body, err := p.encodeRequest(request)
	if err != nil {
		return Image{}, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Image{}, &Error{message: "call ended by the caller's context", cause: ctxErr}
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return Image{}, &Error{message: "build HTTP request failed"}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)

	response, err := p.client.Do(httpRequest)
	if err != nil {
		return Image{}, transportFailure(ctx, "send request", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		return Image{}, &Error{StatusCode: response.StatusCode, message: fmt.Sprintf("images API returned HTTP %d", response.StatusCode)}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, p.maxBodyBytes+1))
	if err != nil {
		return Image{}, transportFailure(ctx, "read response", err)
	}
	if int64(len(raw)) > p.maxBodyBytes {
		return Image{}, p.answerFailure(response.StatusCode, fmt.Sprintf("response body exceeds %d bytes", p.maxBodyBytes))
	}
	return p.decodeImage(response.StatusCode, raw)
}

func (p *Provider) encodeRequest(request ImageRequest) ([]byte, error) {
	if strings.TrimSpace(request.Prompt) == "" {
		return nil, &Error{message: "request has no prompt"}
	}
	if len(request.ReferenceImage) == 0 {
		return nil, &Error{message: "request has no reference image"}
	}
	if !acceptedMediaType(request.ReferenceMediaType) {
		return nil, &Error{message: "reference image is not PNG, JPEG or WebP"}
	}
	dataURL := "data:" + request.ReferenceMediaType + ";base64," + base64.StdEncoding.EncodeToString(request.ReferenceImage)
	body, err := json.Marshal(imagesRequest{
		Model:       p.model,
		Prompt:      request.Prompt,
		N:           1,
		AspectRatio: request.AspectRatio,
		Resolution:  request.Resolution,
		Seed:        request.Seed,
		InputReferences: []inputReference{{
			Type:     "image_url",
			ImageURL: imageURL{URL: dataURL},
		}},
	})
	if err != nil {
		return nil, &Error{message: "encode request failed"}
	}
	return body, nil
}

// decodeImage accepts exactly one b64_json image. The decoded size is checked against
// the cap before any byte is returned, and the leading bytes are sniffed so that a
// non-image fails here rather than in the caller's storage.
func (p *Provider) decodeImage(status int, raw []byte) (Image, error) {
	var answer imagesResponse
	if err := json.Unmarshal(raw, &answer); err != nil {
		return Image{}, p.answerFailure(status, "response body is not a JSON images answer")
	}
	if len(answer.Data) != 1 {
		return Image{}, p.answerFailure(status, fmt.Sprintf("response has %d images, want exactly 1", len(answer.Data)))
	}
	if answer.Data[0].B64JSON == nil {
		return Image{}, p.answerFailure(status, "response image has no b64_json")
	}
	encoded := *answer.Data[0].B64JSON
	if decodedLen(encoded) > p.maxImageBytes {
		return Image{}, p.answerFailure(status, fmt.Sprintf("image exceeds %d bytes", p.maxImageBytes))
	}

	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))
	head := make([]byte, sniffBytes)
	n, err := io.ReadFull(decoder, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return Image{}, p.answerFailure(status, "response image is not valid base64")
	}
	head = head[:n]
	mediaType := sniffImageMediaType(head)
	if mediaType == "" {
		return Image{}, p.answerFailure(status, "response image is not PNG, JPEG or WebP")
	}
	return Image{
		MediaType: mediaType,
		Content:   &cappedImage{source: io.MultiReader(bytes.NewReader(head), decoder), remaining: p.maxImageBytes},
		Model:     reportedModel(answer.Model),
		Cost:      reportedCost(answer.Usage),
	}, nil
}

// maxModelRunes bounds the reported model name carried for traceability.
const maxModelRunes = 128

// reportedModel returns the upstream model name when the answer reports it as a
// non-blank string of at most maxModelRunes characters without control characters, and
// "" otherwise.
func reportedModel(raw json.RawMessage) string {
	var model string
	if len(raw) == 0 || json.Unmarshal(raw, &model) != nil {
		return ""
	}
	if strings.TrimSpace(model) == "" || utf8.RuneCountInString(model) > maxModelRunes {
		return ""
	}
	if strings.ContainsFunc(model, unicode.IsControl) {
		return ""
	}
	return model
}

// reportedCost returns usage.cost when the answer reports it as a finite, non-negative
// JSON number, and nil otherwise.
func reportedCost(raw json.RawMessage) *float64 {
	var usage struct {
		Cost json.RawMessage `json:"cost"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &usage) != nil || len(usage.Cost) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(usage.Cost))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil
	}
	number, ok := value.(json.Number)
	if !ok {
		return nil
	}
	cost, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || math.IsInf(cost, 0) || math.IsNaN(cost) || cost < 0 {
		return nil
	}
	if cost == 0 {
		cost = 0 // a reported -0 is carried as 0
	}
	return &cost
}

// decodedLen is the decoded size of standard padded base64. It decides the cap before
// streaming; cappedImage still enforces the cap on the bytes actually produced.
func decodedLen(encoded string) int64 {
	padding := len(encoded) - len(strings.TrimRight(encoded, "="))
	return int64(len(encoded))/4*3 + int64(len(encoded)%4)*3/4 - int64(padding)
}

// cappedImage streams the decoded image and turns a decoding error or a size over the
// cap into an *Error, so the caller never mistakes a truncated image for a whole one.
type cappedImage struct {
	source    io.Reader
	remaining int64
	failed    error
}

func (c *cappedImage) Read(buf []byte) (int, error) {
	if c.failed != nil {
		return 0, c.failed
	}
	n, err := c.source.Read(buf)
	c.remaining -= int64(n)
	if c.remaining < 0 {
		c.failed = &Error{message: "image exceeds the maximum size"}
		return 0, c.failed
	}
	switch {
	case err == nil, errors.Is(err, io.EOF):
		return n, err
	default:
		c.failed = &Error{message: "response image is not valid base64"}
		return 0, c.failed
	}
}

// transportFailure distinguishes the caller's context ending the call, which is wrapped
// so the caller recognises its own deadline or cancellation, from this client's own
// timeout or a connection failure, which is reported without wrapping a context error.
// The transport error text is left out: it is not needed to classify the failure.
func transportFailure(ctx context.Context, op string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return &Error{message: "call ended by the caller's context", cause: ctxErr}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &Error{message: op + " timed out"}
	}
	return &Error{message: op + " failed"}
}

func (p *Provider) answerFailure(status int, message string) error {
	return &Error{StatusCode: status, message: message}
}

func acceptedMediaType(mediaType string) bool {
	switch mediaType {
	case "image/png", "image/jpeg", "image/webp":
		return true
	}
	return false
}

// sniffImageMediaType recognises the image types an Execution Artifact may hold from
// their signatures; anything else is not an accepted image.
func sniffImageMediaType(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(head, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	case len(head) >= 12 && bytes.Equal(head[:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP")):
		return "image/webp"
	}
	return ""
}
