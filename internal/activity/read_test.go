package activity

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/filelock"
)

// mustLine renders one valid record line for a prefilled log.
func mustLine(t *testing.T, at time.Time, seq int) string {
	t.Helper()
	line, err := encodeLine(Event{Event: EventMount, Phase: PhaseSucceeded, Seq: seq}, at)
	if err != nil {
		t.Fatalf("encodeLine: %v", err)
	}
	return string(line)
}

// TestLastActivityReturnsNewestRecordTime reads the newest record of
// a multi-record log, not its mtime or its oldest activity.
func TestLastActivityReturnsNewestRecordTime(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	newest := testTime.Add(-time.Hour)
	writeLog(t, dir, []byte(
		mustLine(t, testTime.Add(-72*time.Hour), 1)+
			mustLine(t, testTime.Add(-24*time.Hour), 2)+
			mustLine(t, newest, 3),
	))
	got, err := w.LastActivity()
	if err != nil {
		t.Fatalf("LastActivity: %v", err)
	}
	if !got.Equal(newest) {
		t.Fatalf("last activity = %v, want %v", got, newest)
	}
}

// TestLastActivityReportsMissingAndEmptyLog asks the caller to
// initialize instead of treating absence as old activity.
func TestLastActivityReportsMissingAndEmptyLog(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	if _, err := w.LastActivity(); !errors.Is(err, ErrNoActivity) {
		t.Fatalf("missing log: err = %v, want ErrNoActivity", err)
	}
	if err := os.WriteFile(filepath.Join(dir, LogName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.LastActivity(); !errors.Is(err, ErrNoActivity) {
		t.Fatalf("empty log: err = %v, want ErrNoActivity", err)
	}
}

// TestLastActivityRejectsDamagedTails never falls back to an older
// record and never lets an append hide the damage.
func TestLastActivityRejectsDamagedTails(t *testing.T) {
	valid := mustLine(t, testTime.Add(-time.Hour), 1)
	cases := map[string]string{
		"unterminated tail":   valid + `{"time":"2026-10-28T09:15:30Z","event":"mount"`,
		"not json":            valid + "{oops}\n",
		"missing time":        valid + `{"event":"mount","phase":"started"}` + "\n",
		"invalid time":        valid + `{"time":"yesterday","event":"mount","phase":"started"}` + "\n",
		"missing event":       valid + `{"time":"2026-10-28T09:15:30Z","phase":"started"}` + "\n",
		"unknown phase":       valid + `{"time":"2026-10-28T09:15:30Z","event":"mount","phase":"start"}` + "\n",
		"format version 2":    valid + `{"version":2,"time":"2026-10-28T09:15:30Z","event":"mount","phase":"started"}` + "\n",
		"format version 0":    valid + `{"version":0,"time":"2026-10-28T09:15:30Z","event":"mount","phase":"started"}` + "\n",
		"format version 1.5":  valid + `{"version":1.5,"time":"2026-10-28T09:15:30Z","event":"mount","phase":"started"}` + "\n",
		"format version bool": valid + `{"version":true,"time":"2026-10-28T09:15:30Z","event":"mount","phase":"started"}` + "\n",
		"format version null": valid + `{"version":null,"time":"2026-10-28T09:15:30Z","event":"mount","phase":"started"}` + "\n",
		"empty line":          valid + "\n",
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			w, dir := openTestWriter(t, fixedClock(testTime))
			writeLog(t, dir, []byte(data))
			if _, err := w.LastActivity(); !errors.Is(err, ErrDamagedLog) {
				t.Fatalf("LastActivity: err = %v, want ErrDamagedLog", err)
			}
			if _, err := w.Append(Event{Event: EventMount, Phase: PhaseStarted}); !errors.Is(err, ErrDamagedLog) {
				t.Fatalf("Append: err = %v, want ErrDamagedLog", err)
			}
			after, err := os.ReadFile(filepath.Join(dir, LogName))
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != data {
				t.Fatal("a damaged tail was rewritten")
			}
		})
	}
}

