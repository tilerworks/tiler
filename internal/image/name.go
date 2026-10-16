package image

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// ErrInvalidName is a name that is not an explicit owner/repo pair.
var ErrInvalidName = errors.New("invalid image name")

// ParseName accepts exactly two non-empty segments separated by one
// slash. Segments cannot be "." or "..", and cannot contain a slash,
// whitespace, or control characters. The name is never inferred.
func ParseName(s string) (string, string, error) {
	owner, repo, found := strings.Cut(s, "/")
	if !found || strings.Contains(repo, "/") {
		return "", "", invalidName(s)
	}
	if err := checkSegment(owner); err != nil {
		return "", "", err
	}
	if err := checkSegment(repo); err != nil {
		return "", "", err
	}
	return owner, repo, nil
}

func checkSegment(seg string) error {
	if seg == "" || seg == "." || seg == ".." || strings.Contains(seg, "/") || badSegmentRune(seg) {
		return invalidName(seg)
	}
	return nil
}

func badSegmentRune(seg string) bool {
	for _, r := range seg {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

func invalidName(s string) error {
	return fmt.Errorf("%w: %q", ErrInvalidName, s)
}
