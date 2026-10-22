package workspace

import (
	"errors"
	"fmt"
	"path/filepath"
)

// UnmountRequest drives a path-based unmount.
type UnmountRequest struct {
	// Dir is the cleaned absolute mount point path to tear down.
	Dir string
	// Table is the mount table snapshot taken before teardown.
	Table []Entry
	// RuntimeDir is the session-scoped combination directory for
	// this mount (sessions/<name>/runtime/m<seq>). It marks tiler
	// transit points for lower teardown and is the residue sweep
	// root. Empty skips lower teardown and the residue sweep, which
	// keeps an already-unmounted target without a recoverable
	// combination directory idempotent.
	RuntimeDir string
	// Owned reports whether a mount source resolves inside the
	// image library. When Dir itself is not a listed mount, the
	// owned-children branch tears down only the direct child mounts
	// whose source passes it, so leftover subpath combinations come
	// down through their parent directory while foreign child
	// mounts stay untouched. Nil disables that branch.
	Owned func(source string) bool
	// Reload re-reads the mount table for the residue sweep. Nil
	// keeps the injected snapshot.
	Reload func() ([]Entry, error)
	// Run executes external tools. Nil runs them through exec.
	Run func(name string, args ...string) error
	// Hdiutil runs hdiutil and returns stdout when the view is a
	// disk image. Nil selects the platform exec. FUSE teardown
	// ignores it.
	Hdiutil func(argv []string, consErr error) ([]byte, error)
}

// UnmountPath tears down whatever the path holds and reports how
// many mount points it removed; zero means the target was not
// mounted. The target point comes down first, then the overlay
// lowers it lists under the combination directory in reverse
// lowerdir order; a target that is not itself a listed mount takes
// the owned-children branch instead, tearing down the owned direct
// child mounts in reverse order. Then a residue sweep of the
// combination directory catches lowers the table no longer connects
// to the target. An unmounted or already-vanished target succeeds
// silently, and runtime upper and work data is never deleted so a
// remount of the same session action reuses it.
func UnmountPath(req UnmountRequest) (int, error) {
	run := req.Run
	if run == nil {
		run = runTool
	}
	return unmountPath(req, run, hooks{})
}

func unmountPath(req UnmountRequest, run runFunc, seam hooks) (int, error) {
	if req.Dir == "" {
		return 0, errors.New("workspace: dir is required")
	}
	removed := 0
	entry, listed := findEntry(req.Table, req.Dir)
	if listed && dmgMountEntry(entry) {
		if err := detachDMGPoint(req.Dir, req.Hdiutil); err != nil {
			return 0, err
		}
		return 1, nil
	}
	if listed {
		n, err := teardownListed(req, entry, run, seam)
		if err != nil {
			return removed, err
		}
		removed += n
	} else {
		n, err := teardownOwnedChildren(req, run, seam)
		if err != nil {
			return removed, err
		}
		removed += n
	}
	n, err := sweepRuntime(req, run, seam)
	if err != nil {
		return removed, err
	}
	return removed + n, nil
}

// teardownOwnedChildren is the owned-children branch: the parent
// directory itself is not a listed mount, so the combination lives
// in its direct children. Every child entry whose top-most source
// passes the Owned predicate comes down in reverse table order —
// the reverse of the mount order — and a child that already reads
// as a plain directory removes nothing. Foreign child mounts are
// never touched, and without a predicate there is nothing to own.
func teardownOwnedChildren(req UnmountRequest, run runFunc, seam hooks) (int, error) {
	if req.Owned == nil {
		return 0, nil
	}
	parent := filepath.Clean(req.Dir)
	var order []string
	latest := map[string]Entry{}
	for _, entry := range req.Table {
		if entry.Point == parent || filepath.Dir(entry.Point) != parent {
			continue
		}
		if _, seen := latest[entry.Point]; !seen {
			order = append(order, entry.Point)
		}
		latest[entry.Point] = entry
	}
	removed := 0
	for i := len(order) - 1; i >= 0; i-- {
		point := order[i]
		if !req.Owned(latest[point].Source) {
			continue
		}
		n, err := removePoint(point, run, seam)
		if err != nil {
			return removed, err
		}
		removed += n
	}
	return removed, nil
}

// teardownListed removes the target point and then the overlay
// lowers it lists under the runtime prefix, in reverse lowerdir
// order. A busy point with a cwd or open fds and a stale daemon
// take the same path: fusermount3 -u first, escalating to the lazy
// -uz, and a point that still stands afterwards is a residue error.
// A foreign mount gets this single-point teardown only; ownership
// checks are the FUSE tool's business.
func teardownListed(req UnmountRequest, entry Entry, run runFunc, seam hooks) (int, error) {
	removed, err := removePoint(req.Dir, run, seam)
	if err != nil {
		return 0, err
	}
	if req.RuntimeDir == "" {
		return removed, nil
	}
	for i := len(entry.Lowers) - 1; i >= 0; i-- {
		lower := entry.Lowers[i]
		if lower == req.RuntimeDir || !underPrefix(lower, req.RuntimeDir) {
			continue
		}
		n, err := removePoint(lower, run, seam)
		if err != nil {
			return removed, err
		}
		removed += n
	}
	return removed, nil
}

// sweepRuntime is the residue fallback for the shape where the
// overlay entry is already gone but squashfuse lowers remain
// listed. Every mount still listed under the caller-supplied
// combination directory comes down in reverse order. An empty
// combination directory sweeps nothing, which keeps an absent
// target without recoverable session context idempotent.
func sweepRuntime(req UnmountRequest, run runFunc, seam hooks) (int, error) {
	if req.RuntimeDir == "" {
		return 0, nil
	}
	table := req.Table
	if req.Reload != nil {
		var err error
		if table, err = req.Reload(); err != nil {
			return 0, fmt.Errorf("workspace: %w", err)
		}
	}
	runtime := filepath.Clean(req.RuntimeDir)
	var points []string
	for _, entry := range table {
		if entry.Point != runtime && underPrefix(entry.Point, runtime) {
			points = append(points, entry.Point)
		}
	}
	removed := 0
	for i := len(points) - 1; i >= 0; i-- {
		n, err := removePoint(points[i], run, seam)
		if err != nil {
			return removed, err
		}
		removed += n
	}
	return removed, nil
}

// CombinationRuntime is the session-scoped combination directory a
// table entry's mount lives in: the parent of its upper for a
// writable view, or of its newest lower for a read-only chain.
func CombinationRuntime(entry Entry) string {
	switch {
	case entry.Upper != "" && filepath.Base(entry.Upper) == "upper":
		return filepath.Dir(entry.Upper)
	case entry.Upper == "" && len(entry.Lowers) > 0:
		return filepath.Dir(entry.Lowers[0])
	}
	return ""
}

// removePoint tears one point down when something is still mounted
// there and reports whether it removed a mount. A point that
// already reads as a plain directory is a silent no-op, so a
// snapshot the kernel already caught up with removes nothing.
func removePoint(point string, run runFunc, seam hooks) (int, error) {
	if pointGone(point, seam) {
		return 0, nil
	}
	if err := cleanPoint(point, run, seam); err != nil {
		return 0, err
	}
	return 1, nil
}
