// Package activity appends bounded JSON-line events to the activity
// logs of sessions and image versions. A log sits beside a stable
// lock file: every append and read holds that lock, so concurrent
// processes never interleave records or reuse a stale last event.
package activity

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jiangxin/tiler/internal/durable"

	"github.com/jiangxin/tiler/internal/filelock"
)

const (
	// MaxLineBytes bounds one encoded record, newline included.
	MaxLineBytes = 4096
	// DefaultLockTimeout bounds the wait for one log lock.
	DefaultLockTimeout = 5 * time.Second
	// filePerm is the mode of the log and its lock file.
	filePerm = 0o600
)

// Errors reported by the activity writer.
var (
	// ErrInvalidPath is a log directory outside its managed root.
	ErrInvalidPath = errors.New("invalid activity path")
	// ErrUnsafeFile is a symlink or non-regular log, lock, or
	// directory component.
	ErrUnsafeFile = errors.New("unsafe activity file")
	// ErrLineTooLong is a record that cannot fit MaxLineBytes even
	// after its diagnostic text is shortened.
	ErrLineTooLong = errors.New("activity line too long")
	// ErrInvalidEvent is a missing event name or an unknown phase.
	ErrInvalidEvent = errors.New("invalid activity event")
	// ErrNoActivity is a log that does not exist or is empty, so the
	// object needs one initialize event instead of an expiry decision.
	ErrNoActivity = errors.New("no activity recorded")
	// ErrDamagedLog is a non-empty log whose newest record cannot be
	// trusted; such an object is never a removal candidate.
	ErrDamagedLog = errors.New("damaged activity log")
)

// Clock returns the current time. Tests inject a fixed clock so log
// timestamps and expiry decisions are deterministic.
type Clock func() time.Time

// logFile is the append surface of one log file; tests replace the
// opener to inject short writes and sync failures.
type logFile interface {
	io.Writer
	Sync() error
	Close() error
}

// Writer appends events to the activity log of one object directory.
// It holds no open descriptor between calls, so an idle writer never
// keeps a deleted directory alive.
type Writer struct {
	root        string
	dir         string
	logPath     string
	lockPath    string
	clock       Clock
	lockTimeout time.Duration
	maxBytes    int
	keepBytes   int
	openLog     func() (logFile, error)
	makeTemp    func() (tempFile, error)
	rename      func(oldpath, newpath string) error
	syncDir     func(dir string)
}

// OpenLog binds a writer to dir/name inside root. dir must already
// exist; a missing directory reports the underlying error instead of
// recreating a tree that a deletion removed. name is one file
// component.
func OpenLog(root, dir, name string, clock Clock) (*Writer, error) {
	if clock == nil {
		return nil, errors.New("activity: nil clock")
	}
	if err := checkLogName(name); err != nil {
		return nil, err
	}
	if err := checkManagedDir(root, dir); err != nil {
		return nil, err
	}
	logPath := filepath.Join(filepath.Clean(dir), name)
	w := &Writer{
		root:        filepath.Clean(root),
		dir:         filepath.Clean(dir),
		logPath:     logPath,
		lockPath:    logPath + ".lock",
		clock:       clock,
		lockTimeout: DefaultLockTimeout,
		maxBytes:    MaxLogBytes,
		keepBytes:   CompactKeepBytes,
		rename:      os.Rename,
		syncDir:     syncDirectory,
	}
	w.openLog = w.openAppend
	w.makeTemp = w.createTemp
	return w, nil
}

// Dir returns the directory this writer appends to.
func (w *Writer) Dir() string { return w.dir }

// LogPath returns the activity log this writer appends to.
func (w *Writer) LogPath() string { return w.logPath }

// Append appends one event and returns the recorded UTC time. The
// managed path is revalidated and the log lock is held for the whole
// write, so the timestamp is taken under the lock. The time never
// moves back: it is the later of the clock and the newest existing
// record, so a backward clock cannot make the log regress, and
// activity after a future timestamp keeps that future time.
//
// A missing directory, a busy lock, an unsafe file, or a damaged
// newest record fails the append, and no unlocked write is ever
// attempted.
func (w *Writer) Append(ev Event) (time.Time, error) {
	if err := ev.check(); err != nil {
		return time.Time{}, err
	}
	release, err := w.lock()
	if err != nil {
		return time.Time{}, err
	}
	defer release()
	now := w.clock().UTC()
	at := now
	last, err := w.readLastLocked()
	switch {
	case errors.Is(err, ErrNoActivity):
	case err != nil:
		return time.Time{}, err
	case last.Last.After(now):
		at = last.Last
	}
	line, err := encodeLine(ev, at)
	if err != nil {
		return at, err
	}
	if err := w.write(line); err != nil {
		return at, err
	}
	return at, nil
}

// lock takes the exclusive advisory lock of the stable lock file and
// returns the release closure. The lock file is never unlinked, so
// waiters always contend on one inode.
func (w *Writer) lock() (func(), error) {
	if err := checkManagedDir(w.root, w.dir); err != nil {
		return nil, err
	}
	file, err := openRegular(w.lockPath, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, err
	}
	if err := filelock.Acquire(file, w.lockTimeout); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("activity: lock %s: %w", w.dir, err)
	}
	return func() {
		_ = filelock.Release(file)
		_ = file.Close()
	}, nil
}

// write appends the encoded line, then syncs and closes the log. A
// full log is compacted first, under the same lock, so the file stays
// bounded. Any failure is reported: a torn or unsynced record must
// never pass as a complete one.
func (w *Writer) write(line []byte) error {
	if err := w.makeRoom(len(line)); err != nil {
		return err
	}
	file, err := w.openLog()
	if err != nil {
		return err
	}
	if err := writeAll(file, line); err != nil {
		_ = file.Close()
		return fmt.Errorf("activity: append %s: %w", w.logPath, err)
	}
	if err := durable.Sync(file); err != nil {
		_ = file.Close()
		return fmt.Errorf("activity: sync %s: %w", w.logPath, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("activity: close %s: %w", w.logPath, err)
	}
	return nil
}

// openAppend opens the log for appending after rejecting a symlink or
// non-regular file. The log file stays open only for this record.
func (w *Writer) openAppend() (logFile, error) {
	file, err := openRegular(w.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return nil, err
	}
	return file, nil
}

// createTemp creates the scratch file of one compaction beside the
// log, so the replacement rename stays on one filesystem.
func (w *Writer) createTemp() (tempFile, error) {
	file, err := os.CreateTemp(w.dir, "."+filepath.Base(w.logPath)+".tmp-*")
	if err != nil {
		return nil, err
	}
	return file, nil
}

// writeAll writes the whole line in one appending write and reports a
// short write rather than leaving a truncated record behind.
func writeAll(file io.Writer, line []byte) error {
	n, err := file.Write(line)
	if err != nil {
		return err
	}
	if n != len(line) {
		return io.ErrShortWrite
	}
	return nil
}
