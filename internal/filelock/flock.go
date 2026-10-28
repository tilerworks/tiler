//go:build linux || darwin

package filelock

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// Acquire locks an open file exclusively; the caller owns the file's lifetime.
// A nonpositive timeout makes one attempt, while positive waits are bounded.
func Acquire(file *os.File, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil || !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		if !time.Now().Before(deadline) {
			if timeout > 0 {
				return ErrTimeout
			}
			return ErrHeld
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Release unlocks an open file without closing it.
func Release(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
