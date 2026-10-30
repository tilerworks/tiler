package activity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// paddedRecord builds one valid record line of exactly size bytes,
// newline included.
func paddedRecord(t *testing.T, size, seq int) []byte {
	t.Helper()
	const prefix = `{"time":"2026-01-01T00:00:00Z","event":"mount","phase":"started","pad":"`
	suffix := fmt.Sprintf(`","seq":%d}`, seq)
	pad := size - 1 - len(prefix) - len(suffix)
	if pad < 0 {
		t.Fatalf("size %d cannot hold a record with seq %d", size, seq)
	}
	line := prefix + strings.Repeat("x", pad) + suffix + "\n"
	if len(line) != size {
		t.Fatalf("record size = %d, want %d", len(line), size)
	}
	if !json.Valid([]byte(line[:len(line)-1])) {
		t.Fatalf("record is not valid JSON: %q", line)
	}
	return []byte(line)
}

// writeLog fills the activity log with prebuilt records.
func writeLog(t *testing.T, dir string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, testLogName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// logEntries returns the sorted entry names of the object directory.
func logEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// lastSeq returns the seq of the newest record.
func lastSeq(t *testing.T, lines []string) int {
	t.Helper()
	var rec struct {
		Seq int `json:"seq"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &rec); err != nil {
		t.Fatalf("last record %q: %v", lines[len(lines)-1], err)
	}
	return rec.Seq
}

// requireContiguousSuffix requires seqs to be a contiguous ascending
// run that ends at newest, so compaction keeps the newest old history
// whole instead of dropping records from the middle of the suffix.
func requireContiguousSuffix(t *testing.T, seqs []int, newest int) {
	t.Helper()
	if len(seqs) == 0 {
		t.Fatal("no record survived compaction")
	}
	if got := seqs[len(seqs)-1]; got != newest {
		t.Fatalf("kept suffix ends at seq %d, want %d", got, newest)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Fatalf("kept seqs %v are not contiguous", seqs)
		}
	}
}

// keptSeqs returns the seq of every kept record in file order.
func keptSeqs(t *testing.T, lines []string) []int {
	t.Helper()
	seqs := make([]int, 0, len(lines))
	for _, line := range lines {
		var rec struct {
			Seq int `json:"seq"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("record %q: %v", line, err)
		}
		seqs = append(seqs, rec.Seq)
	}
	return seqs
}

