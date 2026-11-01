package cli

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// imageSummaryRow counts each version's own payload once, across all branches.
func imageSummaryRow(view versionImageView, now time.Time) ([]string, error) {
	var total int64
	var latest time.Time
	unknown := false
	for _, node := range view.Images {
		if node.Size > math.MaxInt64-total {
			return nil, fmt.Errorf("images list: total payload size overflows for %s", view.Name)
		}
		total += node.Size
		activity := view.Activity[node.ID]
		unknown = unknown || activity.Unknown
		if activity.At.After(latest) {
			latest = activity.At
		}
	}
	last := formatImageListTime(latest, now)
	if unknown {
		last = "unknown"
	}
	return []string{terminalText(view.Name), strconv.Itoa(len(view.Images)), formatSize(total), last}, nil
}

func writeImageSummaryAt(w io.Writer, views []versionImageView, now time.Time, width int) error {
	if len(views) == 0 {
		_, err := fmt.Fprintln(w, "No images found.")
		return err
	}
	rows := [][]string{{"IMAGE", "VERSIONS", "TOTAL SIZE", "LAST ACTIVITY"}}
	widths := lengths(rows[0])
	for _, view := range views {
		row, err := imageSummaryRow(view, now)
		if err != nil {
			return err
		}
		rows = append(rows, row)
		for i, cell := range row {
			widths[i] = max(widths[i], displayWidth(cell))
		}
	}
	widths[0] = min(widths[0], width-6-widths[1]-widths[2]-widths[3])
	for _, row := range rows {
		row[0] = truncateDisplay(row[0], widths[0])
		var line strings.Builder
		for i, cell := range row {
			padding := strings.Repeat(" ", max(0, widths[i]-displayWidth(cell)))
			if i == 1 || i == 2 {
				line.WriteString(padding + cell)
			} else {
				line.WriteString(cell + padding)
			}
			if i < len(row)-1 {
				line.WriteString("  ")
			}
		}
		if _, err := fmt.Fprintln(w, strings.TrimRight(line.String(), " ")); err != nil {
			return err
		}
	}
	return nil
}
