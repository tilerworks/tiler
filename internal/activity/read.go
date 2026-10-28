package activity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// LastActivity returns the time of the newest complete record. A log
// that does not exist or is empty reports ErrNoActivity, so the
// caller initializes the object instead of expiring it. A non-empty
// log whose newest record cannot be trusted reports ErrDamagedLog and
// never falls back to older activity.
func (w *Writer) LastActivity() (time.Time, error) {
	release, err := w.lock()
	if err != nil {
		return time.Time{}, err
	}
	defer release()
	return w.readLastLocked()
}

// EnsureInitialized inspects the log and writes one initialize event
// when it is missing or empty. The inspection and the write share one
// lock, so concurrent scans initialize an object exactly once. It
// reports whether this call initialized the log and the newest
// activity time either way. A damaged log and an unwritable directory
// report an error instead of a time, so a failed scan never authorizes
// removal.
//
// ev must describe an initialization: the object's own
// activity.initialize in the initialized phase, or the dependency
// event an ancestor records for that propagation. Passing a plain
// lifecycle event would disguise the one-time initialization as real
// activity.
func (w *Writer) EnsureInitialized(ev Event) (bool, time.Time, error) {
	if err := ev.check(); err != nil {
		return false, time.Time{}, err
	}
	release, err := w.lock()
	if err != nil {
		return false, time.Time{}, err
	}
	defer release()
	last, err := w.readLastLocked()
	switch {
	case err == nil:
		return false, last, nil
	case !errors.Is(err, ErrNoActivity):
		return false, time.Time{}, err
	}
	at := w.clock().UTC()
	line, err := encodeLine(ev, at)
	if err != nil {
		return false, at, err
	}
	if err := w.write(line); err != nil {
		return false, at, err
	}
	return true, at, nil
}

// readLastLocked reads the newest record of the log; the caller holds
// the log lock. Only one bounded tail window is read, so a long
// history is never scanned or loaded whole.
func (w *Writer) readLastLocked() (time.Time, error) {
	file, err := openRegular(w.logPath, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, ErrNoActivity
	}
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return time.Time{}, fmt.Errorf("activity: %w", err)
	}
	size := info.Size()
	if size == 0 {
		return time.Time{}, ErrNoActivity
	}
	window := int64(MaxLineBytes)
	if window > size {
		window = size
	}
	buf := make([]byte, window)
	if _, err := file.ReadAt(buf, size-window); err != nil && !errors.Is(err, io.EOF) {
		return time.Time{}, fmt.Errorf("activity: read %s: %w", w.logPath, err)
	}
	line, err := lastLine(buf)
	if err != nil {
		return time.Time{}, err
	}
	return decodeLast(line)
}

// lastLine returns the newest record of one tail window. A missing
// final newline, a final record longer than the window, or an empty
// final line means the tail is damaged, never that older activity is
// still valid.
func lastLine(buf []byte) ([]byte, error) {
	if len(buf) == 0 || buf[len(buf)-1] != '\n' {
		return nil, fmt.Errorf("%w: unterminated tail", ErrDamagedLog)
	}
	end := len(buf) - 1
	start := bytes.LastIndexByte(buf[:end], '\n') + 1
	if start == end {
		return nil, fmt.Errorf("%w: empty newest line", ErrDamagedLog)
	}
	return buf[start:end], nil
}

// decodeLast parses one record and requires the fields that carry
// activity: a valid RFC3339Nano time, an event name, and a known
// phase.
func decodeLast(line []byte) (time.Time, error) {
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		return time.Time{}, fmt.Errorf("%w: %v", ErrDamagedLog, err)
	}
	at, err := time.Parse(time.RFC3339Nano, rec.Time)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: time %q", ErrDamagedLog, rec.Time)
	}
	if rec.Event == "" || !knownPhase(rec.Phase) {
		return time.Time{}, fmt.Errorf("%w: event %q phase %q", ErrDamagedLog, rec.Event, rec.Phase)
	}
	return at.UTC(), nil
}
