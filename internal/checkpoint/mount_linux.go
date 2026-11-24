//go:build linux

package checkpoint

import (
	"fmt"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

// checkTreeMounts detects existing mount points, including bind mounts.
func checkTreeMounts(root string) error {
	mounts, err := mountinfo.Load()
	if err != nil {
		return err
	}
	return checkTreeMountPoints(root, mounts)
}

func checkTreeMountPoints(root string, mounts []mountinfo.Mount) error {
	for _, m := range mounts {
		if within(filepath.Clean(m.Point), root) {
			return fmt.Errorf("mount point in copy tree: %s", m.Point)
		}
	}
	return nil
}
