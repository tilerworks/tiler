package image

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
)

// ErrInvalidName is a name that is not an explicit owner/repo pair.
var ErrInvalidName = errors.New("invalid image name")

// ParseName accepts exactly two non-empty segments separated by one
// slash. The input is cleaned first with the same lexical rules as
// path.Clean: repeated slashes collapse, "." segments drop, and a
// ".." segment cancels the preceding segment. Cleaning uses slash
// rules on every platform. Segments of the cleaned name cannot be
// "." or "..", and cannot contain a slash, whitespace, or control
// characters. A result that is still absolute, escapes via "..", or
// is not owner/repo is refused. The name is never inferred.
// Failures quote the original input and name the owner/repo shape so
// command users know what to pass (for example build --image).
func ParseName(s string) (string, string, error) {
	owner, repo, found := strings.Cut(cleanImageName(s), "/")
	if !found || strings.Contains(repo, "/") ||
		checkSegment(owner) != nil || checkSegment(repo) != nil {
		return "", "", fmt.Errorf(
			"%w: %q; want owner/repo (two non-empty segments, no whitespace)",
			ErrInvalidName, s)
	}
	return owner, repo, nil
}

// cleanImageName applies path.Clean. Image names stay slash-separated
// on every platform, so this does not use filepath.Clean.
func cleanImageName(s string) string {
	return path.Clean(s)
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
