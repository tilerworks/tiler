//go:build unix

package benchmark

import "syscall"

func dirWritable(path string) bool {
	return syscall.Access(path, 2) == nil
}
