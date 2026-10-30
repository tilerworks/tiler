package activity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Revision identifies the newest record of one log: when it happened
// and a digest of its encoded line. A caller that keeps the digest
// can tell later whether the log gained or replaced a record, which
// is how an expiry snapshot stays tied to the activity it saw.
//
// The digest covers only the newest line. Two appends that encode the
// very same line are therefore indistinguishable, and they cannot
// change an expiry decision because they carry the same time.
type Revision struct {
	// Last is the time of the newest record, in UTC.
	Last time.Time
	// Digest is the hex SHA-256 of the newest encoded line.
	Digest string
}

// LastActivity returns the time of the newest complete record. A log
// that does not exist or is empty reports ErrNoActivity, so the
// caller initializes the object instead of expiring it. A non-empty
// log whose newest record cannot be trusted reports ErrDamagedLog and
// never falls back to older activity.
func (w *Writer) LastActivity() (time.Time, error) {
	rev, err := w.Revision()
	return rev.Last, err
}

// Revision reads the newest record under the log lock.
func (w *Writer) Revision() (Revision, error) {
	release, err := w.lock()
	if err != nil {
		return Revision{}, err
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
	initialized, rev, err := w.Inspect(ev)
	return initialized, rev.Last, err
}

// Inspect is EnsureInitialized with the revision of the log it read,
// so one scan can initialize a missing log and pin the record it saw
// without reading the tail twice. The returned revision is the one
// written by this call when it initialized the log.
func (w *Writer) Inspect(ev Event) (bool, Revision, error) {
	if err := ev.check(); err != nil {
		return false, Revision{}, err
	}
	release, err := w.lock()
	if err != nil {
		return false, Revision{}, err
	}
	defer release()
	rev, err := w.readLastLocked()
	switch {
	case err == nil:
		return false, rev, nil
	case !errors.Is(err, ErrNoActivity):
		return false, Revision{}, err
	}
	at := w.clock().UTC()
	line, err := encodeLine(ev, at)
	if err != nil {
		return false, Revision{Last: at}, err
	}
	if err := w.write(line); err != nil {
		return false, Revision{Last: at}, err
	}
	return true, Revision{Last: at, Digest: lineDigest(line)}, nil
}

// readLastLocked reads the newest record of the log; the caller holds
// the log lock. Only one bounded tail window is read, so a long
// history is never scanned or loaded whole.
func (w *Writer) readLastLocked() (Revision, error) {
	file, err := openRegular(w.logPath, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return Revision{}, ErrNoActivity
	}
	if err != nil {
		return Revision{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return Revision{}, fmt.Errorf("activity: %w", err)
	}
	size := info.Size()
	if size == 0 {
		return Revision{}, ErrNoActivity
	}
	window := int64(MaxLineBytes)
	if window > size {
		window = size
	}
	buf := make([]byte, window)
	if _, err := file.ReadAt(buf, size-window); err != nil && !errors.Is(err, io.EOF) {
		return Revision{}, fmt.Errorf("activity: read %s: %w", w.logPath, err)
	}
	line, err := lastLine(buf)
	if err != nil {
		return Revision{}, err
	}
	at, err := decodeLast(line)
	if err != nil {
		return Revision{}, err
	}
	return Revision{Last: at, Digest: lineDigest(line)}, nil
}

// lineDigest is the identifier of one encoded record. The closing
// newline is not part of it, so the record an append encodes and the
// record a later read finds hash the same way.
func lineDigest(line []byte) string {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	sum := sha256.Sum256(line)
	return hex.EncodeToString(sum[:])
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
// phase. Format number 1 is the current line, and its image version
// id is image_version. A missing version, or a string version, is a
// legacy line, and that string is the image version id. Any other
// number, or a value that is neither a number nor a string, is damage.
func decodeLast(line []byte) (time.Time, error) {
	rec, err := decodeLine(line)
	if err != nil {
		return time.Time{}, err
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

// decodeLine reads one record. The version value selects the shape,
// because a legacy line stores a string where a new line stores a
// number. The returned record always uses ImageVersion for the id.
func decodeLine(line []byte) (record, error) {
	raw, err := rawVersion(line)
	if err != nil {
		return record{}, err
	}
	legacy, err := legacyVersion(raw)
	if err != nil {
		return record{}, err
	}
	if legacy {
		return decodeLegacyLine(line)
	}
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		return record{}, fmt.Errorf("%w: %v", ErrDamagedLog, err)
	}
	if rec.Version != recordVersion {
		return record{}, fmt.Errorf("%w: version %d", ErrDamagedLog, rec.Version)
	}
	return rec, nil
}

// rawVersion returns the untyped version value. A missing key is an
// empty message, which the caller treats as a legacy line.
func rawVersion(line []byte) (json.RawMessage, error) {
	var envelope struct {
		Version json.RawMessage `json:"version"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDamagedLog, err)
	}
	return envelope.Version, nil
}

// legacyVersion reports whether raw is a legacy version value. Number
// 1 is the current format. A missing value or a string is legacy.
// Every other value is a damaged log.
func legacyVersion(raw json.RawMessage) (bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return true, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return false, fmt.Errorf("%w: version: %v", ErrDamagedLog, err)
	}
	switch typed := value.(type) {
	case string:
		return true, nil
	case json.Number:
		number, err := typed.Int64()
		if err != nil || number != recordVersion {
			return false, fmt.Errorf("%w: version %s", ErrDamagedLog, typed)
		}
		return false, nil
	default:
		return false, fmt.Errorf("%w: version %s", ErrDamagedLog, raw)
	}
}

// decodeLegacyLine reads a line whose version is missing or a string.
// That string is the image version id.
func decodeLegacyLine(line []byte) (record, error) {
	var old struct {
		payload
		Version string `json:"version"`
	}
	if err := json.Unmarshal(line, &old); err != nil {
		return record{}, fmt.Errorf("%w: %v", ErrDamagedLog, err)
	}
	old.ImageVersion = old.Version
	return record{payload: old.payload}, nil
}
