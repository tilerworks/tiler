//go:build darwin

// Package testutil configures synchronization for temporary test fixtures.
package testutil

import (
	"os"
	"syscall"

	"github.com/jiangxin/tiler/internal/durable"
)

func init() {
	if os.Getenv("TILER_TEST_FULL_SYNC") == "1" {
		return
	}
	// Temporary test fixtures need filesystem synchronization, but do
	// not need File.Sync's F_FULLFSYNC hardware cache flush. Keep real
	// fsync errors and test doubles' injected failures observable.
	// Importing this helper is restricted to test files.
	durable.Sync = func(file durable.File) error {
		if f, ok := file.(*os.File); ok {
			return syscall.Fsync(int(f.Fd()))
		}
		return file.Sync()
	}
}
