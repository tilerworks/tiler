package activity

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// migrateLegacy moves a legacy actions.log, and its lock when the
// rename succeeds, onto dst. A destination that already exists is
// left alone. A rename that fails because the source is read-only or
// on another filesystem copies a readable log and leaves the
// original. A lock that cannot be renamed is not copied; the next
// append creates one beside the new log. An empty legacy path skips
// the migration.
func migrateLegacy(dst, legacy string) error {
	if legacy == "" {
		return nil
	}
	if _, err := os.Lstat(dst); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("activity: %w", err)
	}
	info, err := os.Lstat(legacy)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("activity: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", ErrUnsafeFile, legacy)
	}
	if err := os.Rename(legacy, dst); err != nil {
		return copyLegacyLog(legacy, dst)
	}
	moveLock(legacy+".lock", dst+".lock")
	return nil
}

// moveLock renames a regular lock file. Failure leaves the original
// lock where it is.
func moveLock(src, dst string) {
	info, err := os.Lstat(src)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return
	}
	_ = os.Rename(src, dst)
}

// copyLegacyLog copies src onto dst through a temporary file in the
// destination directory. The source stays in place. A failed copy
// removes the temporary file and reports the error.
func copyLegacyLog(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("activity: migrate %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".migrate-*")
	if err != nil {
		return fmt.Errorf("activity: migrate %s: %w", src, err)
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(tmp, in); err != nil {
		return fmt.Errorf("activity: migrate %s: %w", src, err)
	}
	if err := tmp.Chmod(filePerm); err != nil {
		return fmt.Errorf("activity: migrate %s: %w", src, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("activity: migrate %s: %w", src, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("activity: migrate %s: %w", src, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("activity: migrate %s: %w", src, err)
	}
	done = true
	return nil
}
