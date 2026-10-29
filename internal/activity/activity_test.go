package activity

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/filelock"
)

// testTime is one fixed instant with nanoseconds for the log format.
var testTime = time.Date(2026, 10, 28, 9, 15, 30, 123456789, time.UTC)

// errBoom marks an injected failure.
var errBoom = errors.New("boom")

// fixedClock returns one instant regardless of the wall clock.
func fixedClock(at time.Time) Clock {
	return func() time.Time { return at }
}

// openTestWriter creates a managed object directory and binds a
// writer to its log.
func openTestWriter(t *testing.T, clock Clock) (*Writer, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "dev")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := OpenLog(root, dir, clock)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	return w, dir
}

// readLines returns the log's records without their newlines.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(data) == 0 {
		return nil
	}
	if data[len(data)-1] != '\n' {
		t.Fatalf("log does not end with a newline: %q", data)
	}
	return strings.Split(string(data[:len(data)-1]), "\n")
}

// decodeRecord parses one log line into a generic object.
func decodeRecord(t *testing.T, line string) map[string]any {
	t.Helper()
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("record %q is not JSON: %v", line, err)
	}
	return rec
}

// TestAppendWritesOneUTCRecord checks the record shape, the UTC
// time with nanoseconds, and the owner-only file modes.
func TestAppendWritesOneUTCRecord(t *testing.T) {
	at := time.Date(2026, 10, 28, 9, 15, 30, 123456789, time.FixedZone("CST", 8*60*60))
	w, dir := openTestWriter(t, fixedClock(at))
	got, err := w.Append(Event{
		Event:      EventSessionCreate,
		Phase:      PhaseSucceeded,
		Session:    "dev",
		InstanceID: "inst-1",
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if !got.Equal(at) {
		t.Fatalf("recorded time = %v, want %v", got, at)
	}
	lines := readLines(t, filepath.Join(dir, LogName))
	if len(lines) != 1 {
		t.Fatalf("records = %d, want 1", len(lines))
	}
	rec := decodeRecord(t, lines[0])
	stamp, err := time.Parse(time.RFC3339Nano, rec["time"].(string))
	if err != nil {
		t.Fatalf("time %v: %v", rec["time"], err)
	}
	if !stamp.Equal(at) || stamp.Nanosecond() != 123456789 {
		t.Fatalf("time = %v, want %v", stamp, at)
	}
	if rec["event"] != EventSessionCreate || rec["phase"] != PhaseSucceeded {
		t.Fatalf("record = %v", rec)
	}
	if rec["session"] != "dev" || rec["instance_id"] != "inst-1" {
		t.Fatalf("record = %v", rec)
	}
	for _, name := range []string{LogName, LockName} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%s mode = %o, want 600", name, perm)
		}
	}
}

