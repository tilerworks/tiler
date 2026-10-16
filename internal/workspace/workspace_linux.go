//go:build linux

package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// liveMount reports whether point currently behaves as a mount:
// stat succeeds and the device differs from the parent directory's,
// which is how a FUSE mount shows up once its daemon attached.
func liveMount(point string) bool {
	dev, _, err := devIno(point)
	if err != nil {
		return false
	}
	parentDev, _, err := devIno(filepath.Dir(point))
	if err != nil {
		return false
	}
	return dev != parentDev
}

// answers reports whether the FUSE connection behind point still
// serves requests. stat alone is not enough: the kernel attribute
// cache keeps answering it long after the daemon died, while opening
// the point forces a request that a dead connection refuses with
// ENOTCONN. Any other answer, including a refusal or an I/O error,
// means something is still serving the mount.
func answers(point string) bool {
	file, err := os.Open(point)
	if err == nil {
		_ = file.Close()
		return true
	}
	return !errors.Is(err, syscall.ENOTCONN)
}

// fileUID is the owner of an already-statted file.
func fileUID(info os.FileInfo) uint32 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Uid
	}
	return uint32(os.Geteuid())
}