// TestCompactionPreservesLegacyLineBytes copies an old line unchanged
// and appends a version-1 record after it.
func TestCompactionPreservesLegacyLineBytes(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	w.maxBytes, w.keepBytes = 250, 150
	first := paddedRecord(t, 120, 1)
	second := paddedRecord(t, 120, 2)
	if bytes.Contains(second, []byte(`"version"`)) {
		t.Fatalf("fixture = %q, want a legacy line", second)
	}
	writeLog(t, dir, append(append([]byte{}, first...), second...))
	if _, err := w.Append(Event{
		Event:   EventMount,
		Phase:   PhaseSucceeded,
		Version: "abc",
		Seq:     3,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, testLogName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(content, second) {
		t.Fatalf("legacy bytes changed: %q", content)
	}
	fresh := string(content[len(second):])
	if !strings.HasPrefix(fresh, `{"version":1,`) || !strings.Contains(fresh, `"image_version":"abc"`) {
		t.Fatalf("new line = %q", fresh)
	}
}

// TestAppendCompactsAtExactLimit starts from a log that is exactly
// MaxLogBytes and proves the next append compacts and stays bounded.
func TestAppendCompactsAtExactLimit(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	var data []byte
	const records = MaxLogBytes / MaxLineBytes
	for seq := 1; seq <= records; seq++ {
		data = append(data, paddedRecord(t, MaxLineBytes, seq)...)
	}
	if len(data) != MaxLogBytes {
		t.Fatalf("prefilled log = %d bytes, want %d", len(data), MaxLogBytes)
	}
	writeLog(t, dir, data)
	if _, err := w.Append(Event{Event: EventMount, Phase: PhaseSucceeded, Seq: 99}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, testLogName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > MaxLogBytes {
		t.Fatalf("log = %d bytes, want at most %d", info.Size(), MaxLogBytes)
	}
	if info.Size() > CompactKeepBytes+MaxLineBytes {
		t.Fatalf("log was not compacted: %d bytes", info.Size())
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("compacted log mode = %o, want 600", perm)
	}
	lines := readLines(t, filepath.Join(dir, testLogName))
	if got := lastSeq(t, lines); got != 99 {
		t.Fatalf("newest record seq = %d, want the new event", got)
	}
	requireContiguousSuffix(t, keptSeqs(t, lines)[:len(lines)-1], records)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `"seq":`+fmt.Sprint(records)+`}`) {
		t.Fatalf("compaction dropped the newest old record from %d bytes of history", len(data))
	}
	want := []string{testLogName, testLockName}
	if got := logEntries(t, dir); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("object directory = %v, want %v: no rotation history", got, want)
	}
}

// TestAppendBelowLimitKeepsTheLog proves a log with room is appended
// in place, without a replacement.
func TestAppendBelowLimitKeepsTheLog(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	var data []byte
	for seq := 1; seq <= 3; seq++ {
		data = append(data, paddedRecord(t, 200, seq)...)
	}
	writeLog(t, dir, data)
	before, err := os.Stat(filepath.Join(dir, testLogName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(Event{Event: EventMount, Phase: PhaseSucceeded, Seq: 4}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	after, err := os.Stat(filepath.Join(dir, testLogName))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("the log was replaced although it had room")
	}
	lines := readLines(t, filepath.Join(dir, testLogName))
	if len(lines) != 4 || lastSeq(t, lines) != 4 {
		t.Fatalf("records = %d with newest %d, want 4 with newest 4", len(lines), lastSeq(t, lines))
	}
}

// TestAppendCompactsLargePreexistingLog proves a history far larger
// than the limit is reduced to a bounded, parseable suffix.
func TestAppendCompactsLargePreexistingLog(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	var data []byte
	const records = 5000
	for seq := 1; seq <= records; seq++ {
		data = append(data, paddedRecord(t, 400, seq)...)
	}
	if len(data) < 2*MaxLogBytes {
		t.Fatalf("prefilled log = %d bytes, want a large history", len(data))
	}
	writeLog(t, dir, data)
	if _, err := w.Append(Event{Event: EventMount, Phase: PhaseSucceeded, Seq: 7}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, testLogName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > CompactKeepBytes+MaxLineBytes {
		t.Fatalf("log = %d bytes, want a bounded suffix", info.Size())
	}
	lines := readLines(t, filepath.Join(dir, testLogName))
	if got := lastSeq(t, lines); got != 7 {
		t.Fatalf("newest record seq = %d, want 7", got)
	}
	requireContiguousSuffix(t, keptSeqs(t, lines)[:len(lines)-1], records)
	if !strings.Contains(strings.Join(lines, "\n"), `"seq":`+fmt.Sprint(records)+`}`) {
		t.Fatal("the newest old record was dropped")
	}
	for _, name := range logEntries(t, dir) {
		if strings.HasPrefix(name, ".") {
			t.Fatalf("leftover scratch file %q", name)
		}
	}
}

// TestSuffixRecordsDropsUnterminatedTail keeps only complete records
// and always the newest one.
func TestSuffixRecordsDropsUnterminatedTail(t *testing.T) {
	complete := string(paddedRecord(t, 100, 1)) + string(paddedRecord(t, 100, 2))
	torn := complete + `{"time":"2026-01-01T00:00:00Z","event":"mount"`
	got := suffixRecords([]byte(torn), 150)
	want := paddedRecord(t, 100, 2)
	if string(got) != string(want) {
		t.Fatalf("suffix = %q, want the newest complete record", got)
	}
	if got := suffixRecords([]byte(`{"torn":`), 100); got != nil {
		t.Fatalf("suffix = %q, want nothing without a complete record", got)
	}
	if got := suffixRecords([]byte(complete), 1); string(got) != string(paddedRecord(t, 100, 2)) {
		t.Fatalf("suffix = %q, want the newest record even past the limit", got)
	}
}

// fakeTemp stands in for the compaction scratch file so tests can
// inject write, sync, and close failures.
type fakeTemp struct {
	name     string
	data     []byte
	write    func([]byte) (int, error)
	syncErr  error
	closeErr error
}

func (f *fakeTemp) Write(p []byte) (int, error) {
	if f.write != nil {
		return f.write(p)
	}
	f.data = append(f.data, p...)
	return len(p), nil
}

// Name reports the scratch path the writer must clean up.
func (f *fakeTemp) Name() string { return f.name }

// Sync reports the injected failure.
func (f *fakeTemp) Sync() error { return f.syncErr }

// Close reports the injected failure.
func (f *fakeTemp) Close() error { return f.closeErr }

// TestAppendCompactionFailureKeepsLog proves every failed replacement
// step surfaces an error and leaves the previous log untouched.
func TestAppendCompactionFailureKeepsLog(t *testing.T) {
	cases := map[string]func(dir string) func(w *Writer){
		"create temp": func(string) func(*Writer) {
			return func(w *Writer) {
				w.makeTemp = func() (tempFile, error) { return nil, errBoom }
			}
		},
		"short write": func(dir string) func(*Writer) {
			return func(w *Writer) {
				w.makeTemp = func() (tempFile, error) {
					return &fakeTemp{name: filepath.Join(dir, ".tmp"), write: func(p []byte) (int, error) {
						return len(p) - 1, nil
					}}, nil
				}
			}
		},
		"sync": func(dir string) func(*Writer) {
			return func(w *Writer) {
				w.makeTemp = func() (tempFile, error) {
					return &fakeTemp{name: filepath.Join(dir, ".tmp"), syncErr: errBoom}, nil
				}
			}
		},
		"close": func(dir string) func(*Writer) {
			return func(w *Writer) {
				w.makeTemp = func() (tempFile, error) {
					return &fakeTemp{name: filepath.Join(dir, ".tmp"), closeErr: errBoom}, nil
				}
			}
		},
		"rename": func(string) func(*Writer) {
			return func(w *Writer) {
				w.rename = func(string, string) error { return errBoom }
			}
		},
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			w, dir := openTestWriter(t, fixedClock(testTime))
			w.maxBytes, w.keepBytes = 512, 256
			var data []byte
			for seq := 1; seq <= 4; seq++ {
				data = append(data, paddedRecord(t, 200, seq)...)
			}
			writeLog(t, dir, data)
			prepare(dir)(w)
			if _, err := w.Append(Event{Event: EventMount, Phase: PhaseSucceeded, Seq: 5}); !errors.Is(err, errBoom) && !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("err = %v, want the injected failure", err)
			}
			content, err := os.ReadFile(filepath.Join(dir, testLogName))
			if err != nil {
				t.Fatal(err)
			}
			if string(content) != string(data) {
				t.Fatal("a failed compaction changed the log")
			}
			for _, entry := range logEntries(t, dir) {
				if strings.HasPrefix(entry, ".") {
					t.Fatalf("leftover scratch file %q", entry)
				}
			}
		})
	}
}

// TestAppendRotationStaysBoundedUnderConcurrency rotates the log
// while many appenders write and requires a bounded, parseable file.
func TestAppendRotationStaysBoundedUnderConcurrency(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	w.maxBytes, w.keepBytes = 4096, 2048
	const workers, each = 4, 40
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
	info, err := os.Stat(filepath.Join(dir, testLogName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > int64(w.maxBytes) {
		t.Fatalf("log = %d bytes, want at most %d", info.Size(), w.maxBytes)
	}
	lines := readLines(t, filepath.Join(dir, testLogName))
	if len(lines) == 0 {
		t.Fatal("rotation removed every record")
	}
	for _, line := range lines {
		decodeRecord(t, line)
	}
	for _, entry := range logEntries(t, dir) {
		if strings.HasPrefix(entry, ".") {
			t.Fatalf("leftover scratch file %q", entry)
		}
	}
}
