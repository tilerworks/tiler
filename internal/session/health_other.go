//go:build !linux

package session

import "github.com/jiangxin/tiler/internal/mountinfo"

// healthy reports that the real mount health probe needs Linux.
func healthy([]mountinfo.Mount, string) (bool, error) {
	return false, ErrHealthUnavailable
}

// liveMount is unreachable without the real probe.
func liveMount(string) bool {
	return false
}

// stillAttached is unreachable without the real probe, so CleanStale
// degrades to a no-op off Linux.
func stillAttached(string) bool {
	return false
}
