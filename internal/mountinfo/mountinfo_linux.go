//go:build linux

package mountinfo

import (
	"fmt"
	"os"
)

// procMountinfo is the kernel mount table of the current process.
const procMountinfo = "/proc/self/mountinfo"

// Load reads and parses the host mount table. A malformed line
// reports its line number.
func Load() ([]Mount, error) {
	file, err := os.Open(procMountinfo)
	if err != nil {
		return nil, fmt.Errorf("mountinfo: %w", err)
	}
	defer func() { _ = file.Close() }()
	return Parse(file)
}