// TestLastActivityReadsLegacyTail keeps the time of an old line and
// restores a string version as the image version id.
func TestLastActivityReadsLegacyTail(t *testing.T) {
	const id = "old-image-id"
	at := testTime.Add(-2 * time.Hour)
	cases := map[string]string{
		"string version":  `{"time":"` + at.Format(time.RFC3339Nano) + `","event":"mount","phase":"succeeded","version":"` + id + `"}`,
		"missing version": `{"time":"` + at.Format(time.RFC3339Nano) + `","event":"mount","phase":"succeeded"}`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			w, dir := openTestWriter(t, fixedClock(testTime))
			writeLog(t, dir, []byte(line+"\n"))
			got, err := w.LastActivity()
			if err != nil {
				t.Fatalf("LastActivity: %v", err)
			}
			if !got.Equal(at) {
				t.Fatalf("last activity = %v, want %v", got, at)
			}
			rec, err := decodeLine([]byte(line))
			if err != nil {
				t.Fatalf("decodeLine: %v", err)
			}
			wantID := ""
			if name == "string version" {
				wantID = id
			}
			if rec.ImageVersion != wantID {
				t.Fatalf("image version = %q, want %q", rec.ImageVersion, wantID)
			}
		})
	}
}

// TestLastActivityHandlesLongHistory reads the newest record of a
// long history and reports its time instead of the newer mtime.
func TestLastActivityHandlesLongHistory(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	var data []byte
	const records = 5000
	for seq := 1; seq <= records; seq++ {
		data = append(data, paddedRecord(t, 400, seq)...)
	}
	newest := testTime.Add(-time.Hour)
	data = append(data, mustLine(t, newest, records+1)...)
	if len(data) < 2*MaxLogBytes {
		t.Fatalf("history = %d bytes, want a long log", len(data))
	}
	writeLog(t, dir, data)
	if err := os.Chtimes(filepath.Join(dir, LogName), testTime, testTime); err != nil {
		t.Fatal(err)
	}
	got, err := w.LastActivity()
	if err != nil {
		t.Fatalf("LastActivity: %v", err)
	}
	if !got.Equal(newest) {
		t.Fatalf("last activity = %v, want the record time %v, not the mtime %v", got, newest, testTime)
	}
}

