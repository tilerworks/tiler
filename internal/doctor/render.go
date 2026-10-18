package doctor

import (
	"fmt"
	"io"
	"strings"
)

// Write renders one tab-separated line per probe. The detail is
// flattened so a host error cannot break the line into extra rows.
func Write(w io.Writer, results []Result) error {
	for _, result := range results {
		_, err := fmt.Fprintf(w, "%s\t%s\t%s\n", result.Name, result.Status, result.Detail)
		if err != nil {
			return err
		}
	}
	return nil
}

// ConfigSummary describes the resolved tiler configuration that doctor
// reports alongside its probes: the config file location and the core
// runtime directories. It is rendered as its own section so the probe
// table stays name/status/detail.
type ConfigSummary struct {
	Path        string `json:"path"`
	ImagesDir   string `json:"images_dir"`
	RuntimeDir  string `json:"runtime_dir"`
	SessionsDir string `json:"sessions_dir"`
	CacheDir    string `json:"cache_dir"`
	LogLevel    string `json:"log_level"`
}

// WriteConfig renders the config summary as its own section: one
// config<TAB>key<TAB>value line per field. It keeps the same
// three-column shape as the probe table so a strict TSV reader stays
// valid, precedes the probes so the effective configuration is always
// visible, and never affects the doctor verdict.
func WriteConfig(w io.Writer, s ConfigSummary) error {
	rows := []struct{ key, value string }{
		{"path", s.Path},
		{"images_dir", s.ImagesDir},
		{"runtime_dir", s.RuntimeDir},
		{"sessions_dir", s.SessionsDir},
		{"cache_dir", s.CacheDir},
		{"log_level", s.LogLevel},
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(w, "config\t%s\t%s\n", row.key, cleanDetail(row.value)); err != nil {
			return err
		}
	}
	return nil
}

func cleanDetail(detail string) string {
	detail = strings.ReplaceAll(detail, "\t", " ")
	detail = strings.ReplaceAll(detail, "\r", " ")
	detail = strings.ReplaceAll(detail, "\n", " ")
	return strings.TrimSpace(detail)
}
