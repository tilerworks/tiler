package session

import (
	"strings"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

// tableActivity uses mount identity and runtime ownership, independently of
// readability and historical action status. Files on disk are not evidence.
func (e *Engine) tableActivity(view View, table []mountinfo.Mount) mountEvidence {
	evidence := mountEvidence{Available: true}
	for _, entry := range table {
		if entry.FSType != "overlay" && entry.FSType != "fuse" && !strings.HasPrefix(entry.FSType, "fuse.") {
			continue
		}
		internal := e.activityRuntimeOwner(entry.Point) == view.Name
		runtimeOwned := e.activityRuntimeOwner(viewRuntimeDir(entry)) == view.Name
		if internal || runtimeOwned {
			evidence.InternalMount = true
		}
		for _, mount := range view.Mounts {
			if !SameMountPoint(entry.Point, mount.Mountpoint) || internal {
				continue
			}
			if runtimeOwned {
				evidence.UserMount = true
			}
			for _, layer := range mount.Layers {
				if layer.Available && SameMountPoint(layer.Mountpoint, entry.Point) {
					evidence.UserMount = true
				}
			}
		}
	}
	return evidence
}

func (e *Engine) activityRuntimeOwner(path string) string {
	if e.SessionsDir == "" || path == "" {
		return ""
	}
	root, err := canonicalQueryPath(e.SessionsDir)
	if err != nil {
		return ""
	}
	path, err = canonicalQueryPath(path)
	if err != nil {
		return ""
	}
	name, _, _ := sessionRuntimeOf(root, path)
	return name
}
