//go:build unix

package cli

import "syscall"

// hostBenchmarkAccess judges writability with access(2) W_OK only.
// Plan must not create or delete files under the volume path.
func hostBenchmarkAccess(path string) error {
	return syscall.Access(path, 2)
}
