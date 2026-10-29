package activity

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
)

// Bounds of one activity log. A log that would grow past MaxLogBytes
// is compacted to its newest complete records before the append, so
// the file never exceeds the limit and no rotation history appears.
const (
	// MaxLogBytes bounds one activity log file.
	MaxLogBytes = 102400
	// CompactKeepBytes is the approximate newest suffix kept after a
	// compaction.
	CompactKeepBytes = 51200
)

// tempFile is the scratch file of one compaction; tests replace the
// factory to inject write, sync, and close failures.
type tempFile interface {
	io.Writer
	Name() string
	Sync() error
	Close() error
}

// makeRoom compacts the log when appending lineLen more bytes would
// push it past maxBytes. A missing log needs no room.
func (w *Writer) makeRoom(lineLen int) error {
	size, err := w.logSize()
	if err != nil {
		return err
	}
	if size == 0 || size+int64(lineLen) <= int64(w.maxBytes) {
		return nil
	}
	return w.compact()
}

// logSize reports the current log size and refuses an unsafe path
// before any read or append follows it.
func (w *Writer) logSize() (int64, error) {
	info, err := os.Lstat(w.logPath)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("activity: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return 0, fmt.Errorf("%w: %s", ErrUnsafeFile, w.logPath)
	}
	return info.Size(), nil
}

// compact replaces the log with its newest complete records. The
// replacement is a same-directory temporary file renamed into place,
// so a failure leaves the previous log intact.
func (w *Writer) compact() error {
	keep, err := w.tailRecords()
	if err != nil {
		return err
	}
	return w.replace(keep)
}

// tailRecords reads one bounded window from the end of the log and
// returns the newest complete records that fit keepBytes, always
// keeping the newest one. The window bounds memory to keepBytes plus
// one record, so a huge history is never read whole.
func (w *Writer) tailRecords() ([]byte, error) {
	file, err := openRegular(w.logPath, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("activity: %w", err)
	}
	size := info.Size()
	window := int64(w.keepBytes) + MaxLineBytes
	if window > size {
		window = size
	}
	buf := make([]byte, window)
	if _, err := file.ReadAt(buf, size-window); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("activity: compact %s: %w", w.logPath, err)
	}
	if window < size {
		// The window may start inside a record; that leading piece
		// is not a record and is never kept.
		index := bytes.IndexByte(buf, '\n')
		if index < 0 {
			return nil, nil
		}
		buf = buf[index+1:]
	}
	return suffixRecords(buf, w.keepBytes), nil
}

// suffixRecords returns the trailing complete records that fit limit,
// always keeping the newest complete record. An unterminated tail is
// not a record and is dropped.
func suffixRecords(buf []byte, limit int) []byte {
	if size := len(buf); size > 0 && buf[size-1] != '\n' {
		index := bytes.LastIndexByte(buf, '\n')
		if index < 0 {
			return nil
		}
		buf = buf[:index+1]
	}
	start, total := len(buf), 0
	for start > 0 {
		index := bytes.LastIndexByte(buf[:start-1], '\n')
		begin := index + 1
		if total > 0 && total+start-begin > limit {
			break
		}
		total += start - begin
		start = begin
	}
	return buf[start:]
}

// replace writes suffix to a same-directory temporary file, syncs it,
// and renames it over the log. A failed step keeps the old log and
// removes the temporary file.
func (w *Writer) replace(suffix []byte) error {
	tmp, err := w.makeTemp()
	if err != nil {
		return fmt.Errorf("activity: compact %s: %w", w.logPath, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := writeAll(tmp, suffix); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("activity: compact %s: %w", w.logPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("activity: compact %s: %w", w.logPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("activity: compact %s: %w", w.logPath, err)
	}
	if err := w.rename(name, w.logPath); err != nil {
		return fmt.Errorf("activity: compact %s: %w", w.logPath, err)
	}
	w.syncDir(w.dir)
	return nil
}

// syncDirectory flushes one directory entry best effort, so a
// completed rename survives a crash.
func syncDirectory(dir string) {
	file, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = file.Sync()
	_ = file.Close()
}
