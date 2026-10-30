package image

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// NameSelector matches complete image names; wildcard characters cross slashes.
// Its zero value selects every name.
type NameSelector struct {
	patterns []string
	matches  []*regexp.Regexp
}

// NewNameSelector validates all patterns before any library scan can begin.
func NewNameSelector(patterns []string) (NameSelector, error) {
	s := NameSelector{patterns: slices.Clone(patterns)}
	for _, pattern := range patterns {
		if _, err := path.Match(pattern, ""); err != nil {
			return NameSelector{}, fmt.Errorf("invalid image pattern %q: %w", pattern, err)
		}
		re, err := compileNameGlob(pattern)
		if err != nil {
			return NameSelector{}, fmt.Errorf("invalid image pattern %q: %w", pattern, err)
		}
		s.matches = append(s.matches, re)
	}
	return s, nil
}

// Select returns sorted unique names and unmatched patterns in argument order.
// An empty result is non-nil, distinguishing it from an unrestricted name set.
func (s NameSelector) Select(names []string) (selected, unmatched []string) {
	selected = make([]string, 0)
	matched := make([]bool, len(s.matches))
	for _, name := range names {
		keep := len(s.matches) == 0
		for i, re := range s.matches {
			if re.MatchString(name) {
				keep, matched[i] = true, true
			}
		}
		if keep {
			selected = append(selected, name)
		}
	}
	slices.Sort(selected)
	selected = slices.Compact(selected)
	for i, ok := range matched {
		if !ok {
			unmatched = append(unmatched, s.patterns[i])
		}
	}
	return selected, unmatched
}

// compileNameGlob translates validated path glob syntax without path boundaries.
func compileNameGlob(pattern string) (*regexp.Regexp, error) {
	var out strings.Builder
	out.WriteString("(?s)^(")
	runes := []rune(pattern)
	for i := 0; i < len(runes); i++ {
		switch r := runes[i]; r {
		case '*':
			out.WriteString(".*")
		case '?':
			out.WriteByte('.')
		case '\\':
			i++
			out.WriteString(regexp.QuoteMeta(string(runes[i])))
		case '[':
			out.WriteByte('[')
			i++
			if runes[i] == '^' {
				out.WriteByte('^')
				i++
			}
			for ; runes[i] != ']'; i++ {
				if runes[i] == '-' {
					out.WriteByte('-')
					continue
				}
				if runes[i] == '\\' {
					i++
				}
				_, _ = fmt.Fprintf(&out, `\x{%x}`, runes[i])
			}
			out.WriteByte(']')
		default:
			out.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	out.WriteString(")$")
	return regexp.Compile(out.String())
}
