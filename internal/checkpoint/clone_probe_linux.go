//go:build linux

package checkpoint

import (
	"errors"
	"os"
	"syscall"
)

func probeCloneDestination(dir, source *os.File, name string, clone func(*os.File, *os.File, string) error) (bool, error) {
	dst, err := openProbeFile(dir, name)
	if err != nil {
		return false, nil
	}
	cloneErr := clone(dst, source, name)
	cleanupErr := removeProbeFile(dir, name, dst)
	_ = dst.Close()
	if cleanupErr != nil {
		return false, cleanupErr
	}
	return errors.Is(cloneErr, syscall.EOPNOTSUPP) || errors.Is(cloneErr, syscall.ENOTTY) || errors.Is(cloneErr, syscall.ENOSYS), nil
}
