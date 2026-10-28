package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Sentinel errors shared by the store and its callers.
var (
	// ErrNotFound means no record exists under the given name.
	ErrNotFound = errors.New("session not found")
	// ErrExists means a session name is already recorded.
	ErrExists = errors.New("already exists")
	// ErrLockHeld means another process holds the record lock; scan
	// callers skip this round instead of blocking.
	ErrLockHeld = errors.New("session record is locked")
	// ErrLockTimeout means an exclusive lock stayed busy past its
	// deadline. Callers treat it as a runtime error, never a hang.
	ErrLockTimeout = errors.New("session lock timeout")
	// ErrLockUnavailable means advisory locking needs Linux.
	ErrLockUnavailable = errors.New("session locking requires linux")
)

// recordFileName is the file name of the record inside each session
// directory.
const recordFileName = "session.json"

// lockFileName is the file name of the advisory lock inside each
// session directory.
const lockFileName = "session.lock"

// Store reads and writes session records under one directory. Dir
// must point at the configured sessions directory.
//
// The mutating operations (Save, Append, Writeback) are
// read-modify-write cycles: callers must hold the session's
// exclusive lock around them, so one session is only ever mutated
// by one process at a time. Scan readers take the non-blocking lock
// and skip a held record instead of waiting.
type Store struct {
	// Dir is the sessions root; each session owns one subdirectory
	// <name>/ holding its session.json record, session.lock, and
	// runtime subtree.
	Dir string
}

// sessionDir validates the name and returns the session's own
// directory under the sessions root.
func (s Store) sessionDir(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", fmt.Errorf("session: %w", err)
	}
	return filepath.Join(s.Dir, name), nil
}

// recordPath validates the name and joins the record file inside the
// session directory.
func (s Store) recordPath(name string) (string, error) {
	dir, err := s.sessionDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, recordFileName), nil
}

// lockPath validates the name and joins the lock file inside the
// session directory, beside the record it protects.
func (s Store) lockPath(name string) (string, error) {
	dir, err := s.sessionDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, lockFileName), nil
}

// Load reads and validates one record. A missing file reports
// ErrNotFound; a corrupt file reports its parse failure instead of
// loading a half-understood record.
func (s Store) Load(name string) (Record, error) {
	path, err := s.recordPath(name)
	if err != nil {
		return Record{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Record{}, fmt.Errorf("session: %w: %s", ErrNotFound, name)
		}
		return Record{}, fmt.Errorf("session: %w", err)
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, fmt.Errorf("session: parse %s: %w", path, err)
	}
	if err := rec.Validate(); err != nil {
		return Record{}, fmt.Errorf("session: parse %s: %w", path, err)
	}
	if rec.Name != name {
		return Record{}, fmt.Errorf(
			"session: %s holds the record of %s", path, rec.Name)
	}
	return rec, nil
}

// Save validates the record and writes it atomically, so a reader
// never sees a half-written record.
func (s Store) Save(rec Record) error {
	if err := rec.Validate(); err != nil {
		return err
	}
	path, err := s.recordPath(rec.Name)
	if err != nil {
		return err
	}
	if err := s.ensureDir(rec.Name); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	return writeAtomic(path, append(data, '\n'))
}

// Append records one action write-ahead: it assigns the next
// increasing seq, forces the pending status, stamps a zero timestamp
// with the current time, and leaves the record fsynced on disk
// before it returns, so the action is durable before any mount
// executes. It returns the stored action with its assigned seq. The
// caller must hold the session's exclusive lock.
func (s Store) Append(name string, action Action) (Action, error) {
	rec, err := s.Load(name)
	if err != nil {
		return Action{}, err
	}
	action.Seq = nextSeq(rec.Actions)
	action.Status = StatusPending
	if action.Type == TypeMount && action.RuntimeID == "" {
		action.RuntimeID = runtimeIDOf(action.Seq)
	}
	if action.Timestamp.IsZero() {
		action.Timestamp = time.Now().UTC()
	}
	rec.Actions = append(rec.Actions, action)
	if err := s.Save(rec); err != nil {
		return Action{}, err
	}
	return action, nil
}

