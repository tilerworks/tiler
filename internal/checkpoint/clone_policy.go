//go:build linux || darwin

package checkpoint

import (
	"errors"
	"syscall"
)

// clonePolicy belongs to one tree copy. A file-specific failure does not
// prevent later files from attempting a clone.
type clonePolicy struct {
	unavailable bool
}

func isCloneNotSupported(err error) bool {
	// Darwin assigns distinct values to these errors.
	return errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP)
}

func (p *clonePolicy) fallback(err error, confirm func() (bool, error)) (bool, error) {
	if err == nil {
		return false, nil
	}
	if errors.Is(err, syscall.ENOSYS) {
		p.unavailable = true
		return true, nil
	}
	if isCloneNotSupported(err) || errors.Is(err, syscall.ENOTTY) {
		unsupported, probeErr := confirm()
		if probeErr != nil {
			return false, probeErr
		}
		p.unavailable = unsupported
		return true, nil
	}
	return false, nil
}
