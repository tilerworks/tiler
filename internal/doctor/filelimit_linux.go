//go:build linux

package doctor

import (
	"errors"
	"syscall"
)

// isSizeLimitErr reports whether err is one of the errno classes a
// filesystem uses to refuse extending a sparse file: EFBIG (file too
// big), EINVAL (extent not representable), or ENOSPC (the size cannot
// be reserved).
func isSizeLimitErr(err error) bool {
	return errors.Is(err, syscall.EFBIG) ||
		errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.ENOSPC)
}
