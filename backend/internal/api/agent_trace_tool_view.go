package api

import (
	"bytes"
	"encoding/json"
	"net/url"
	"sort"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// ---- Agent Trace: Tool-call facts ----
//
// A Tool Attempt's input and result stay out of the Agent Trace as whole documents. The
// projection below shows only the few members Studio names on a run card, for four Tools,
// keyed by their stable Tool Name. The Tool registration contract carries no display
// declaration to drive this generically, so the table is fixed here; it only selects what
// to show and never feeds a Runtime decision. Every member is type-checked: a Tool
// registered under one of these names with another schema yields nothing, not a guess.
//
// Argument members come from the Attempt's input, written once when the Attempt was
// created. Result members are read only from a SUCCEEDED Attempt, the one status whose
// result is a committed Tool result. Values are copied from the persisted documents and
// never written back.

// Tool Names whose calls this projection describes.
const (
	toolGenerateImage = "generate_image"
	toolGenerateVideo = "generate_video"
	toolSandboxTest   = "sandbox_test"
	toolDeploy        = "deploy"
)

// Reasons a projected member is withheld instead of shown.
const (
	withheldTooLarge  = "TOO_LARGE"
	withheldUnsafeURL = "UNSAFE_URL"
)

// toolPhotoDTO names the photo a generate_image call works on. Position (1-based) and
// Count place it in the Run input's photo set; both are absent when the set does not
// determine one position.
type toolPhotoDTO struct {
	PhotoAssetID string `json:"photoAssetId"`
	Position     *int   `json:"position,omitempty"`
	Count        *int   `json:"count,omitempty"`
}

// toolTestDTO is a sandbox_test result's verdict and counts, as the runner reported them.
type toolTestDTO struct {
	Passed  bool               `json:"passed"`
	Summary toolTestSummaryDTO `json:"summary"`
}

type toolTestSummaryDTO struct {
	Total  int64 `json:"total"`
	Failed int64 `json:"failed"`
}

// toolDeploymentDTO is a deploy call's target, from its arguments, and the version
// production reported, from its result.
type toolDeploymentDTO struct {
	Target  *toolDeployTargetDTO `json:"target,omitempty"`
	Version *string              `json:"version,omitempty"`
}

type toolDeployTargetDTO struct {
	Service     string `json:"service"`
	Environment string `json:"environment"`
}

// toolVideoDTO is the video a generate_video call produced, by its Provider URL.
type toolVideoDTO struct {
	VideoURL string `json:"videoUrl"`
}

// withheldFieldDTO names a projected member left out of the response and why, so a
// missing value is never mistaken for one that was not recorded.
type withheldFieldDTO struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// toolAttemptView is the projection's output for one Tool Attempt.
type toolAttemptView struct {
	Photo      *toolPhotoDTO
	Test       *toolTestDTO
	Deployment *toolDeploymentDTO
	Video      *toolVideoDTO
	Withheld   []withheldFieldDTO
}

// toolViewProjector holds what every Tool Attempt of one Trace is projected against.
type toolViewProjector struct {
	maxFieldBytes int
	photoSets     []assetList
}

func newToolViewProjector(runInput json.RawMessage, maxFieldBytes int) toolViewProjector {
	return toolViewProjector{maxFieldBytes: maxFieldBytes, photoSets: runInputAssetLists(runInput)}
}

func (p toolViewProjector) project(attempt domain.ToolAttempt) toolAttemptView {
	var view toolAttemptView
	arguments := decodeObject(attempt.Input)
	var result map[string]json.RawMessage
	if attempt.Status == domain.ToolAttemptSucceeded {
		result = decodeObject(attempt.Result)
	}

	switch attempt.ToolName {
	case toolGenerateImage:
		if id, ok := p.boundedString(&view, "photo.photoAssetId", arguments["photoAssetId"]); ok {
			photo := &toolPhotoDTO{PhotoAssetID: id}
			if position, count, found := positionInPhotoSet(p.photoSets, id); found {
				photo.Position, photo.Count = &position, &count
			}
			view.Photo = photo
		}
	case toolSandboxTest:
		view.Test = projectTest(result)
	case toolDeploy:
		deployment := &toolDeploymentDTO{}
		if target := decodeObject(arguments["target"]); target != nil {
			service, okService := p.boundedString(&view, "deployment.target.service", target["service"])
			environment, okEnvironment := p.boundedString(&view, "deployment.target.environment", target["environment"])
			if okService && okEnvironment {
				deployment.Target = &toolDeployTargetDTO{Service: service, Environment: environment}
			}
		}
		if version, ok := p.boundedString(&view, "deployment.version", result["version"]); ok {
			deployment.Version = &version
		}
		if deployment.Target != nil || deployment.Version != nil {
			view.Deployment = deployment
		}
	case toolGenerateVideo:
		if videoURL, ok := p.boundedString(&view, "video.videoUrl", result["videoUrl"]); ok {
			if credentialFreeURL(videoURL) {
				view.Video = &toolVideoDTO{VideoURL: videoURL}
			} else {
				view.Withheld = append(view.Withheld, withheldFieldDTO{Field: "video.videoUrl", Reason: withheldUnsafeURL})
			}
		}
	}
	return view
}

// boundedString reads raw as a non-empty JSON string. A string whose encoding exceeds
// the field bound is recorded as withheld under field and not returned: a cut identifier
// or URL would read as a different value.
func (p toolViewProjector) boundedString(view *toolAttemptView, field string, raw json.RawMessage) (string, bool) {
	var value string
	if raw == nil || json.Unmarshal(raw, &value) != nil || value == "" {
		return "", false
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	if len(encoded) > p.maxFieldBytes {
		view.Withheld = append(view.Withheld, withheldFieldDTO{Field: field, Reason: withheldTooLarge})
		return "", false
	}
	return value, true
}

// projectTest reads the verdict and counts of a committed sandbox_test result. Booleans
// and integers have a bounded encoding, so they are never withheld for size.
func projectTest(result map[string]json.RawMessage) *toolTestDTO {
	var passed bool
	if result == nil || json.Unmarshal(result["passed"], &passed) != nil {
		return nil
	}
	summary := decodeObject(result["summary"])
	total, okTotal := nonNegativeInteger(summary["total"])
	failed, okFailed := nonNegativeInteger(summary["failed"])
	if !okTotal || !okFailed {
		return nil
	}
	return &toolTestDTO{Passed: passed, Summary: toolTestSummaryDTO{Total: total, Failed: failed}}
}

func nonNegativeInteger(raw json.RawMessage) (int64, bool) {
	if raw == nil {
		return 0, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var number json.Number
	if decoder.Decode(&number) != nil {
		return 0, false
	}
	value, err := number.Int64()
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

// credentialFreeURL accepts only an absolute http or https URL with a host and without
// userinfo, query or fragment: the parts of a URL where a signature, token or password
// travels. A Provider URL that has any of them is withheld rather than shown.
func credentialFreeURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" &&
		parsed.User == nil && parsed.RawQuery == "" && !parsed.ForceQuery &&
		parsed.Fragment == "" && parsed.RawFragment == "" && parsed.Opaque == ""
}

// decodeObject decodes raw as a JSON object, or returns nil for anything else.
func decodeObject(raw json.RawMessage) map[string]json.RawMessage {
	if !hasJSONValue(raw) {
		return nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil
	}
	return object
}

// assetList is one top-level Run input member holding a list of AssetRefs, as asset ids
// in their recorded order.
type assetList []string

// runInputAssetLists finds the Run input members that are non-empty arrays whose every
// element is an object with a non-empty string assetId, such as the photo set a Run is
// created with. Members are visited in sorted key order so the result does not depend on
// map iteration.
func runInputAssetLists(runInput json.RawMessage) []assetList {
	input := decodeObject(runInput)
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var lists []assetList
	for _, key := range keys {
		var items []json.RawMessage
		if json.Unmarshal(input[key], &items) != nil || len(items) == 0 {
			continue
		}
		list := make(assetList, 0, len(items))
		for _, item := range items {
			var ref struct {
				AssetID *string `json:"assetId"`
			}
			if json.Unmarshal(item, &ref) != nil || ref.AssetID == nil || *ref.AssetID == "" {
				list = nil
				break
			}
			list = append(list, *ref.AssetID)
		}
		if list != nil {
			lists = append(lists, list)
		}
	}
	return lists
}

// positionInPhotoSet places assetID in the one asset list that holds it. It reports found
// only when exactly one list holds the id, exactly once; any other case leaves the
// position undetermined rather than picking one.
func positionInPhotoSet(lists []assetList, assetID string) (position, count int, found bool) {
	for _, list := range lists {
		for index, id := range list {
			if id != assetID {
				continue
			}
			if found {
				return 0, 0, false
			}
			position, count, found = index+1, len(list), true
		}
	}
	return position, count, found
}
