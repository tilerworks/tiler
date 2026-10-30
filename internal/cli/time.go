package cli

import "time"

// formatTextTime renders human-facing timestamps in the runtime local zone.
// Machine-readable output keeps its original format and precision.
func formatTextTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}
