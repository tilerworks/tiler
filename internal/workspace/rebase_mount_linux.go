//go:build linux

package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jiangxin/tiler/internal/dirroot"
	"github.com/jiangxin/tiler/internal/durable"
)

func rebaseTransit(runtime string) string { return filepath.Join(runtime, "rebase") }

// Old recovery views depend on both the served and desired transit mounts.
func cleanRebaseTransits(req Request, run runFunc, seam hooks) error {
	root := rebaseTransit(req.RuntimeDir)
	var points []string
	for _, entry := range req.Table {
		if underPrefix(entry.Point, root) {
			points = append(points, entry.Point)
		}
	}
	// Views hold the lower mounts busy regardless of mount-table ordering.
	slices.SortFunc(points, func(a, b string) int {
		rank := func(p string) int {
			switch {
			case strings.HasPrefix(filepath.Base(p), "concat-"):
				return 2
			case strings.HasPrefix(filepath.Base(p), "lower-"):
				return 1
			default:
				return 0
			}
		}
		if rank(a) != rank(b) {
			return rank(a) - rank(b)
		}
		return strings.Compare(b, a)
	})
	for _, point := range points {
		if err := cleanPoint(point, run, seam); err != nil {
			return err
		}
	}
	return nil
}

func (w *Workspace) rebaseUpper(req Request, desired []string, seam hooks) (err error) {
	plan, err := pendingRebasePlan(w.runtimeDir, req.Rebase.Identity)
	if err != nil || (plan != nil && plan.Applied) {
		return err
	}
	root := rebaseTransit(w.runtimeDir)
	temp := &Workspace{runtimeDir: root, run: w.run}
	defer func() {
		err = errors.Join(err, temp.closeRebaseViews(seam))
		// A failed detach must leave its directories and lower mounts intact.
		if len(temp.mounts) == 0 {
			err = errors.Join(err, temp.cleanup())
		}
	}()
	if err := temp.mkdirNew(root); err != nil {
		return err
	}
	if plan == nil {
		current, err := w.rebaseCurrent(req, temp, seam)
		if err != nil {
			return err
		}
		target, err := temp.rebaseView(req, "desired", desired, false, seam)
		if err != nil {
			return err
		}
		deleted, err := planRebaseDeletions(current, target)
		if err != nil {
			return err
		}
		fresh := newRebasePlan(req.Rebase.Identity, deleted)
		if err := saveRebasePlan(w.runtimeDir, fresh); err != nil {
			return err
		}
		plan = &fresh
		// Never attach the writable upper while the comparison view reads it.
		if err := temp.closeRebaseViews(seam); err != nil {
			return err
		}
	}
	if len(plan.Deleted) > 0 {
		view, err := temp.rebaseView(req, "apply", desired, true, seam)
		if err != nil {
			return err
		}
		if err := applyRebaseDeletions(view, plan.Deleted); err != nil {
			return err
		}
		if err := temp.closeRebaseViews(seam); err != nil {
			return err
		}
		if err := syncRebaseUpper(w.UpperDir, plan.Deleted); err != nil {
			return err
		}
	}
	plan.Applied = true
	return saveRebasePlan(w.runtimeDir, *plan)
}

func (w *Workspace) closeRebaseViews(seam hooks) error {
	for len(w.mounts) > 0 {
		i := len(w.mounts) - 1
		if err := cleanPoint(w.mounts[i], w.run, seam); err != nil {
			return err
		}
		w.mounts = w.mounts[:i]
	}
	return nil
}

func (w *Workspace) rebaseCurrent(req Request, temp *Workspace, seam hooks) (string, error) {
	oldReq := Request{Members: req.Rebase.Members, Subdir: req.Subdir, ReadOnly: true,
		SquashfsOptions: req.SquashfsOptions, FuseOptions: req.FuseOptions}
	members, err := stack(oldReq)
	if err != nil {
		return "", err
	}
	if err := preflight(oldReq); err != nil {
		return "", err
	}
	members, err = temp.mountBlockMembers(members, seam)
	if err != nil {
		return "", err
	}
	members, err = pruneBySubdir(members, oldReq, w.run)
	if err != nil {
		return "", err
	}
	points := []string{w.UpperDir}
	for i, m := range members {
		point := filepath.Join(temp.runtimeDir, fmt.Sprintf("lower-%d", i))
		if err := temp.mkdirNew(point); err != nil {
			return "", err
		}
		if err := temp.mountPoint(m, oldReq, point, seam); err != nil {
			return "", err
		}
		points = append(points, point)
	}
	return temp.rebaseView(req, "current", points, false, seam)
}

func (w *Workspace) rebaseView(req Request, name string, lowers []string, writable bool, seam hooks) (string, error) {
	point := filepath.Join(w.runtimeDir, name)
	if err := w.mkdirNew(point); err != nil {
		return "", err
	}
	upper, work := "", ""
	if writable {
		upper, work = filepath.Join(req.RuntimeDir, "upper"), filepath.Join(req.RuntimeDir, "work")
	}
	argv := overlayArgv(lowers, upper, work, req.FuseOptions, point)
	if err := w.run(argv[0], argv[1:]...); err != nil {
		return "", err
	}
	w.mounts = append(w.mounts, point)
	if err := waitLive(point, seam); err != nil {
		return "", err
	}
	return point, nil
}

// Remove through FUSE so rootless and privileged whiteout encodings both work.
func applyRebaseDeletions(view string, deleted []string) error {
	root, err := dirroot.Open(view)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, rel := range deleted {
		// Recovery must never traverse a symlink or a replaced parent.
		parent := filepath.Dir(rel)
		for parent != "." {
			info, err := root.Lstat(parent)
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			if err != nil {
				return err
			}
			if !info.IsDir() {
				return fmt.Errorf("upper rebase: deletion parent %q is not a directory", parent)
			}
			parent = filepath.Dir(parent)
		}
		if err := root.RemoveAll(rel); err != nil {
			return err
		}
	}
	return nil
}

// Sync generated markers before their directories, without opening unrelated
// user files. A device whiteout needs its directory synced, never the device.
func syncRebaseUpper(upper string, deleted []string) error {
	root, err := dirroot.Open(upper)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	syncPath := func(rel string) error {
		file, err := root.Open(rel)
		if err != nil {
			return err
		}
		return errors.Join(durable.Sync(file), file.Close())
	}
	seen := make(map[string]bool)
	var directories []string
	for _, rel := range deleted {
		info, err := root.Lstat(rel)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && info.Mode().IsRegular() {
			if err := syncPath(rel); err != nil {
				return err
			}
		}
		for dir := filepath.Dir(rel); dir != "."; dir = filepath.Dir(dir) {
			if !seen[dir] {
				seen[dir] = true
				directories = append(directories, dir)
			}
		}
	}
	// Descending paths put each child before its parent; sync the root last.
	slices.SortFunc(directories, func(a, b string) int { return strings.Compare(b, a) })
	for _, dir := range append(directories, ".") {
		info, err := root.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue // An opaque ancestor can already hide the deleted path.
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("upper rebase: sync parent %q is not a directory", dir)
		}
		if err := syncPath(dir); err != nil {
			return err
		}
	}
	return nil
}
