//go:build !linux && !darwin

package filelock

import (
	"os"
	"time"
)

// Acquire reports platforms without advisory locking.
func Acquire(*os.File, time.Duration) error { return ErrUnavailable }

// Release reports platforms without advisory locking.
func Release(*os.File) error { return ErrUnavailable }
