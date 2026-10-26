//go:build linux

package benchmark

import "syscall"

func platformSync() {
	syscall.Sync()
}
