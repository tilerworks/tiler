package image

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ErrInvalidDescription identifies malformed image description text.
var ErrInvalidDescription = errors.New("invalid image description")

// NormalizeDescription preserves text apart from CRLF and trailing newlines.
// An empty result clears the description; other results need a nonblank first line.
func NormalizeDescription(text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("%w: text must be valid UTF-8", ErrInvalidDescription)
	}
	if strings.ContainsRune(text, '\x00') {
		return "", fmt.Errorf("%w: text must not contain NUL", ErrInvalidDescription)
	}
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if text != "" && strings.TrimSpace(DescriptionSummary(text)) == "" {
		return "", fmt.Errorf("%w: first line must not be blank", ErrInvalidDescription)
	}
	return text, nil
}

// DescriptionFromMessages joins repeated message arguments with one empty line.
func DescriptionFromMessages(messages []string) (string, error) {
	return NormalizeDescription(strings.Join(messages, "\n\n"))
}

// DescriptionSummary returns the first physical line of normalized text.
// Presentation layers are responsible for escaping terminal control characters.
func DescriptionSummary(description string) string {
	first, _, _ := strings.Cut(description, "\n")
	return first
}
