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
// empty, ".", or ".."; cannot start with "." or "_"; cannot equal a
// leaf layout filename; cannot be a chain-id shaped hex string of
// length idHexLen or more; and cannot contain a slash, whitespace,
// or control characters. A result that is still absolute, escapes
// via "..", or is not owner/repo is refused. The name is never
// inferred. Failures quote the original input and name the
// owner/repo shape so command users know what to pass (for example
// build --image).
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
	if seg == "" || seg == "." || seg == ".." ||
		strings.Contains(seg, "/") || badSegmentRune(seg) ||
		seg[0] == '.' || seg[0] == '_' ||
		isReservedLeafName(seg) || isChainIDShaped(seg) {
		return invalidName(seg)
	}
	return nil
}

// isReservedLeafName reports whether seg collides with a file that
// lives at the leaf of a repository directory. There is no separate
// layout-word reserved list: underscore-prefixed layout dirs are
// already refused by the leading "_" rule.
func isReservedLeafName(seg string) bool {
	switch seg {
	case fileMeta, ImageFile, ImageDMG, fileConcatView:
		return true
	default:
		return false
	}
}

// isChainIDShaped reports whether seg looks like a chain id: at least
// idHexLen hexadecimal digits, any ASCII case. The floor matches
// idHexLen so shorter hex strings remain valid name segments.
func isChainIDShaped(seg string) bool {
	if len(seg) < idHexLen {
		return false
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if (c < '0' || c > '9') &&
			(c < 'a' || c > 'f') &&
			(c < 'A' || c > 'F') {
			return false
		}
	}
	return true
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
