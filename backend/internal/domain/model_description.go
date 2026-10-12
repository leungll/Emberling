package domain

import (
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"
)

// MaxModelDescriptionLength bounds ModelMetadata.Description in characters, not bytes.
const MaxModelDescriptionLength = 280

// ValidateModelDescription checks an optional model description: valid UTF-8, a single
// line without control characters, and at most MaxModelDescriptionLength characters. An
// empty description is valid. Errors never repeat the description, because a configured
// value must not reach startup errors or logs.
func ValidateModelDescription(description string) error {
	if !utf8.ValidString(description) {
		return errors.New("must be valid UTF-8")
	}
	for _, r := range description {
		if unicode.IsControl(r) {
			return errors.New("must be a single line without control characters")
		}
	}
	if utf8.RuneCountInString(description) > MaxModelDescriptionLength {
		return fmt.Errorf("must be at most %d characters", MaxModelDescriptionLength)
	}
	return nil
}
