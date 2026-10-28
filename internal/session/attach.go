package session

import (
	"fmt"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

// Attached returns the mount point that still keeps one recorded session
// attached, or the empty string when the session is confidently unattached.
// A non-nil error means the host would not report its mounts; a caller must
// then keep the session instead of reading the failure as no mount.
//
// The guard is read-only and never unmounts anything. It reads the record and
// the host, never the stored health: a FUSE daemon that died leaves its kernel
// mount behind, and an action written down as unmounted can still be attached,
// so every recorded mount point counts whatever its status. Mounts below a
// recorded point count as well, and so does any mount under the session
// directory, which covers the runtime intermediate layers of a view.
func (e *Engine) Attached(rec Record) (string, error) {
	if err := ValidateName(rec.Name); err != nil {
		return "", fmt.Errorf("session: attachment: %w", err)
	}
	roots := ownerRoots(rec, e.SessionsDir)
	if e.useDiskImages() {
		images, err := e.diskImages()
		if err != nil {
			return "", err
		}
		return attachedDiskPoint(images, roots), nil
	}
	table, err := e.attachTable()
	if err != nil {
		return "", err
	}
	return attachedTablePoint(table, roots), nil
}

// attachTable snapshots the host mount table for the attachment guard. A
// missing seam loads the real table, because the guard cannot tell an
// unattached session from an unread table, and a platform that cannot read the
// table reports that error.
func (e *Engine) attachTable() ([]mountinfo.Mount, error) {
	if e.LoadTable != nil {
		return e.loadTable()
	}
	table, err := mountinfo.Load()
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	return table, nil
}

// ownerRoots lists the directories whose mounts belong to one session: every
// mount point the record wrote, whatever its status, and the session directory
// that holds its runtime layers.
func ownerRoots(rec Record, sessionsDir string) []string {
	roots := make([]string, 0, len(rec.Actions)+1)
	for _, action := range rec.Actions {
		if action.Type == TypeMount && action.Mountpoint != "" {
			roots = append(roots, action.Mountpoint)
		}
	}
	if sessionsDir != "" {
		roots = append(roots, filepath.Join(sessionsDir, rec.Name))
	}
	return roots
}

// attachedTablePoint returns the mount that keeps the session attached: a
// recorded view, a nested mount below one, or a runtime intermediate layer.
func attachedTablePoint(table []mountinfo.Mount, roots []string) string {
	var nearest nearestPoint
	for _, mount := range table {
		if mount.Point != "" && underAnyRoot(mount.Point, roots) {
			nearest.offer(mount.Point)
		}
	}
	return nearest.choice
}

// attachedDiskPoint returns the volume that keeps the session attached on a
// host that reconciles through hdiutil info: a volume at a recorded point, or
// a volume whose writable shadow lies in the session runtime.
func attachedDiskPoint(images []DiskImage, roots []string) string {
	var nearest nearestPoint
	for _, image := range images {
		if image.Point != "" && underAnyRoot(image.Point, roots) {
			nearest.offer(image.Point)
			continue
		}
		runtime := diskRuntime(image.Shadow)
		if runtime == "" || !underAnyRoot(runtime, roots) {
			continue
		}
		if image.Point != "" {
			nearest.offer(image.Point)
			continue
		}
		nearest.offer(runtime)
	}
	return nearest.choice
}

// nearestPoint picks the point a caller reports back. The shortest path is the
// outermost mount, which is the recorded view rather than a mount nested below
// it, and the lexicographic order settles two paths of equal length, so the
// answer never depends on the order of a host snapshot.
type nearestPoint struct {
	choice string
}

func (n *nearestPoint) offer(point string) {
	if point == "" {
		return
	}
	shorter := len(point) < len(n.choice)
	tie := len(point) == len(n.choice) && point < n.choice
	if n.choice == "" || shorter || tie {
		n.choice = point
	}
}

// underAnyRoot reports whether one mount point is a root or lies below one
// after symlinks are resolved on both sides.
func underAnyRoot(point string, roots []string) bool {
	resolved := resolvedMount(point)
	for _, root := range roots {
		if root != "" && underPrefix(resolved, resolvedMount(root)) {
			return true
		}
	}
	return false
}
