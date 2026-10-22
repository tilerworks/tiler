//go:build !linux && !darwin

package workspace

import "os"

// available reports that mounting never works off Linux and macOS.
// Those hosts have their own files. The mount table and /dev/fuse
// are never touched here.
func available() error {
	return ErrUnavailable
}

// devIno is unreachable because Mount refuses before it runs.
func devIno(string) (uint64, uint64, error) {
	return 0, 0, ErrUnavailable
}

// liveMount is unreachable because Mount refuses before it polls.
func liveMount(string) bool {
	return false
}

// answers is unreachable because Mount refuses before it polls.
func answers(string) bool {
	return false
}

// fileUID is unreachable because Mount refuses before it guards. It
// reports the current user so the pure owner check trivially passes.
func fileUID(os.FileInfo) uint32 {
	return uint32(os.Geteuid())
}
