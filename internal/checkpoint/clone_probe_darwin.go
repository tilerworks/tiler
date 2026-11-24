//go:build darwin

package checkpoint

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func probeCloneDestination(dir, source *os.File, name string, clone func(*os.File, *os.File, string) error) (bool, error) {
	err := clone(nil, source, name)
	if err != nil {
		// The clone operation must not leave a destination on failure.
		var st unix.Stat_t
		if statErr := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); statErr == nil {
			return false, errors.New("unidentified clone probe destination")
		} else if !errors.Is(statErr, syscall.ENOENT) {
			return false, statErr
		}
		return isCloneNotSupported(err) || errors.Is(err, syscall.ENOSYS), nil
	}
	file, openErr := openEntry(dir, name, unix.O_RDONLY)
	if openErr != nil {
		return false, openErr
	}
	cleanupErr := removeProbeFile(dir, name, file)
	_ = file.Close()
	return false, cleanupErr
}
