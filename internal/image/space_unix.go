//go:build unix

package image

import (
	"math"
	"syscall"
)

// probeSpaceOS reads free space with Statfs. Bavail is the blocks a
// non-root caller may still write; that matches ordinary tiler runs.
// Dev groups paths that share one volume so same-volume writes add.
func probeSpaceOS(path string) (spaceInfo, error) {
	dir, err := existingPath(path)
	if err != nil {
		return spaceInfo{}, err
	}
	var fsstat syscall.Statfs_t
	if err := syscall.Statfs(dir, &fsstat); err != nil {
		return spaceInfo{}, err
	}
	var st syscall.Stat_t
	if err := syscall.Stat(dir, &st); err != nil {
		return spaceInfo{}, err
	}
	return spaceInfo{avail: freeBytes(fsstat), dev: uint64(st.Dev)}, nil
}

func freeBytes(st syscall.Statfs_t) int64 {
	bsize := uint64(st.Bsize)
	if bsize == 0 || st.Bavail == 0 {
		return 0
	}
	if st.Bavail > math.MaxUint64/bsize {
		return math.MaxInt64
	}
	n := st.Bavail * bsize
	if n > uint64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(n)
}
