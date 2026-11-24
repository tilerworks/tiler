//go:build darwin

package checkpoint

import (
	"fmt"
	"path/filepath"
	"syscall"
)

// darwinMountPoints returns the kernel's current mount points. The snapshot
// is advisory: another process can still change mounts after it is read.
func darwinMountPoints() ([]string, error) {
	stats, err := mountSnapshot(func(buf []syscall.Statfs_t) (int, error) {
		return syscall.Getfsstat(buf, 0)
	})
	if err != nil {
		return nil, fmt.Errorf("getfsstat: %w", err)
	}
	points := make([]string, 0, len(stats))
	for _, stat := range stats {
		point := cString(stat.Mntonname[:])
		if !filepath.IsAbs(point) {
			return nil, fmt.Errorf("getfsstat: invalid mount point %q", point)
		}
		points = append(points, filepath.Clean(point))
	}
	return points, nil
}

func cString(s []int8) string {
	b := make([]byte, 0, len(s))
	for _, c := range s {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}
