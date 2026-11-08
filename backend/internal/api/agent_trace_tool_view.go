package api

import (
	"bytes"
	"encoding/json"
	"net/url"
	"sort"
	"unicode/utf8"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// ---- Agent Trace: Tool-call facts ----
//
// A Tool Attempt's input and result stay out of the Agent Trace as whole documents. The
// projection below shows the few members Studio's existing cards address by name, for the
// Tools listed here, keyed by their stable Tool Name. The generic path is the Tool
// registration's display declaration, projected by agent_trace_display_view.go for any
// Tool; this table remains for the named card shapes those Tools already have. It only
// selects what to show and never feeds a Runtime decision. Every member is type-checked: a
// Tool registered under one of these names with another schema yields nothing, not a
// guess.
//
// Argument members come from the Attempt's input, written once when the Attempt was
// created. Result members are read only from a SUCCEEDED Attempt, the one status whose
// result is a committed Tool result. Values are copied from the persisted documents and
// never written back.

// Tool Names whose calls this projection describes.
const (
	toolGenerateImage = "generate_image"
	toolReviewAsset   = "review_asset"
	toolGenerateVideo = "generate_video"
	toolSandboxTest   = "sandbox_test"
	toolVerifyPatch   = "verify_patch"
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

// toolSettingsDTO is the generation settings a generate_image call asked for, from its
// arguments: only the prompt, aspect ratio and resolution, each absent when the call did
// not set it. The prompt is free text, so a long one is cut at maxProjectedPromptBytes on
// a character boundary and PromptTruncated marks the cut.
type toolSettingsDTO struct {
	Prompt          *string `json:"prompt,omitempty"`
	PromptTruncated bool    `json:"promptTruncated,omitempty"`
	AspectRatio     *string `json:"aspectRatio,omitempty"`
	Resolution      *string `json:"resolution,omitempty"`
}

// maxProjectedPromptBytes bounds the prompt shown for a generate_image call.
const maxProjectedPromptBytes = 500

// toolReviewDTO is a review_asset result's verdict and the reasons the review gave.
// Reasons is absent when the review gave none or they do not have the bounded shape the
// Tool declares.
type toolReviewDTO struct {
	Passed  bool     `json:"passed"`
	Reasons []string `json:"reasons,omitempty"`
}

// Review reason bounds, matching what review_asset declares for its result.
const (
	maxProjectedReviewReasons = 3
	maxProjectedReasonRunes   = 200
)

// toolTestDTO is a sandbox_test result's verdict and counts, as the runner reported them.
type toolTestDTO struct {
	Passed  bool               `json:"passed"`
	Summary toolTestSummaryDTO `json:"summary"`
}

type toolTestSummaryDTO struct {
	Total  int64 `json:"total"`
	Failed int64 `json:"failed"`
}

// toolVerificationDTO is a verify_patch result: the overall verdict, the three checks it is
// made of, the test counts, and the Execution Artifact holding the runner's log, which the
// artifact content route serves. The log text itself never enters the Trace.
type toolVerificationDTO struct {
	Passed            bool               `json:"passed"`
	TestsPassed       bool               `json:"testsPassed"`
	ScanClean         bool               `json:"scanClean"`
	DependenciesClean bool               `json:"dependenciesClean"`
	Tests             toolTestSummaryDTO `json:"tests"`
	LogArtifactID     string             `json:"logArtifactId"`
	LogTruncated      bool               `json:"logTruncated"`
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
	Photo        *toolPhotoDTO
	Settings     *toolSettingsDTO
	Review       *toolReviewDTO
	Test         *toolTestDTO
	Verification *toolVerificationDTO
	Deployment   *toolDeploymentDTO
	Video        *toolVideoDTO
	Withheld     []withheldFieldDTO
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
		view.Settings = p.projectSettings(&view, decodeObject(arguments["settings"]))
	case toolReviewAsset:
		view.Review = p.projectReview(&view, result)
	case toolSandboxTest:
		view.Test = projectTest(result)
	case toolVerifyPatch:
		view.Verification = p.projectVerification(&view, result)
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

// projectSettings reads the named settings members of a generate_image call. Any other
// settings member, such as a seed, is not shown.
func (p toolViewProjector) projectSettings(view *toolAttemptView, settings map[string]json.RawMessage) *toolSettingsDTO {
	if settings == nil {
		return nil
	}
	projected := &toolSettingsDTO{}
	if prompt, truncated, ok := p.truncatedString(view, "settings.prompt", settings["prompt"], maxProjectedPromptBytes); ok {
		projected.Prompt, projected.PromptTruncated = &prompt, truncated
	}
	if aspectRatio, ok := p.boundedString(view, "settings.aspectRatio", settings["aspectRatio"]); ok {
		projected.AspectRatio = &aspectRatio
	}
	if resolution, ok := p.boundedString(view, "settings.resolution", settings["resolution"]); ok {
		projected.Resolution = &resolution
	}
	if projected.Prompt == nil && projected.AspectRatio == nil && projected.Resolution == nil {
		return nil
	}
	return projected
}

// truncatedString reads raw as a non-empty JSON string cut to at most limit bytes on a
// character boundary, reporting whether it was cut. The cut text is still subject to the
// field bound and is withheld under field when its encoding exceeds it.
func (p toolViewProjector) truncatedString(view *toolAttemptView, field string, raw json.RawMessage, limit int) (string, bool, bool) {
	var value string
	if raw == nil || json.Unmarshal(raw, &value) != nil || value == "" {
		return "", false, false
	}
	truncated := false
	if len(value) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(value[cut]) {
			cut--
		}
		value, truncated = value[:cut], true
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", false, false
	}
	if len(encoded) > p.maxFieldBytes {
		view.Withheld = append(view.Withheld, withheldFieldDTO{Field: field, Reason: withheldTooLarge})
		return "", false, false
	}
	return value, truncated, true
}

// projectReview reads the verdict and reasons of a committed review_asset result. The
// verdict must be a boolean or nothing is shown. Reasons are shown only as the bounded
// list of strings the Tool declares; a reason whose encoding exceeds the field bound
// withholds the list rather than cutting one reason or dropping it silently.
func (p toolViewProjector) projectReview(view *toolAttemptView, result map[string]json.RawMessage) *toolReviewDTO {
	var passed bool
	if result == nil || json.Unmarshal(result["passed"], &passed) != nil {
		return nil
	}
	review := &toolReviewDTO{Passed: passed}
	var reasons []string
	if raw := result["reasons"]; raw != nil && json.Unmarshal(raw, &reasons) == nil && len(reasons) <= maxProjectedReviewReasons {
		for _, reason := range reasons {
			if utf8.RuneCountInString(reason) > maxProjectedReasonRunes {
				return review
			}
			encoded, err := json.Marshal(reason)
			if err != nil {
				return review
			}
			if len(encoded) > p.maxFieldBytes {
				view.Withheld = append(view.Withheld, withheldFieldDTO{Field: "review.reasons", Reason: withheldTooLarge})
				return review
			}
		}
		review.Reasons = reasons
	}
	return review
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

// projectVerification reads the verdicts, test counts and log artifact of a committed
// verify_patch result. Every verdict must be a boolean and the artifact id a bounded
// string, or nothing is shown: a partial verification card would misstate the result.
func (p toolViewProjector) projectVerification(view *toolAttemptView, result map[string]json.RawMessage) *toolVerificationDTO {
	if result == nil {
		return nil
	}
	verification := &toolVerificationDTO{}
	for key, target := range map[string]*bool{
		"passed": &verification.Passed, "testsPassed": &verification.TestsPassed,
		"scanClean": &verification.ScanClean, "dependenciesClean": &verification.DependenciesClean,
		"logTruncated": &verification.LogTruncated,
	} {
		if result[key] == nil || json.Unmarshal(result[key], target) != nil {
			return nil
		}
	}
	tests := decodeObject(decodeObject(result["summary"])["tests"])
	total, okTotal := nonNegativeInteger(tests["total"])
	failed, okFailed := nonNegativeInteger(tests["failed"])
	if !okTotal || !okFailed {
		return nil
	}
	verification.Tests = toolTestSummaryDTO{Total: total, Failed: failed}
	artifactID, ok := p.boundedString(view, "verification.logArtifactId", result["logArtifactId"])
	if !ok {
		return nil
	}
	verification.LogArtifactID = artifactID
	return verification
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
