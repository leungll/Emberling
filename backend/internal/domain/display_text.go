package domain

import (
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"
)

// Display text is the optional, human-facing copy of a registration: what a Node does, what
// a port carries, and the short name of a Tool. Studio shows it; the Runtime never reads it,
// and a Tool's title is never part of a model request, which only sees the Tool's
// model-facing Description. Bounds are in characters, not bytes.
const (
	// MaxDisplayTitleLength bounds a display title such as PortMetadata.Title or
	// ToolMetadata.Title.
	MaxDisplayTitleLength = 64
	// MaxDisplayDescriptionLength bounds a one-sentence display description such as
	// NodeMetadata.Description or PortMetadata.Description.
	MaxDisplayDescriptionLength = 280
)

// ValidateDisplayLine checks one optional display text: valid UTF-8, a single line without
// control characters, and at most maxRunes characters. An empty text is valid, because
// every display text is optional. Errors never repeat the text, so a registration value
// cannot reach startup errors or logs through them.
func ValidateDisplayLine(text string, maxRunes int) error {
	if text == "" {
		return nil
	}
	if !utf8.ValidString(text) {
		return errors.New("must be valid UTF-8")
	}
	for _, r := range text {
		if unicode.IsControl(r) {
			return errors.New("must be a single line without control characters")
		}
	}
	if utf8.RuneCountInString(text) > maxRunes {
		return fmt.Errorf("must be at most %d characters", maxRunes)
	}
	return nil
}
