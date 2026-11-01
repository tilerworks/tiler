package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jiangxin/tiler/internal/session"
)

type statusTreeNode struct {
	label    string
	children []statusTreeNode
}

// writeStatusDetails renders the shared, snapshot-based session detail view.
func writeStatusDetails(w io.Writer, overview session.Overview, now time.Time, cwd string) error {
	var text strings.Builder
	ids, collision := map[string]string{}, false
	for i, view := range overview.Sessions {
		if i > 0 {
			text.WriteByte('\n')
		}
		writeStatusTitle(&text, view, now)
		for _, mount := range view.Mounts {
			marks := mount.Mode + " · " + mount.Status
			if mount.Health != "" {
				marks += " · " + mount.Health
			}
			fmt.Fprintf(&text, "\n  📁 %s  →  %s  [%s]\n", statusMountImage(mount, cwd), compactStatusPath(mount.Mountpoint, cwd), terminalText(marks))
			for _, layer := range mount.Layers {
				short := listID(layer.Version)
				collision = collision || ids[short] != "" && ids[short] != layer.Version
				ids[short] = layer.Version
			}
			writeStatusTree(&text, statusMountTree(mount, cwd), "     ")
		}
	}
	for _, orphan := range overview.Orphans {
		fmt.Fprintf(&text, "orphan %s %s %s\n", compactStatusPath(orphan.Point, cwd), terminalText(orphan.FSType), compactStatusPath(orphan.RuntimeDir, cwd))
	}
	if text.Len() == 0 {
		text.WriteString("no sessions recorded\n")
	}
	if collision {
		text.WriteString("Short IDs collide; use status --json for full IDs.\n")
	}
	_, err := io.WriteString(w, text.String())
	return err
}

func writeStatusTitle(w *strings.Builder, view session.View, now time.Time) {
	parts := []string{view.Name}
	if view.State != "" {
		parts = append(parts, view.State)
	}
	for _, mark := range []struct {
		enabled bool
		label   string
	}{{view.Current, "current"}, {view.Locked, "locked"}, {view.RemoveOnClose, "remove on close"}} {
		if mark.enabled {
			parts = append(parts, mark.label)
		}
	}
	parts = append(parts, view.ActivityReasons...)
	fmt.Fprintln(w, terminalText(strings.Join(parts, " · ")))
	if view.Locked {
		return
	}
	fmt.Fprintf(w, "  Created: %s", formatImageListTime(view.Created, now))
	if !view.Closed.IsZero() {
		fmt.Fprintf(w, " · Closed: %s", formatImageListTime(view.Closed, now))
	}
	fmt.Fprintf(w, " · Last activity: %s\n", formatSessionActivity(view.Activity, now))
}

func statusMountImage(mount session.MountView, cwd string) string {
	if mount.Name == "" {
		return compactStatusPath(mount.Image, cwd)
	}
	tip := mount.TipID
	if mount.Health == session.HealthHealthy && mount.ServedTip != "" {
		tip = mount.ServedTip
	}
	return terminalText(mount.Name + ":" + listID(tip))
}

func statusMountTree(mount session.MountView, cwd string) []statusTreeNode {
	var nodes []statusTreeNode
	for _, layer := range mount.Layers {
		point := "unavailable"
		if layer.Available {
			point = compactStatusPath(layer.Mountpoint, cwd)
		}
		nodes = append(nodes, statusTreeNode{label: fmt.Sprintf("%-11s  %s  %s", terminalText(layer.Kind), terminalText(listID(layer.Version)), point)})
	}
	if mount.OverlayUpper != "" || len(mount.OverlayLowers) > 0 {
		overlay := statusTreeNode{label: "overlay"}
		if mount.OverlayUpper != "" {
			overlay.children = append(overlay.children, statusTreeNode{label: "upper     " + compactStatusPath(mount.OverlayUpper, cwd)})
		}
		if len(mount.OverlayLowers) > 0 {
			lower := statusTreeNode{label: "lower     [highest priority first]"}
			for _, path := range mount.OverlayLowers {
				lower.children = append(lower.children, statusTreeNode{label: compactStatusPath(path, cwd)})
			}
			overlay.children = append(overlay.children, lower)
		}
		nodes = append(nodes, overlay)
	}
	if mount.Shadow != "" {
		nodes = append(nodes, statusTreeNode{label: "shadow    " + compactStatusPath(mount.Shadow, cwd)})
	}
	if mount.ServedTip != "" && mount.ServedTip != mount.TipID {
		nodes = append(nodes, statusTreeNode{label: "Desired: " + terminalText(listID(mount.TipID)) + " · Served: " + terminalText(listID(mount.ServedTip))})
	}
	if mount.Health == session.HealthStale {
		nodes = append(nodes, statusTreeNode{label: "Warning: recorded mount is no longer healthy"})
	}
	for _, diagnostic := range mount.Diagnostics {
		nodes = append(nodes, statusTreeNode{label: "Warning: " + terminalText(diagnostic)})
	}
	return nodes
}

func writeStatusTree(w *strings.Builder, nodes []statusTreeNode, prefix string) {
	for i, node := range nodes {
		connector, continuation := "├─ ", "│  "
		if i == len(nodes)-1 {
			connector, continuation = "└─ ", "   "
		}
		fmt.Fprintln(w, prefix+connector+node.label)
		writeStatusTree(w, node.children, prefix+continuation)
	}
}
