//go:build !linux

package session

import (
	"fmt"
	"time"
)

// lockSession reports that advisory locking needs Linux. The rest of
// the package compiles and its record logic stays testable on any
// platform.
func (s Store) lockSession(string, time.Duration) (func(), error) {
	return nil, fmt.Errorf("session: %w", ErrLockUnavailable)
}

// tryLockSession reports that advisory locking needs Linux.
func (s Store) tryLockSession(string) (func(), error) {
	return nil, fmt.Errorf("session: %w", ErrLockUnavailable)
}
