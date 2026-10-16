//go:build linux

package workspace

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// available checks the FUSE device this platform mounts through.
func available() error {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		return fmt.Errorf("%w: /dev/fuse: %v", ErrUnavailable, err)
	}
	return nil
}

// devIno reports the device and inode of a directory.
func devIno(dir string) (uint64, uint64, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return 0, 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, errors.New("workspace: cannot read device and inode")
	}
	return st.Dev, st.Ino, nil
}
