package cli

import (
	"path/filepath"
	"strings"
)

// compactStatusPath compares canonical absolute and cwd-relative spellings.
// Its display-only result never exceeds 63 terminal columns.
func compactStatusPath(path, cwd string) string {
	if path == "" {
		return ""
	}
	cwd = displayCanonicalPath(cwd)
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	absolute := displayCanonicalPath(path)
	chosen := terminalText(absolute)
	if relative, err := filepath.Rel(cwd, absolute); err == nil {
		relative = terminalText(relative)
		if displayWidth(relative) < displayWidth(chosen) {
			chosen = relative
		}
	}
	if displayWidth(chosen) <= 63 {
		return chosen
	}
	parts := strings.Split(terminalText(absolute), string(filepath.Separator))
	suffix := parts[len(parts)-1]
	if displayWidth(suffix) > 61 {
		cells, used := displayCells(suffix), 0
		start := len(cells)
		for start > 0 && used+cells[start-1].width <= 61 {
			start--
			used += cells[start].width
		}
		var tail strings.Builder
		for _, cell := range cells[start:] {
			tail.WriteString(cell.text)
		}
		return "…/" + tail.String()
	}
	for i := len(parts) - 2; i >= 0 && parts[i] != ""; i-- {
		candidate := parts[i] + string(filepath.Separator) + suffix
		if displayWidth(candidate) > 61 {
			break
		}
		suffix = candidate
	}
	return "…/" + suffix
}

// displayCanonicalPath resolves existing ancestors even after a mount vanishes.
// A resolution error preserves the cleaned spelling for diagnostic display.
func displayCanonicalPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	for ancestor := absolute; ; ancestor = filepath.Dir(ancestor) {
		if resolved, err := filepath.EvalSymlinks(ancestor); err == nil {
			tail, _ := filepath.Rel(ancestor, absolute)
			return filepath.Join(resolved, tail)
		}
		if filepath.Dir(ancestor) == ancestor {
			return absolute
		}
	}
}
