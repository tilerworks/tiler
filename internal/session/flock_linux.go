//go:build linux

package session

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// lockPollInterval is the pause between non-blocking lock attempts
// while a timed lock waits for its deadline.
const lockPollInterval = 20 * time.Millisecond

// lockSession takes the exclusive advisory lock of one session
// through a non-blocking flock. A positive timeout polls until the
// deadline and reports ErrLockTimeout when it expires; a zero or
// negative timeout makes a single attempt and reports ErrLockHeld
// when the lock is busy, so scan callers skip this round instead of
// blocking. The returned closure releases the lock and closes the
// lock file.
func (s Store) lockSession(name string, timeout time.Duration) (func(), error) {
	file, err := s.openLock(name)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return releaseLock(file), nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = file.Close()
			return nil, fmt.Errorf("session: lock %s: %w", name, err)
		}
		if !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf(
				"session: lock %s: %w", name, lockBusyErr(timeout))
		}
		time.Sleep(lockPollInterval)
	}
}

// tryLockSession takes the exclusive lock without blocking. A lock
// another process holds reports ErrLockHeld.
func (s Store) tryLockSession(name string) (func(), error) {
	return s.lockSession(name, 0)
}

// lockBusyErr names the refusal: a timed lock waited out its
// deadline, a non-blocking attempt was simply held.
func lockBusyErr(timeout time.Duration) error {
	if timeout > 0 {
		return ErrLockTimeout
	}
	return ErrLockHeld
}

// openLock creates or opens the lock file beside the record.
func (s Store) openLock(name string) (*os.File, error) {
	path, err := s.lockPath(name)
	if err != nil {
		return nil, err
	}
	if err := s.ensureDir(name); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	return file, nil
}

// releaseLock unlocks and closes one held lock file.
func releaseLock(file *os.File) func() {
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}
}
