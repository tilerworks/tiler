package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jiangxin/tiler/internal/image"
)

const imageListWidth = 80

func writeVersionImageTable(w io.Writer, views []versionImageView) error {
	return writeVersionImageTableAt(w, views, time.Now())
}

func writeVersionImageTableAt(w io.Writer, views []versionImageView, now time.Time) error {
	if len(views) == 0 {
		_, err := fmt.Fprintln(w, "No images found.")
		return err
	}
	header := []string{"IMAGE", "SIZE", "CREATED", "LAST ACTIVITY", "DESCRIPTION"}
	widths := lengths(header)
	rows := make([][][]string, len(views))
	collides := false
	for i, view := range views {
		var collision bool
		rows[i], collision = versionImageRows(view, now)
		collides = collides || collision
		for _, row := range rows[i] {
			for j := 0; j < len(row)-1; j++ {
				widths[j] = max(widths[j], displayWidth(row[j]))
			}
		}
	}
	// Reserve the description header's width even for unusually deep trees.
	widths[0] = min(widths[0], imageListWidth-10-widths[1]-widths[2]-widths[3]-widths[4])
	widths[4] = imageListWidth - 10 - widths[0] - widths[1] - widths[2] - widths[3]
	for i, view := range views {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w, imageListTitle(view)); err != nil {
			return err
		}
		if view.Latest == nil {
			if _, err := fmt.Fprintln(w, "  No image versions."); err != nil {
				return err
			}
			continue
		}
		if err := writeVersionImageRow(w, header, widths); err != nil {
			return err
		}
		for _, row := range rows[i] {
			row[0] = compactImageTree(row[0], widths[0])
			if err := writeVersionImageRow(w, row, widths); err != nil {
				return err
			}
		}
	}
	if collides {
		_, err := fmt.Fprintln(w, "Short IDs collide; use images list --json for full IDs.")
		return err
	}
	return nil
}

func imageListTitle(view versionImageView) string {
	latest := "-"
	if view.Latest != nil {
		latest = listID(view.Latest.ID)
	}
	suffix := fmt.Sprintf(" · %d versions · latest: %s", len(view.Images), latest)
	name := truncateDisplay(terminalText(view.Name), imageListWidth-3-displayWidth(suffix))
	return "📦 " + name + suffix
}

func versionImageRows(view versionImageView, now time.Time) ([][]string, bool) {
	lastChild := make(map[string]string, len(view.Images))
	for _, node := range view.Images {
		lastChild[node.Parent] = node.ID
	}
	prefixes := make(map[string]string, len(view.Images))
	rows := make([][]string, 0, len(view.Images))
	shortIDs := make(map[string]bool, len(view.Images))
	collides := false
	for _, node := range view.Images {
		short := listID(node.ID)
		collides = collides || shortIDs[short]
		shortIDs[short] = true
		prefix, connector := prefixes[node.Parent], "├─ "
		prefixes[node.ID] = prefix + "│  "
		if lastChild[node.Parent] == node.ID {
			connector = "└─ "
			prefixes[node.ID] = prefix + "   "
		}
		id := prefix + connector + short
		if view.Latest != nil && view.Latest.ID == node.ID {
			id += " ★"
		}
		description := terminalText(image.DescriptionSummary(node.Description))
		if description == "" {
			description = "-"
		}
		last := formatImageListTime(view.Activity[node.ID].At, now)
		if view.Activity[node.ID].Unknown {
			last = "unknown"
		}
		rows = append(rows, []string{id, formatSize(node.Size), formatImageListTime(node.CreatedAt, now), last, description})
	}
	return rows, collides
}

// compactImageTree elides only old ancestor guides, never the ID or latest mark.
func compactImageTree(text string, width int) string {
	if displayWidth(text) <= width {
		return text
	}
	cells := displayCells(text)
	start, used := len(cells), 0
	for start > 0 && used+cells[start-1].width <= width-2 {
		start--
		used += cells[start].width
	}
	var suffix strings.Builder
	for _, cell := range cells[start:] {
		suffix.WriteString(cell.text)
	}
	return "… " + suffix.String()
}

func writeVersionImageRow(w io.Writer, row []string, widths []int) error {
	var line strings.Builder
	line.WriteString("  ")
	for i, cell := range row[:len(row)-1] {
		padding := strings.Repeat(" ", max(0, widths[i]-displayWidth(cell)))
		if i == 1 {
			line.WriteString(padding + cell)
		} else {
			line.WriteString(cell + padding)
		}
		line.WriteString("  ")
	}
	prefix := line.String()
	for i, description := range wrapDescription(row[len(row)-1], widths[len(widths)-1]) {
		indent := prefix
		if i > 0 {
			indent = strings.Repeat(" ", displayWidth(prefix))
		}
		if _, err := fmt.Fprintln(w, indent+description); err != nil {
			return err
		}
	}
	return nil
}
