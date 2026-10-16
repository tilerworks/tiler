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
	// ErrLockHeld means another process holds the record lock; scan
	// callers skip this round instead of blocking.
	ErrLockHeld = errors.New("session record is locked")
	// ErrLockTimeout means an exclusive lock stayed busy past its
	// deadline. Callers treat it as a runtime error, never a hang.
	ErrLockTimeout = errors.New("session lock timeout")
	// ErrLockUnavailable means advisory locking needs Linux.
	ErrLockUnavailable = errors.New("session locking requires linux")
)

// recordExt is the file extension of every record.
const recordExt = ".json"

// lockExt is the file extension of every advisory lock file.
const lockExt = ".lock"

// Store reads and writes session records under one directory. Dir
// must point at the configured sessions directory.
//
// The mutating operations (Save, Append, Writeback) are
// read-modify-write cycles: callers must hold the session's
// exclusive lock around them, so one session is only ever mutated
// by one process at a time. Scan readers take the non-blocking lock
// and skip a held record instead of waiting.
type Store struct {
	// Dir holds the <name>.json records and their <name>.lock files.
	Dir string
}

// recordPath validates the name and joins the record file under the
// sessions directory.
func (s Store) recordPath(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", fmt.Errorf("session: %w", err)
	}
	return filepath.Join(s.Dir, name+recordExt), nil
}

// lockPath validates the name and joins the lock file under the
// sessions directory, beside the record it protects.
func (s Store) lockPath(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", fmt.Errorf("session: %w", err)
	}
	return filepath.Join(s.Dir, lockName(name)), nil
}

// lockName flattens a session name into its lock file name. Validated
// names are already single segments; the flattening keeps the helper
// safe for any name a scan reads back from disk.
func lockName(name string) string {
	flat := strings.NewReplacer("/", "_", `\`, "_").Replace(name)
	return flat + lockExt
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
	if err := s.ensureDir(); err != nil {
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
		name, ok := strings.CutSuffix(entry.Name(), recordExt)
		if entry.IsDir() || !ok {
			continue
		}
		rec, err := s.Load(name)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, nil
}

// ensureDir creates the sessions directory with owner-only access.
func (s Store) ensureDir() error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
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
