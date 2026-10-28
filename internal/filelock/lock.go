// Package filelock shares advisory lock mechanics across library and session locks.
package filelock

import "errors"

var (
	// ErrHeld reports contention during a non-blocking attempt.
	ErrHeld = errors.New("lock is held")
	// ErrTimeout reports contention beyond a positive timeout.
	ErrTimeout = errors.New("lock timeout")
	// ErrUnavailable reports platforms without advisory locking.
	ErrUnavailable = errors.New("advisory locking requires linux or darwin")
)
