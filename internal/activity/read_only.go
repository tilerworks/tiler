package activity

import (
	"errors"
	"os"
	"time"

	"github.com/jiangxin/tiler/internal/filelock"
)

// ReadLastActivity reads under an existing log lock without creating files or
// tightening permissions. Missing or empty logs have no activity; a nonempty
// log without its stable lock cannot be read safely.
func ReadLastActivity(root, dir, name string) (time.Time, error) {
	w, err := OpenLog(root, dir, name, time.Now)
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, ErrNoActivity
	}
	if err != nil {
		return time.Time{}, err
	}
	file, err := openRegularMode(w.logPath, os.O_RDONLY, false)
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, ErrNoActivity
	}
	if err != nil {
		return time.Time{}, err
	}
	info, statErr := file.Stat()
	_ = file.Close()
	if statErr != nil {
		return time.Time{}, statErr
	}
	if info.Size() == 0 {
		return time.Time{}, ErrNoActivity
	}
	lock, err := openRegularMode(w.lockPath, os.O_RDONLY, false)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = lock.Close() }()
	if err := filelock.Acquire(lock, 0); err != nil {
		return time.Time{}, err
	}
	defer func() { _ = filelock.Release(lock) }()
	// Reopen after locking: compaction may have replaced the first inode.
	file, err = openRegularMode(w.logPath, os.O_RDONLY, false)
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, ErrNoActivity
	}
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = file.Close() }()
	rev, err := readTail(file, w.logPath)
	return rev.Last, err
}