// TestLastActivityRejectsUnsafeLog refuses to follow a symlinked log.
func TestLastActivityRejectsUnsafeLog(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	target := filepath.Join(t.TempDir(), "target.log")
	if err := os.WriteFile(target, []byte(mustLine(t, testTime, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, LogName)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.LastActivity(); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("err = %v, want ErrUnsafeFile", err)
	}
}

// TestLastActivityReportsLockContention never reads without the lock.
func TestLastActivityReportsLockContention(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	writeLog(t, dir, []byte(mustLine(t, testTime, 1)))
	file, err := os.OpenFile(filepath.Join(dir, LockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := filelock.Acquire(file, time.Second); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	w.lockTimeout = 50 * time.Millisecond
	if _, err := w.LastActivity(); !errors.Is(err, filelock.ErrTimeout) {
		t.Fatalf("err = %v, want a lock timeout", err)
	}
}

// TestAppendKeepsEventTimeNondecreasing records the newest existing
// time when the clock moves backwards.
func TestAppendKeepsEventTimeNondecreasing(t *testing.T) {
	now := testTime
	w, dir := openTestWriter(t, func() time.Time { return now })
	if _, err := w.Append(Event{Event: EventMount, Phase: PhaseSucceeded}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	now = testTime.Add(-time.Hour)
	got, err := w.Append(Event{Event: EventUnmount, Phase: PhaseSucceeded})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if !got.Equal(testTime) {
		t.Fatalf("backward clock recorded %v, want %v", got, testTime)
	}
	lines := readLines(t, filepath.Join(dir, LogName))
	last := decodeRecord(t, lines[len(lines)-1])
	stamp, err := time.Parse(time.RFC3339Nano, last["time"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if !stamp.Equal(testTime) {
		t.Fatalf("newest record time = %v, want %v", stamp, testTime)
	}
}

// TestAppendKeepsFutureRecordTime keeps a future record time until
// the clock catches up, so that object can never look expired.
func TestAppendKeepsFutureRecordTime(t *testing.T) {
	future := testTime.Add(24 * time.Hour)
	now := future
	w, _ := openTestWriter(t, func() time.Time { return now })
	if _, err := w.Append(Event{Event: EventMount, Phase: PhaseSucceeded}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	now = testTime
	got, err := w.Append(Event{Event: EventUnmount, Phase: PhaseSucceeded})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if !got.Equal(future) {
		t.Fatalf("recorded %v, want the future time %v kept", got, future)
	}
}

// TestEnsureInitializedWritesOnceAtCurrentTime initializes a missing
// log once and reports the marker only on that scan.
func TestEnsureInitializedWritesOnceAtCurrentTime(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	ev := Event{Event: EventInitialize, Phase: PhaseInitialized}
	initialized, at, err := w.EnsureInitialized(ev)
	if err != nil {
		t.Fatalf("EnsureInitialized: %v", err)
	}
	if !initialized {
		t.Fatal("the first scan did not report an initialization")
	}
	if !at.Equal(testTime) {
		t.Fatalf("initialized at %v, want %v", at, testTime)
	}
	lines := readLines(t, filepath.Join(dir, LogName))
	if len(lines) != 1 {
		t.Fatalf("records = %d, want 1", len(lines))
	}
	if rec := decodeRecord(t, lines[0]); rec["event"] != EventInitialize || rec["phase"] != PhaseInitialized {
		t.Fatalf("record = %v", rec)
	}
	initialized, at, err = w.EnsureInitialized(ev)
	if err != nil {
		t.Fatalf("second EnsureInitialized: %v", err)
	}
	if initialized {
		t.Fatal("a repeated scan renewed an initialized log")
	}
	if !at.Equal(testTime) {
		t.Fatalf("second scan time = %v, want %v", at, testTime)
	}
	if lines := readLines(t, filepath.Join(dir, LogName)); len(lines) != 1 {
		t.Fatalf("records = %d after the second scan, want 1", len(lines))
	}
}

// TestEnsureInitializedKeepsExistingLog treats any complete record as
// existing activity and never rewrites the log.
func TestEnsureInitializedKeepsExistingLog(t *testing.T) {
	old := testTime.Add(-500 * 24 * time.Hour)
	w, dir := openTestWriter(t, fixedClock(testTime))
	data := []byte(mustLine(t, old, 1))
	writeLog(t, dir, data)
	before, err := os.Stat(filepath.Join(dir, LogName))
	if err != nil {
		t.Fatal(err)
	}
	initialized, at, err := w.EnsureInitialized(Event{Event: EventInitialize, Phase: PhaseInitialized})
	if err != nil {
		t.Fatalf("EnsureInitialized: %v", err)
	}
	if initialized {
		t.Fatal("an existing log was initialized again")
	}
	if !at.Equal(old) {
		t.Fatalf("activity = %v, want %v", at, old)
	}
	after, err := os.Stat(filepath.Join(dir, LogName))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("the log was replaced")
	}
	content, err := os.ReadFile(filepath.Join(dir, LogName))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(data) {
		t.Fatal("an existing log was rewritten")
	}
}

// TestEnsureInitializedInitializesEmptyLog treats a zero-length log
// as a missing one.
func TestEnsureInitializedInitializesEmptyLog(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	if err := os.WriteFile(filepath.Join(dir, LogName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	initialized, at, err := w.EnsureInitialized(Event{Event: EventInitialize, Phase: PhaseInitialized})
	if err != nil {
		t.Fatalf("EnsureInitialized: %v", err)
	}
	if !initialized || !at.Equal(testTime) {
		t.Fatalf("initialized = %v at %v, want true at %v", initialized, at, testTime)
	}
	if lines := readLines(t, filepath.Join(dir, LogName)); len(lines) != 1 {
		t.Fatalf("records = %d, want 1", len(lines))
	}
}

// TestEnsureInitializedRejectsDamagedLog never initializes over a
// damaged log and never reports activity for it.
func TestEnsureInitializedRejectsDamagedLog(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	data := []byte("{truncated")
	writeLog(t, dir, data)
	initialized, _, err := w.EnsureInitialized(Event{Event: EventInitialize, Phase: PhaseInitialized})
	if !errors.Is(err, ErrDamagedLog) {
		t.Fatalf("err = %v, want ErrDamagedLog", err)
	}
	if initialized {
		t.Fatal("a damaged log was reported as initialized")
	}
	content, err := os.ReadFile(filepath.Join(dir, LogName))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(data) {
		t.Fatal("a damaged log was rewritten")
	}
}

// TestEnsureInitializedNeverRecreatesDirectory fails on a deleted
// object directory instead of recreating the object.
func TestEnsureInitializedNeverRecreatesDirectory(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.EnsureInitialized(Event{Event: EventInitialize, Phase: PhaseInitialized}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want a missing directory error", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("object directory was recreated: %v", err)
	}
}

// TestEnsureInitializedRejectsInvalidEvent refuses an unnamed event
// without touching the log.
func TestEnsureInitializedRejectsInvalidEvent(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	if _, _, err := w.EnsureInitialized(Event{Phase: PhaseInitialized}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("err = %v, want ErrInvalidEvent", err)
	}
	if _, err := os.Stat(filepath.Join(dir, LogName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("log exists after a rejected event: %v", err)
	}
}

// TestEnsureInitializedConcurrentScansInitializeOnce proves the
// inspect-then-write pair is atomic.
func TestEnsureInitializedConcurrentScansInitializeOnce(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	const workers = 8
	var wait sync.WaitGroup
	results := make(chan bool, workers)
	failures := make(chan error, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			initialized, _, err := w.EnsureInitialized(Event{Event: EventInitialize, Phase: PhaseInitialized})
			if err != nil {
				failures <- err
				return
			}
			results <- initialized
		}()
	}
	wait.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatalf("EnsureInitialized: %v", err)
	}
	count := 0
	for initialized := range results {
		if initialized {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("initializations = %d, want exactly 1", count)
	}
	if lines := readLines(t, filepath.Join(dir, LogName)); len(lines) != 1 {
		t.Fatalf("records = %d, want 1", len(lines))
	}
}

// TestRevisionPinsTheNewestRecord keeps an expiry snapshot tied to the
// record it saw: an unchanged log reports the same revision, and one
// appended or replaced record changes the digest.
func TestRevisionPinsTheNewestRecord(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	ev := Event{Event: EventInitialize, Phase: PhaseInitialized}
	initialized, first, err := w.Inspect(ev)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !initialized || first.Digest == "" || !first.Last.Equal(testTime) {
		t.Fatalf("Inspect = %v %+v, want one initialized record at %v", initialized, first, testTime)
	}

	again, err := w.Revision()
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	if again != first {
		t.Fatalf("revision = %+v, want the unchanged %+v", again, first)
	}

	if _, err := w.Append(Event{Event: EventSessionOpen, Phase: PhaseSucceeded}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	after, err := w.Revision()
	if err != nil {
		t.Fatalf("Revision after append: %v", err)
	}
	if after.Digest == first.Digest {
		t.Fatalf("digest %q survived an append", after.Digest)
	}
	if !after.Last.After(first.Last) && !after.Last.Equal(first.Last) {
		t.Fatalf("revision time %v went back from %v", after.Last, first.Last)
	}

	last, err := w.LastActivity()
	if err != nil {
		t.Fatalf("LastActivity: %v", err)
	}
	if !last.Equal(after.Last) {
		t.Fatalf("LastActivity = %v, want the revision time %v", last, after.Last)
	}
	if lines := readLines(t, filepath.Join(dir, LogName)); len(lines) != 2 {
		t.Fatalf("records = %d, want 2", len(lines))
	}
}

// TestRevisionReportsMissingAndDamagedLogs never invents activity for
// a log a scan cannot read.
func TestRevisionReportsMissingAndDamagedLogs(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	if _, err := w.Revision(); !errors.Is(err, ErrNoActivity) {
		t.Fatalf("missing log: err = %v, want ErrNoActivity", err)
	}
	writeLog(t, dir, []byte("{truncated"))
	if _, err := w.Revision(); !errors.Is(err, ErrDamagedLog) {
		t.Fatalf("damaged log: err = %v, want ErrDamagedLog", err)
	}
}
