//go:build linux

package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// cloneFile asks the filesystem to share the source's data blocks.
func cloneFile(dst, src *os.File) error {
	const ficlone = 0x40049409
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, dst.Fd(), ficlone, src.Fd())
	if errno == 0 {
		return nil
	}
	if errors.Is(errno, syscall.EOPNOTSUPP) || errors.Is(errno, syscall.ENOTTY) ||
		errors.Is(errno, syscall.EXDEV) || errors.Is(errno, syscall.EINVAL) ||
		errors.Is(errno, syscall.ENOSYS) {
		return fmt.Errorf("%w: %w", errCloneUnsupported, errno)
	}
	return errno
}