// TestAppendEscapesTextIntoSingleLine proves path and error text
// cannot split one record into two lines.
func TestAppendEscapesTextIntoSingleLine(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	mountpoint := "/tmp/dir\nwith\"quote"
	failure := "mount failed\nnext line"
	if _, err := w.Append(Event{
		Event:      EventMount,
		Phase:      PhaseFailed,
		Mountpoint: mountpoint,
		Error:      failure,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	lines := readLines(t, filepath.Join(dir, LogName))
	if len(lines) != 1 {
		t.Fatalf("records = %d, want 1", len(lines))
	}
	rec := decodeRecord(t, lines[0])
	if rec["mountpoint"] != mountpoint || rec["error"] != failure {
		t.Fatalf("record = %v", rec)
	}
}

// TestEncodeLineBoundsRecordSize keeps every record within
// MaxLineBytes and refuses to shorten an identifier.
func TestEncodeLineBoundsRecordSize(t *testing.T) {
	long, err := encodeLine(Event{
		Event: EventMount,
		Phase: PhaseStarted,
		Error: strings.Repeat("e", 4*MaxLineBytes),
	}, testTime)
	if err != nil {
		t.Fatalf("encodeLine: %v", err)
	}
	if len(long) > MaxLineBytes {
		t.Fatalf("line = %d bytes, want at most %d", len(long), MaxLineBytes)
	}
	if !json.Valid(bytes.TrimSuffix(long, []byte("\n"))) {
		t.Fatalf("line is not valid JSON: %q", long)
	}
	rec := decodeRecord(t, string(bytes.TrimSuffix(long, []byte("\n"))))
	if rec["event"] != EventMount || rec["phase"] != PhaseStarted {
		t.Fatalf("record = %v", rec)
	}
	text, _ := rec["error"].(string)
	if text == "" || len(text) >= 4*MaxLineBytes {
		t.Fatalf("diagnostic text = %d bytes", len(text))
	}

	cases := map[string]Event{
		"event name": {Event: strings.Repeat("x", MaxLineBytes), Phase: PhaseStarted},
		"session":    {Event: EventMount, Phase: PhaseStarted, Session: strings.Repeat("s", MaxLineBytes)},
	}
	for name, ev := range cases {
		if _, err := encodeLine(ev, testTime); !errors.Is(err, ErrLineTooLong) {
			t.Fatalf("%s: err = %v, want ErrLineTooLong", name, err)
		}
	}
}

// TestAppendRejectsInvalidEvent refuses an unnamed event or an
// unknown phase without touching the log.
func TestAppendRejectsInvalidEvent(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	for name, ev := range map[string]Event{
		"missing name":  {Phase: PhaseStarted},
		"unknown phase": {Event: EventMount, Phase: "start"},
	} {
		if _, err := w.Append(ev); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("%s: err = %v, want ErrInvalidEvent", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, LogName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("log exists after rejected events: %v", err)
	}
}

// TestAppendTightensInheritedFileModes proves a log or lock that was
// copied in with a wider mode becomes owner-only on the next append.
func TestAppendTightensInheritedFileModes(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	for _, name := range []string{LogName, LockName} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(dir, name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Append(Event{Event: EventMount, Phase: PhaseStarted}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	for _, name := range []string{LogName, LockName} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%s mode = %o, want 600", name, perm)
		}
	}
}

// TestOpenLogRequiresClock rejects a writer without a clock source.
func TestOpenLogRequiresClock(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "dev")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLog(root, dir, nil); err == nil || !strings.Contains(err.Error(), "clock") {
		t.Fatalf("err = %v, want a missing clock error", err)
	}
}

// TestOpenLogNeverRecreatesDirectory fails on a missing object
// directory and leaves it missing.
func TestOpenLogNeverRecreatesDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "gone")
	if _, err := OpenLog(root, dir, fixedClock(testTime)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want a missing directory error", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("object directory was created: %v", err)
	}
}

// TestAppendFailsAfterDirectoryRemoval proves a writer bound earlier
// does not resurrect a deleted object directory.
func TestAppendFailsAfterDirectoryRemoval(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(Event{Event: EventMount, Phase: PhaseStarted}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want a missing directory error", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("object directory was recreated: %v", err)
	}
}

// TestOpenLogRejectsPathsOutsideRoot confines the log to its managed
// root and to absolute paths.
func TestOpenLogRejectsPathsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	cases := map[string]string{
		"outside root": outside,
		"relative":     filepath.Join("sessions", "dev"),
	}
	for name, dir := range cases {
		if _, err := OpenLog(root, dir, fixedClock(testTime)); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("%s: err = %v, want ErrInvalidPath", name, err)
		}
	}
}

// TestOpenLogRejectsSymlinkDirectory refuses an object directory
// reached through a symlink.
func TestOpenLogRejectsSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLog(root, link, fixedClock(testTime)); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("err = %v, want ErrUnsafeFile", err)
	}
}

// TestAppendRejectsSymlinkLog refuses to follow a symlinked log and
// leaves its external target unchanged.
func TestAppendRejectsSymlinkLog(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	target := filepath.Join(t.TempDir(), "target.log")
	if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, LogName)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(Event{Event: EventMount, Phase: PhaseStarted}); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("err = %v, want ErrUnsafeFile", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep\n" {
		t.Fatalf("external target = %q, want it unchanged", data)
	}
}

// TestAppendRejectsNonRegularFiles refuses a log or lock that is a
// directory instead of a regular file.
func TestAppendRejectsNonRegularFiles(t *testing.T) {
	for _, name := range []string{LogName, LockName} {
		t.Run(name, func(t *testing.T) {
			w, dir := openTestWriter(t, fixedClock(testTime))
			if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Append(Event{Event: EventMount, Phase: PhaseStarted}); !errors.Is(err, ErrUnsafeFile) {
				t.Fatalf("err = %v, want ErrUnsafeFile", err)
			}
		})
	}
}

// fakeLog records writes and injects write, sync, and close failures.
type fakeLog struct {
	data     []byte
	write    func([]byte) (int, error)
	syncErr  error
	closeErr error
}

