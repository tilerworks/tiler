//go:build linux

package checkpoint

import (
	"fmt"
	"os"

	"github.com/jiangxin/tiler/internal/mountinfo"
	"golang.org/x/sys/unix"
)

func checkpointMounts() ([]mountinfo.Mount, error) { return mountinfo.Load() }

func publishExchangeStage(_ *Parent, _ string, source, dest *os.File, name string) error {
	return linkVerifiedExchangeStage(source, dest, name)
}

// Link through the verified descriptor even if its name is replaced.
var linkVerifiedExchangeStage = func(source, dest *os.File, name string) error {
	path := fmt.Sprintf("/proc/self/fd/%d", source.Fd())
	return unix.Linkat(unix.AT_FDCWD, path, int(dest.Fd()), name, unix.AT_SYMLINK_FOLLOW)
}
