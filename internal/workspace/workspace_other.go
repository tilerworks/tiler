//go:build !linux

package workspace

// available reports that mounting never works off Linux. The host
// mount table and /dev/fuse are never touched here.
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
