//go:build linux

package image

import (
	"errors"
	"syscall"
)

// isSizeLimitWrite reports whether a write failure is the kernel
// refusing a file that grew past the single-file limit of the
// target storage. Only EFBIG carries that meaning on a real write;
// ENOSPC stays "out of space" and EINVAL stays "bad argument".
func isSizeLimitWrite(err error) bool {
	return errors.Is(err, syscall.EFBIG)
}
