package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// maxModelIDLength bounds a configured Model ID.
const maxModelIDLength = 64

// modelIDPattern is the Model ID format accepted from configuration: a lowercase letter
// first, then lowercase letters, digits, '.' and '-'. The Registry itself only refuses
// empty and duplicate IDs, so the format is enforced here, at startup.
var modelIDPattern = regexp.MustCompile(`^[a-z][a-z0-9.-]*$`)

// RemovedKey names a configuration key that is no longer read and the key that replaced
// it.
type RemovedKey struct {
	Key         string
	Replacement string
}

// removedKeys lists every removed key in the order it is reported.
var removedKeys = []RemovedKey{
	{Key: KeyRemovedOpenAIBaseURL, Replacement: KeyModelBaseURL},
	{Key: KeyRemovedOpenAIAPIKey, Replacement: KeyModelAPIKey},
	{Key: KeyRemovedOpenAIModel, Replacement: KeyModels},
	{Key: KeyRemovedOpenAIModels, Replacement: KeyModels},
	{Key: KeyRemovedOpenAITimeout, Replacement: KeyModelTimeout},
}

// RemovedConfigError reports removed keys that are still set. Like the other
// configuration errors it names keys only, never their values.
type RemovedConfigError struct {
	Keys []RemovedKey
}

func (e *RemovedConfigError) Error() string {
	parts := make([]string, 0, len(e.Keys))
	for _, key := range e.Keys {
		parts = append(parts, fmt.Sprintf("%s (use %s)", key.Key, key.Replacement))
	}
	return "config: removed environment variables are set: " + strings.Join(parts, ", ")
}

// checkRemovedKeys records every removed key that is set, whatever else is configured: a
// removed key is never silently ignored, so a deployment cannot lose a model unnoticed.
func (l *loader) checkRemovedKeys() {
	for _, removed := range removedKeys {
		if strings.TrimSpace(l.env(removed.Key)) != "" {
			l.removed = append(l.removed, removed)
		}
	}
}

// modelListEntry is the JSON form of one list entry.
type modelListEntry struct {
	ID          string `json:"id"`
	Model       string `json:"model"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
}

// modelList parses the model list. Problems name the zero-based entry index and the field,
// never a configured value; a JSON decoding error is not passed on because its text can
// quote the input.
func (l *loader) modelList() []OpenAIModelEntry {
	value := l.required(KeyModels)
	if value == "" {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(value), &items); err != nil {
		l.fail(KeyModels, "must be a JSON array of objects with id, model, displayName and an optional description")
		return nil
	}
	entries := make([]OpenAIModelEntry, 0, len(items))
	var problems []string
	for i, item := range items {
		decoder := json.NewDecoder(bytes.NewReader(item))
		decoder.DisallowUnknownFields()
		var parsed modelListEntry
		if err := decoder.Decode(&parsed); err != nil || bytes.TrimSpace(item)[0] != '{' {
			problems = append(problems, fmt.Sprintf("entry %d: must be an object with only the string fields id, model, displayName and description", i))
			continue
		}
		entries = append(entries, OpenAIModelEntry{
			ID:          strings.TrimSpace(parsed.ID),
			Model:       strings.TrimSpace(parsed.Model),
			DisplayName: strings.TrimSpace(parsed.DisplayName),
			Description: strings.TrimSpace(parsed.Description),
		})
	}
	if len(problems) == 0 {
		problems = validateModelList(entries)
	}
	if len(problems) > 0 {
		l.fail(KeyModels, strings.Join(problems, "; "))
		return nil
	}
	return entries
}

// validateModelList returns every problem in models: an empty list, a missing field, an
// ID outside the Model ID format, an ID that repeats an earlier entry or a description
// outside its bounds.
func validateModelList(models []OpenAIModelEntry) []string {
	if len(models) == 0 {
		return []string{"must list at least one model"}
	}
	var problems []string
	firstIndex := make(map[string]int, len(models))
	for i, model := range models {
		switch {
		case model.ID == "":
			problems = append(problems, fmt.Sprintf("entry %d: id is required", i))
		case len(model.ID) > maxModelIDLength || !modelIDPattern.MatchString(model.ID):
			problems = append(problems, fmt.Sprintf(
				"entry %d: id must start with a lowercase letter, contain only lowercase letters, digits, '.' and '-', and be at most %d characters",
				i, maxModelIDLength))
		default:
			if first, seen := firstIndex[model.ID]; seen {
				problems = append(problems, fmt.Sprintf("entry %d: id duplicates entry %d", i, first))
			} else {
				firstIndex[model.ID] = i
			}
		}
		if model.Model == "" {
			problems = append(problems, fmt.Sprintf("entry %d: model is required", i))
		}
		if model.DisplayName == "" {
			problems = append(problems, fmt.Sprintf("entry %d: displayName is required", i))
		}
		if err := domain.ValidateModelDescription(model.Description); err != nil {
			problems = append(problems, fmt.Sprintf("entry %d: description %v", i, err))
		}
	}
	return problems
}
