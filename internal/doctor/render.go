package doctor

import (
	"fmt"
	"io"
	"strings"
)

const (
	// headingConfig labels the resolved-configuration section.
	headingConfig = "📋 Config"
	// headingChecks labels the probe section.
	headingChecks = "🔧 Checks"
	// columnGap separates aligned columns. Spaces only: a tab shifts
	// with the terminal's tab stop and the columns stop lining up.
	columnGap = "  "
	// rowIndent keeps section titles and rows visually apart.
	rowIndent = "  "
	// wrapFloor is the shortest detail slice, so a very wide name
	// column still leaves room for a readable continuation.
	wrapFloor = 24
)

// Write renders the probe section: an emoji heading, then one
// space-aligned row per probe (name, status, detail). A detail that
// does not fit width wraps on spaces, and each continuation
// line is indented to the detail column.
func Write(w io.Writer, results []Result, width int) error {
	if _, err := fmt.Fprintf(w, "%s\n\n", headingChecks); err != nil {
		return err
	}
	rows := make([][]string, len(results))
	for i, result := range results {
		rows[i] = []string{result.Name, result.Status, cleanDetail(result.Detail)}
	}
	return writeAligned(w, rows, width)
}

// ConfigSummary describes the resolved tiler configuration that doctor
// reports alongside its probes: the config file location and the core
// runtime directories. It is rendered as its own section so the probe
// table stays name/status/detail.
type ConfigSummary struct {
	Path        string `json:"path"`
	ImagesDir   string `json:"images_dir"`
	SessionsDir string `json:"sessions_dir"`
	CacheDir    string `json:"cache_dir"`
	LogsDir     string `json:"logs_dir"`
	LogLevel    string `json:"log_level"`
}

// WriteConfig renders the config summary under its own heading, one
// space-aligned key/value row per field, then a blank line before the
// probe section. It precedes the probes so the effective configuration
// is always visible, and never affects the doctor verdict.
func WriteConfig(w io.Writer, s ConfigSummary, width int) error {
	if _, err := fmt.Fprintf(w, "%s\n\n", headingConfig); err != nil {
		return err
	}
	rows := [][]string{
		{"path", cleanDetail(s.Path)},
		{"images_dir", cleanDetail(s.ImagesDir)},
		{"sessions_dir", cleanDetail(s.SessionsDir)},
		{"cache_dir", cleanDetail(s.CacheDir)},
		{"logs_dir", cleanDetail(s.LogsDir)},
		{"log_level", cleanDetail(s.LogLevel)},
	}
	if err := writeAligned(w, rows, width); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w)
	return err
}

// writeAligned prints rows using spaces. Every column but the last is
// padded to the widest value in that column; the last column is the
// body and wraps to stay within the requested width.
func writeAligned(w io.Writer, rows [][]string, width int) error {
	if len(rows) == 0 {
		return nil
	}
	cols := len(rows[0])
	widths := make([]int, cols-1)
	for _, row := range rows {
		for i := 0; i < cols-1 && i < len(row); i++ {
			if n := len(row[i]); n > widths[i] {
				widths[i] = n
			}
		}
	}
	for _, row := range rows {
		var prefix strings.Builder
		prefix.WriteString(rowIndent)
		for i := 0; i < cols-1; i++ {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			prefix.WriteString(cell)
			if pad := widths[i] - len(cell); pad > 0 {
				prefix.WriteString(strings.Repeat(" ", pad))
			}
			prefix.WriteString(columnGap)
		}
		head := prefix.String()
		detail := ""
		if cols-1 < len(row) {
			detail = row[cols-1]
		}
		budget := width - len(head)
		if budget < wrapFloor {
			budget = wrapFloor
		}
		parts := wrapWords(detail, budget)
		for i, part := range parts {
			line := head + part
			if i > 0 {
				line = strings.Repeat(" ", len(head)) + part
			}
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
	}
	return nil
}

// wrapWords splits text into chunks of at most width, breaking on
// spaces. A single token longer than width is cut so the line still
// ends.
func wrapWords(text string, width int) []string {
	if text == "" {
		return []string{""}
	}
	if len(text) <= width {
		return []string{text}
	}
	var lines []string
	for len(text) > width {
		cut := strings.LastIndex(text[:width+1], " ")
		if cut <= 0 {
			cut = width
		}
		lines = append(lines, strings.TrimRight(text[:cut], " "))
		text = strings.TrimLeft(text[cut:], " ")
		if text == "" {
			return lines
		}
	}
	return append(lines, text)
}

func cleanDetail(detail string) string {
	detail = strings.ReplaceAll(detail, "\t", " ")
	detail = strings.ReplaceAll(detail, "\r", " ")
	detail = strings.ReplaceAll(detail, "\n", " ")
	return strings.TrimSpace(detail)
}
