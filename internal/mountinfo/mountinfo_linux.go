//go:build linux

package mountinfo

import (
	"fmt"
	"os"
)

// procMountinfo is the kernel mount table of the current process.
const procMountinfo = "/proc/self/mountinfo"

// Load reads and parses the host mount table, then resolves the
// overlay directories of anonymous fuse-overlayfs entries through
// their daemons. A malformed line reports its line number.
func Load() ([]Mount, error) {
	file, err := os.Open(procMountinfo)
	if err != nil {
		return nil, fmt.Errorf("mountinfo: %w", err)
	}
	defer func() { _ = file.Close() }()
	mounts, err := Parse(file)
	if err != nil {
		return nil, err
	}
	enrichOverlays(mounts, procRoot)
	return mounts, nil
}