func (f *fakeLog) Write(p []byte) (int, error) {
	if f.write != nil {
		return f.write(p)
	}
	f.data = append(f.data, p...)
	return len(p), nil
}

// Sync reports the injected failure.
func (f *fakeLog) Sync() error { return f.syncErr }

// Close reports the injected failure.
func (f *fakeLog) Close() error { return f.closeErr }

// TestAppendReportsIncompleteWrites surfaces a short write, a write
// or sync failure, and a close failure instead of a torn record.
func TestAppendReportsIncompleteWrites(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	cases := map[string]*fakeLog{
		"short write": {write: func(p []byte) (int, error) { return len(p) - 1, nil }},
		"write error": {write: func([]byte) (int, error) { return 0, errBoom }},
		"sync error":  {syncErr: errBoom},
		"close error": {closeErr: errBoom},
	}
	for name, file := range cases {
		t.Run(name, func(t *testing.T) {
			w.openLog = func() (logFile, error) { return file, nil }
			if _, err := w.Append(Event{Event: EventMount, Phase: PhaseStarted}); !errors.Is(err, errBoom) && !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("err = %v, want the injected failure", err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dir, LogName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("log exists after failed writes: %v", err)
	}
}

// TestAppendReportsOpenFailure never falls back to an unlocked write
// when the log cannot be opened.
func TestAppendReportsOpenFailure(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	w.openLog = func() (logFile, error) { return nil, errBoom }
	if _, err := w.Append(Event{Event: EventMount, Phase: PhaseStarted}); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want the injected failure", err)
	}
	if _, err := os.Stat(filepath.Join(dir, LogName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("log exists after a failed open: %v", err)
	}
}

// TestAppendReportsLockContention refuses a lock another holder keeps
// and resumes once it is released.
func TestAppendReportsLockContention(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	file, err := os.OpenFile(filepath.Join(dir, LockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := filelock.Acquire(file, time.Second); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	w.lockTimeout = 50 * time.Millisecond
	if _, err := w.Append(Event{Event: EventMount, Phase: PhaseStarted}); !errors.Is(err, filelock.ErrTimeout) {
		t.Fatalf("err = %v, want a lock timeout", err)
	}
	w.lockTimeout = 0
	if _, err := w.Append(Event{Event: EventMount, Phase: PhaseStarted}); !errors.Is(err, filelock.ErrHeld) {
		t.Fatalf("err = %v, want a held lock", err)
	}
	if _, err := os.Stat(filepath.Join(dir, LogName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("log exists although the lock was never held: %v", err)
	}
	if err := filelock.Release(file); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := w.Append(Event{Event: EventMount, Phase: PhaseStarted}); err != nil {
		t.Fatalf("append after release: %v", err)
	}
}

// TestAppendKeepsOneLockInode proves the lock file is never replaced,
// so waiters never lock a stale inode.
func TestAppendKeepsOneLockInode(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	if _, err := w.Append(Event{Event: EventMount, Phase: PhaseStarted}); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(dir, LockName))
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := w.Append(Event{Event: EventMount, Phase: PhaseSucceeded}); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.Stat(filepath.Join(dir, LockName))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("the lock file was replaced")
	}
	if lines := readLines(t, filepath.Join(dir, LogName)); len(lines) != 4 {
		t.Fatalf("records = %d, want 4", len(lines))
	}
}

// TestAppendConcurrentGoroutinesKeepRecordsWhole appends from many
// goroutines and requires one complete, parseable record per event.
func TestAppendConcurrentGoroutinesKeepRecordsWhole(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	const workers, each = 8, 25
	var wait sync.WaitGroup
	failures := make(chan error, workers)
	for worker := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for seq := range each {
				event := Event{
					Event:   EventMount,
					Phase:   PhaseSucceeded,
					Session: "dev",
					Seq:     worker*each + seq + 1,
				}
				if _, err := w.Append(event); err != nil {
					failures <- err
					return
				}
			}
		}()
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("append: %v", err)
	}
	lines := readLines(t, filepath.Join(dir, LogName))
	if len(lines) != workers*each {
		t.Fatalf("records = %d, want %d", len(lines), workers*each)
	}
	seen := make(map[int]bool, len(lines))
	for _, line := range lines {
		rec := decodeRecord(t, line)
		seq, ok := rec["seq"].(float64)
		if !ok {
			t.Fatalf("record without seq: %q", line)
		}
		seen[int(seq)] = true
	}
	if len(seen) != workers*each {
		t.Fatalf("distinct events = %d, want %d", len(seen), workers*each)
	}
}
