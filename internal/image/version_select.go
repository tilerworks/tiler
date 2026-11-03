package image

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrVersionNotFound identifies a valid selector with no stored version.
var ErrVersionNotFound = errors.New("image version not found")

// ErrInvalidHashPrefix identifies malformed explicitly selected version hashes.
var ErrInvalidHashPrefix = errors.New("hash prefix must contain 4 to 64 hexadecimal characters")

// AmbiguousVersionError lists full candidate IDs for a non-unique selector.
type AmbiguousVersionError struct {
	Selector   string
	Candidates []string
}

func (e *AmbiguousVersionError) Error() string {
	return fmt.Sprintf("image selector %q: ambiguous hash prefix\n  %s\nUse a longer hash prefix.",
		e.Selector, strings.Join(e.Candidates, "\n  "))
}

// ParseVersionSelector parses a library reference after file-path precedence has
// been handled by the caller. An empty returned prefix requests the latest node.
func ParseVersionSelector(selector string) (name, prefix string, err error) {
	rawName, prefix, explicit := strings.Cut(selector, ":")
	segments, err := ParseName(rawName)
	if err != nil {
		return "", "", fmt.Errorf("image selector %q: %w", selector, err)
	}
	prefix = strings.ToLower(prefix)
	if explicit && (len(prefix) < 4 || !isIDRef(prefix)) {
		return "", "", fmt.Errorf("image selector %q: %w", selector, ErrInvalidHashPrefix)
	}
	return strings.Join(segments, "/"), prefix, nil
}

// SelectVersion resolves a scoped name or name:hash into its root-to-tip stack.
// It does not resolve payload paths or change the current runtime layout.
func (m VersionManifest) SelectVersion(selector string) ([]ImageMeta, error) {
	name, prefix, err := ParseVersionSelector(selector)
	if err != nil {
		return nil, err
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if name != m.Name {
		return nil, fmt.Errorf("image selector %q: name does not match repository %q", selector, m.Name)
	}
	forest, err := NewForest(m.Images)
	if err != nil {
		return nil, err
	}
	if prefix == "" {
		latest, ok := forest.Latest()
		if !ok {
			return nil, fmt.Errorf("image %q: no image versions", name)
		}
		return forest.Stack(latest.ID)
	}
	return selectVersionPrefix(forest, selector, prefix)
}

func selectVersionPrefix(forest Forest, selector, prefix string) ([]ImageMeta, error) {
	var matches []string
	for id := range forest.nodes {
		if strings.HasPrefix(id, prefix) {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("image selector %q: no matching image: %w; run 'tiler images show %s' to see available versions", selector, ErrVersionNotFound, strings.SplitN(selector, ":", 2)[0])
	case 1:
		return forest.Stack(matches[0])
	default:
		slices.Sort(matches)
		return nil, &AmbiguousVersionError{Selector: selector, Candidates: matches}
	}
}
