//go:build darwin

package benchmark

import "syscall"

func platformSync() {
	_ = syscall.Sync()
}
