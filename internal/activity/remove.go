package activity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// errAbsentAncestor means a parent component does not exist, so the
// target file cannot exist either. Callers treat that as a successful
// removal of a missing log.
var errAbsentAncestor = errors.New("activity ancestor is absent")

// RemoveLog deletes path and the lock file beside it. A missing file
// is success. A symlink or any other non-regular file is an error and
// is left in place, so deletion never follows a link out of root.
func RemoveLog(root, path string) error {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	lock := path + ".lock"
	logState, err := classifyRegular(root, path)
	if err != nil {
		return err
	}
	lockState, err := classifyRegular(root, lock)
	if err != nil {
		return err
	}
	if logState {
		if err := unlinkRegular(path); err != nil {
			return err
		}
	}
	if lockState {
		return unlinkRegular(lock)
	}
	return nil
}

// RemoveLogDir deletes dir when it is a real directory inside root. A
// missing directory is success. A symlink or non-directory is an error
// and is not followed.
func RemoveLogDir(root, dir string) error {
	root = filepath.Clean(root)
	dir = filepath.Clean(dir)
	if dir == root {
		return fmt.Errorf("%w: %q", ErrInvalidPath, dir)
	}
	if err := managedPrefix(root, dir); err != nil {
		return err
	}
	if err := inspectAncestors(root, filepath.Dir(dir)); err != nil {
		if errors.Is(err, errAbsentAncestor) {
			return nil
		}
		return err
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("activity: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrUnsafeFile, dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("activity: %w", err)
	}
	return nil
}

// PruneEmptyDirs removes empty real directories from dir up to, but
// not including, stop. A non-empty directory ends the walk. A symlink
// is an error and is not removed.
func PruneEmptyDirs(dir, stop string) error {
	dir = filepath.Clean(dir)
	stop = filepath.Clean(stop)
	if dir != stop {
		if err := managedPrefix(stop, dir); err != nil {
			return err
		}
	}
	for dir != stop && dir != "." && dir != string(filepath.Separator) {
		removed, err := removeEmptyDir(dir)
		if err != nil || !removed {
			return err
		}
		dir = filepath.Dir(dir)
	}
	return nil
}

// classifyRegular reports whether path is an existing regular file
// inside root. A missing file reports false. Both states are checked
// before either file is unlinked.
func classifyRegular(root, path string) (bool, error) {
	if err := managedPrefix(root, path); err != nil {
		return false, err
	}
	if path == root {
		return false, fmt.Errorf("%w: %q", ErrInvalidPath, path)
	}
	if err := inspectAncestors(root, filepath.Dir(path)); err != nil {
		if errors.Is(err, errAbsentAncestor) {
			return false, nil
		}
		return false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("activity: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("%w: %s", ErrUnsafeFile, path)
	}
	return true, nil
}

// inspectAncestors refuses a symlink or non-directory between dir and
// root, inclusive. A missing component reports errAbsentAncestor only
// after every existing component has been checked, so a symlink above
// a missing child is still refused.
func inspectAncestors(root, dir string) error {
	if err := managedPrefix(root, dir); err != nil {
		return err
	}
	absent := false
	for current := dir; ; {
		info, err := os.Lstat(current)
		switch {
		case errors.Is(err, os.ErrNotExist):
			absent = true
		case err != nil:
			return fmt.Errorf("activity: %w", err)
		case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
			return fmt.Errorf("%w: %s", ErrUnsafeFile, current)
		}
		if current == root {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("%w: %q", ErrInvalidPath, dir)
		}
		current = parent
	}
	if absent {
		return errAbsentAncestor
	}
	return nil
}

func unlinkRegular(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("activity: %w", err)
	}
	return nil
}

// removeEmptyDir removes dir when it is an empty real directory. A
// missing directory counts as removed so the caller can keep walking.
// A non-empty directory stops the walk without an error.
func removeEmptyDir(dir string) (bool, error) {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("activity: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("%w: %s", ErrUnsafeFile, dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("activity: %w", err)
	}
	if len(entries) != 0 {
		return false, nil
	}
	if err := os.Remove(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
			return false, nil
		}
		return false, fmt.Errorf("activity: %w", err)
	}
	return true, nil
}
