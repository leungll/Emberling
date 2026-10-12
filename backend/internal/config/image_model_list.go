package config

import (
	"fmt"
	"strings"
	"time"
)

// The image model group: one shared Images API endpoint, key and per-call timeout, and
// the list of image models registered behind them for the generate_image Tool.
const (
	KeyImageModels  = EnvPrefix + "IMAGE_MODELS"
	KeyImageBaseURL = EnvPrefix + "IMAGE_BASE_URL"
	KeyImageAPIKey  = EnvPrefix + "IMAGE_API_KEY"
	KeyImageTimeout = EnvPrefix + "IMAGE_TIMEOUT"
)

// DefaultImageTimeout bounds one image generation call when KeyImageTimeout is unset. A
// real image model can take minutes for one image, and the call is never resent, so the
// bound is generous; the Agent Run deadline still applies on top of it.
const DefaultImageTimeout = 5 * time.Minute

// ImageModel addresses the optional image model Adapter. Like the decision model group,
// either every required field is set and one image model is registered per list entry, or
// none is and only the deterministic mock image model exists; a partial group is a
// startup error. The base URL, key and timeout are shared by every image model.
type ImageModel struct {
	BaseURL string
	APIKey  Secret
	Models  []ImageModelEntry
	// Timeout bounds one image generation call. It is DefaultImageTimeout unless set, and
	// the caller's context, such as the Agent Run deadline, may end the call sooner.
	Timeout time.Duration
}

// ImageModelEntry is one registered image model. It has the same shape and rules as a
// decision model entry, and image and decision Model IDs share one namespace.
type ImageModelEntry = OpenAIModelEntry

// Enabled reports whether the image model Adapter is configured.
func (m ImageModel) Enabled() bool {
	return m.BaseURL != "" || !m.APIKey.IsZero() || len(m.Models) > 0
}

// imageModel reads the optional image model group. When none of its required keys is set
// the group is disabled; once any is set, every required key is.
func (l *loader) imageModel() ImageModel {
	enabled := false
	for _, key := range []string{KeyImageBaseURL, KeyImageAPIKey, KeyImageModels} {
		if strings.TrimSpace(l.env(key)) != "" {
			enabled = true
		}
	}
	timeout := l.optionalDuration(KeyImageTimeout)
	if !enabled {
		if timeout > 0 {
			l.fail(KeyImageTimeout, "set only together with "+KeyImageBaseURL)
		}
		return ImageModel{}
	}
	if timeout == 0 {
		timeout = DefaultImageTimeout
	}
	return ImageModel{
		BaseURL: l.requiredProviderURL(KeyImageBaseURL),
		APIKey:  Secret{value: l.required(KeyImageAPIKey)},
		Models:  l.modelList(KeyImageModels),
		Timeout: timeout,
	}
}

// missingKeys lists the required keys of an enabled group that are empty.
func (m ImageModel) missingKeys() []string {
	if !m.Enabled() {
		return nil
	}
	var missing []string
	for _, item := range []struct {
		key   string
		empty bool
	}{
		{KeyImageBaseURL, m.BaseURL == ""},
		{KeyImageAPIKey, m.APIKey.IsZero()},
		{KeyImageModels, len(m.Models) == 0},
	} {
		if item.empty {
			missing = append(missing, item.key)
		}
	}
	return missing
}

// validate checks the timeout and the model list of an enabled group, and that no image
// Model ID repeats a decision Model ID: both kinds of model are resolved through one
// Model Registry, so an ID may name only one of them.
func (m ImageModel) validate(decision OpenAIModel) error {
	if !m.Enabled() {
		if m.Timeout != 0 {
			return &InvalidConfigError{Key: KeyImageTimeout, Reason: "set only together with " + KeyImageBaseURL}
		}
		return nil
	}
	if m.Timeout <= 0 {
		return &InvalidConfigError{Key: KeyImageTimeout, Reason: "must be a positive duration"}
	}
	problems := validateModelList(m.Models)
	decisionIndex := make(map[string]int, len(decision.Models))
	for i, model := range decision.Models {
		decisionIndex[model.ID] = i
	}
	for i, model := range m.Models {
		if first, taken := decisionIndex[model.ID]; taken && model.ID != "" {
			problems = append(problems, fmt.Sprintf("entry %d: id duplicates %s entry %d", i, KeyModels, first))
		}
	}
	if len(problems) > 0 {
		return &InvalidConfigError{Key: KeyImageModels, Reason: strings.Join(problems, "; ")}
	}
	return nil
}