// nextSeq is one past the highest recorded seq.
func nextSeq(actions []Action) int {
	highest := 0
	for _, action := range actions {
		if action.Seq > highest {
			highest = action.Seq
		}
	}
	return highest + 1
}

// Writeback updates one recorded action after its execution: the
// final status, and the mountpoint when the execution resolved or
// changed it. An empty mountpoint keeps the recorded one. The
// updated record is written atomically. The caller must hold the
// session's exclusive lock.
func (s Store) Writeback(name string, seq int, status, mountpoint string) error {
	if !knownStatus(status) {
		return fmt.Errorf("session: %s: unknown status %q", name, status)
	}
	rec, err := s.Load(name)
	if err != nil {
		return err
	}
	for i := range rec.Actions {
		if rec.Actions[i].Seq != seq {
			continue
		}
		rec.Actions[i].Status = status
		if mountpoint != "" {
			rec.Actions[i].Mountpoint = mountpoint
		}
		return s.Save(rec)
	}
	return fmt.Errorf("session: %s has no action with seq %d", name, seq)
}

// WritebackMounted records one mount action as mounted after the
// view it names proved live: it sets the mounted status, takes the
// mountpoint when the execution resolved or changed it — an empty
// mountpoint keeps the recorded one — and, when clearServed is set,
// empties the served stack ids in the same atomic write. Folding
// the clear into the status write leaves a crash no window to
// strand status=mounted beside stale served ids, a state no later
// open could heal. A seq the record does not carry is an error, as
// in Writeback. The caller must hold the session's exclusive lock.
func (s Store) WritebackMounted(
	name string,
	seq int,
	mountpoint string,
	clearServed bool,
) error {
	rec, err := s.Load(name)
	if err != nil {
		return err
	}
	for i := range rec.Actions {
		if rec.Actions[i].Seq != seq {
			continue
		}
		rec.Actions[i].Status = StatusMounted
		if mountpoint != "" {
			rec.Actions[i].Mountpoint = mountpoint
		}
		if clearServed {
			rec.Actions[i].ServedID = ""
			rec.Actions[i].ServedTip = ""
		}
		return s.Save(rec)
	}
	return fmt.Errorf("session: %s has no action with seq %d", name, seq)
}

// knownStatus reports whether a writeback status is part of the
// record contract.
func knownStatus(status string) bool {
	switch status {
	case StatusPending, StatusMounted, StatusFailed, StatusUnmounted:
		return true
	}
	return false
}

// List loads every record in the directory, ordered by file name. A
// record that fails to parse is reported, never silently dropped. A
// missing directory lists nothing.
func (s Store) List() ([]Record, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: %w", err)
	}
	var records []Record
	for _, entry := range entries {
		if !entry.IsDir() || strings.EqualFold(entry.Name(), referenceDir) {
			continue
		}
		rec, err := s.Load(entry.Name())
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidName) {
			// A directory that is not a session, either missing its
			// record or named no session ever owns, is skipped so a
			// stray directory cannot break a listing. This matches
			// the scan path, which tolerates the same names.
			continue
		}
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, nil
}

// ensureDir creates the session's own directory with owner-only
// access, so its record and lock have a home.
func (s Store) ensureDir(name string) error {
	dir, err := s.sessionDir(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	return nil
}

// writeAtomic writes data to path through a unique temporary file in
// the same directory: write, fsync, close, rename over the target,
// then a best-effort directory fsync so the rename itself survives a
// crash. A failure removes the temporary file and leaves the
// previous content intact.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := writeSyncClose(tmp, data); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	syncDir(filepath.Dir(path))
	return nil
}

// writeSyncClose writes one document, fsyncs it, and closes the file.
func writeSyncClose(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// syncDir flushes a directory entry best effort, so a completed
// rename is durable after a crash.
func syncDir(dir string) {
	file, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = file.Sync()
	_ = file.Close()
}
