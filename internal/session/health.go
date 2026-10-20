package session

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

// ErrHealthUnavailable means the real mount health probe needs
// Linux. Injected seams keep the lifecycle logic testable on any
// platform.
var ErrHealthUnavailable = errors.New(
	"session: mount health probing requires linux")

// Healthy applies the shared two-condition health check to one
// mountpoint: a mount table entry exists at the point or the point
// stats as a mount, and the directory still answers reads. It tells
// a healthy view from a stale one, whether listed-but-dead or
// vanished from the table. The recovery decision of Open and the
// stale fallback of the reverse lookup share it. The real probes are
// Linux-only; other platforms report ErrHealthUnavailable.
func Healthy(table []mountinfo.Mount, point string) (bool, error) {
	return healthy(table, point)
}

// classify applies the two-condition rule to probe outcomes: the
// point must be a mount somewhere (table entry or stat) and must
// still answer reads. Pure, so the classification table is testable
// without real mounts.
func classify(listed, live, readable bool) bool {
	return (listed || live) && readable
}

// listed reports a mount table entry at the exact point.
func listed(table []mountinfo.Mount, point string) bool {
	for _, m := range table {
		if m.Point == point {
			return true
		}
	}
	return false
}

// CleanStale tears one stale point down before a rebuild, the
// recovery rule of Open: an unconditional fusermount3 -u,
// escalating to the lazy -uz. A point that carries no mount
// afterwards — including one that never carried any — cleans up as
// a success, so recovery never fails on an already-plain directory.
func CleanStale(point string) error {
	if point == "" {
		return nil
	}
	err := runTool("fusermount3", "-u", point)
	if err == nil {
		return nil
	}
	lazyErr := runTool("fusermount3", "-uz", point)
	if lazyErr == nil {
		return nil
	}
	if stillAttached(point) {
		return fmt.Errorf("session: cannot clean %s: %w (lazy: %v)",
			point, err, lazyErr)
	}
	return nil
}

// runTool executes one external tool. A non-zero exit carries the
// trimmed combined output.
func runTool(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, bytes.TrimSpace(out))
	}
	return nil
}
