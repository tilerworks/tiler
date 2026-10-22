//go:build !unix

package image

import "math"

// probeSpaceOS cannot call Statfs on this platform. It reports
// unbounded free space so the build still runs; product builds are
// Linux and macOS, where space_unix.go performs the real check.
func probeSpaceOS(path string) (spaceInfo, error) {
	if _, err := existingPath(path); err != nil {
		return spaceInfo{}, err
	}
	return spaceInfo{avail: math.MaxInt64, dev: 1}, nil
}
