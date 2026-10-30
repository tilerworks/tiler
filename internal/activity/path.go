package activity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// checkLogName accepts one file component and refuses a name that
// could escape the log directory.
func checkLogName(name string) error {
	if name == "" || name == "." || name == ".." || name != filepath.Base(name) {
		return fmt.Errorf("%w: %q", ErrInvalidPath, name)
	}
	return nil
}

// PrepareDir creates dir at 0755 inside root. Components that already
// exist must be real directories, so a symlink is never followed into
// a tree outside the managed root.
func PrepareDir(root, dir string) error {
	root = filepath.Clean(root)
	dir = filepath.Clean(dir)
	if err := managedPrefix(root, dir); err != nil {
		return err
	}
	if err := rejectExistingSymlinks(root, dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("activity: %w", err)
	}
	return checkManagedDir(root, dir)
}

// checkManagedDir verifies that dir is an existing directory inside
// root and that no component is a symlink or a non-directory. A
// missing directory reports the underlying error: the activity log
// never recreates a directory that a deletion removed.
func checkManagedDir(root, dir string) error {
	root = filepath.Clean(root)
	dir = filepath.Clean(dir)
	if err := managedPrefix(root, dir); err != nil {
		return err
	}
	for current := dir; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("activity: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: %s", ErrUnsafeFile, current)
		}
		if current == root {
			return nil
		}
	}
}

func managedPrefix(root, dir string) error {
	if !filepath.IsAbs(root) || !filepath.IsAbs(dir) {
		return fmt.Errorf("%w: %q", ErrInvalidPath, dir)
	}
	if dir != root && !strings.HasPrefix(dir, root+string(filepath.Separator)) {
		return fmt.Errorf("%w: %q is outside %q", ErrInvalidPath, dir, root)
	}
	return nil
}

// rejectExistingSymlinks walks the existing ancestors of dir and
// refuses a symlink or a non-directory. Missing components are left
// for MkdirAll.
func rejectExistingSymlinks(root, dir string) error {
	for current := dir; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if current == root {
				return nil
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("activity: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: %s", ErrUnsafeFile, current)
		}
		if current == root {
			return nil
		}
	}
}

// openRegular opens path with flag and refuses a path that is, or
// turns out to be, a symlink or a non-regular file. Every opened file
// ends up owner-only, so a log or lock inherited with wider
// permissions is tightened instead of trusted.
func openRegular(path string, flag int) (*os.File, error) {
	before, err := os.Lstat(path)
	switch {
	case err == nil:
		if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: %s", ErrUnsafeFile, path)
		}
	case errors.Is(err, os.ErrNotExist):
		// The open below creates the file with filePerm.
	default:
		return nil, fmt.Errorf("activity: %w", err)
	}
	file, err := os.OpenFile(path, flag, filePerm)
	if err != nil {
		return nil, fmt.Errorf("activity: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("activity: %w", err)
	}
	if !info.Mode().IsRegular() || (before != nil && !os.SameFile(before, info)) {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %s", ErrUnsafeFile, path)
	}
	if info.Mode().Perm() != filePerm {
		if err := file.Chmod(filePerm); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("activity: %w", err)
		}
	}
	return file, nil
}
