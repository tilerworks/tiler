package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
)

// activityRecord is one decoded session activity record.
type activityRecord struct {
	Time       string `json:"time"`
	Event      string `json:"event"`
	Phase      string `json:"phase"`
	Session    string `json:"session"`
	Seq        int    `json:"seq"`
	Mountpoint string `json:"mountpoint"`
	Error      string `json:"error"`
}

// readActivityLog decodes the activity log of one session; a missing
// log reads as no records.
func readActivityLog(t *testing.T, dir, name string) []activityRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name, activity.LogName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read activity log: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	records := make([]activityRecord, 0, len(lines))
	for _, line := range lines {
		var rec activityRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("activity record %q: %v", line, err)
		}
		records = append(records, rec)
	}
	return records
}

// activityPhases renders event/phase pairs in file order.
func activityPhases(records []activityRecord) string {
	pairs := make([]string, 0, len(records))
	for _, rec := range records {
		pairs = append(pairs, rec.Event+"/"+rec.Phase)
	}
	return strings.Join(pairs, ",")
}

// TestNewRecordsOneCreateEvent writes exactly one create activity
// record for a session, stamped with the injected clock.
func TestNewRecordsOneCreateEvent(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.New("dev"); err != nil {
		t.Fatalf("New: %v", err)
	}
	records := readActivityLog(t, dir, "dev")
	want := activity.EventSessionCreate + "/" + activity.PhaseSucceeded
	if got := activityPhases(records); got != want {
		t.Fatalf("phases = %q, want %q", got, want)
	}
	if records[0].Session != "dev" {
		t.Fatalf("session = %q, want dev", records[0].Session)
	}
	stamp, err := time.Parse(time.RFC3339Nano, records[0].Time)
	if err != nil {
		t.Fatal(err)
	}
	if !stamp.Equal(testNow) {
		t.Fatalf("time = %v, want %v", stamp, testNow)
	}
}

// TestNewRemovableRecordsOneCreateEvent records a --rm creation the
// same way as a kept one.
func TestNewRemovableRecordsOneCreateEvent(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.NewRemovable("tmp"); err != nil {
		t.Fatalf("NewRemovable: %v", err)
	}
	want := activity.EventSessionCreate + "/" + activity.PhaseSucceeded
	if got := activityPhases(readActivityLog(t, dir, "tmp")); got != want {
		t.Fatalf("phases = %q, want %q", got, want)
	}
}

// TestDuplicateNewDoesNotRenewActivity keeps a refused create from
// refreshing the log of the session that already owns the name.
func TestDuplicateNewDoesNotRenewActivity(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	before := readActivityLog(t, dir, "dev")
	if err := e.New("dev"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate New err = %v, want ErrExists", err)
	}
	after := readActivityLog(t, dir, "dev")
	if len(after) != len(before) || after[len(after)-1].Time != before[len(before)-1].Time {
		t.Fatalf("a refused create renewed the log: %q -> %q",
			activityPhases(before), activityPhases(after))
	}
}

// TestOpenRecordsStartedAndSucceededForHealthyView refreshes the log
// for an explicit open even when every view is already healthy.
func TestOpenRecordsStartedAndSucceededForHealthyView(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedMounted(t, e, "dev", "/mnt/dev")
	e.Health = func(string) bool { return true }
	if err := e.Open("dev", func(Action) (Result, error) {
		t.Fatal("a healthy view must not be mounted again")
		return Result{}, nil
	}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	records := readActivityLog(t, dir, "dev")
	got := activityPhases(records[len(records)-2:])
	want := activity.EventSessionOpen + "/" + activity.PhaseStarted + "," +
		activity.EventSessionOpen + "/" + activity.PhaseSucceeded
	if got != want {
		t.Fatalf("last phases = %q, want %q", got, want)
	}
}

// TestOpenRecordsFailedPhaseWhenReplayFails keeps the diagnostic text
// of a failed open.
func TestOpenRecordsFailedPhaseWhenReplayFails(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedMounted(t, e, "dev", "/mnt/dev")
	e.Health = func(string) bool { return false }
	e.Clean = func(string) error { return nil }
	failure := errors.New("mount boom")
	err := e.Open("dev", func(Action) (Result, error) { return Result{}, failure })
	if !errors.Is(err, failure) {
		t.Fatalf("Open err = %v, want the mount failure", err)
	}
	records := readActivityLog(t, dir, "dev")
	tail := records[len(records)-2:]
	if tail[0].Event != activity.EventMount || tail[0].Phase != activity.PhaseFailed {
		t.Fatalf("replay record = %+v, want a failed replay mount", tail[0])
	}
	if tail[0].Seq != 1 || tail[0].Mountpoint != "/mnt/dev" {
		t.Fatalf("replay record = %+v, want seq 1 at /mnt/dev", tail[0])
	}
	if !strings.Contains(tail[0].Error, "mount boom") {
		t.Fatalf("replay error = %q, want the mount failure", tail[0].Error)
	}
	last := tail[1]
	if last.Event != activity.EventSessionOpen || last.Phase != activity.PhaseFailed {
		t.Fatalf("last record = %+v, want a failed open", last)
	}
	if !strings.Contains(last.Error, "mount boom") {
		t.Fatalf("failed open error = %q, want the mount failure", last.Error)
	}
}

// TestCloseRecordsStartedAndSucceededForEmptySession closes a session
// without views and still refreshes its activity.
func TestCloseRecordsStartedAndSucceededForEmptySession(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	removed, err := e.Close("dev", func(string) error {
		t.Fatal("an empty session has nothing to unmount")
		return nil
	})
	if err != nil || removed {
		t.Fatalf("Close = %v, %v, want a kept close", removed, err)
	}
	want := strings.Join([]string{
		activity.EventSessionCreate + "/" + activity.PhaseSucceeded,
		activity.EventSessionClose + "/" + activity.PhaseStarted,
		activity.EventSessionClose + "/" + activity.PhaseSucceeded,
	}, ",")
	if got := activityPhases(readActivityLog(t, dir, "dev")); got != want {
		t.Fatalf("phases = %q, want %q", got, want)
	}
}

// TestCloseOfClosedSessionWritesNothing keeps a repeated close from
// renewing the log.
func TestCloseOfClosedSessionWritesNothing(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	umount := func(string) error { return nil }
	if _, err := e.Close("dev", umount); err != nil {
		t.Fatalf("Close: %v", err)
	}
	before := readActivityLog(t, dir, "dev")
	if _, err := e.Close("dev", umount); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	after := readActivityLog(t, dir, "dev")
	if len(after) != len(before) {
		t.Fatalf("a repeated close wrote %d records, want %d",
			len(after)-len(before), 0)
	}
}

// TestCloseRemovableRemovesTheLogWithTheSession writes the outcome
// before a --rm close deletes the session directory.
func TestCloseRemovableRemovesTheLogWithTheSession(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.NewRemovable("tmp"); err != nil {
		t.Fatal(err)
	}
	removed, err := e.Close("tmp", func(string) error { return nil })
	if err != nil || !removed {
		t.Fatalf("Close = %v, %v, want a removal", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session directory survived the --rm close: %v", err)
	}
}

// TestActivityFailureAfterCreateReportsCompletedOperation keeps the
// record and reports the missing log instead of pretending the
// create failed.
func TestActivityFailureAfterCreateReportsCompletedOperation(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	boom := errors.New("log boom")
	e.Activity = func(string) (*activity.Writer, error) { return nil, boom }
	err := e.New("dev")
	if !errors.Is(err, ErrActivityFailed) {
		t.Fatalf("New err = %v, want ErrActivityFailed", err)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("New err = %v, want the log failure in the chain", err)
	}
	if _, loadErr := e.Load("dev"); loadErr != nil {
		t.Fatalf("the created record is missing: %v", loadErr)
	}
}

// TestStartedActivityFailureBlocksOpen leaves the session untouched
// when its log cannot take the started record.
func TestStartedActivityFailureBlocksOpen(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedMounted(t, e, "dev", "/mnt/dev")
	e.Activity = func(string) (*activity.Writer, error) { return nil, errors.New("log boom") }
	calls := &callLog{}
	err := e.Open("dev", func(Action) (Result, error) {
		calls.add("mount")
		return Result{}, nil
	})
	if err == nil {
		t.Fatal("Open succeeded without its started record")
	}
	if len(calls.calls) != 0 {
		t.Fatalf("mount ran after a failed started record: %v", calls.calls)
	}
	rec, loadErr := e.Load("dev")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if rec.State != StateActive || rec.Actions[0].Status != StatusMounted {
		t.Fatalf("record changed after a failed started record: %+v", rec)
	}
}

// TestStartedActivityFailureBlocksClose leaves every view mounted
// when the close started record cannot be written.
func TestStartedActivityFailureBlocksClose(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedMounted(t, e, "dev", "/mnt/dev")
	e.Activity = func(string) (*activity.Writer, error) { return nil, errors.New("log boom") }
	calls := &callLog{}
	removed, err := e.Close("dev", func(string) error {
		calls.add("umount")
		return nil
	})
	if err == nil || removed {
		t.Fatalf("Close = %v, %v, want a refused close", removed, err)
	}
	if len(calls.calls) != 0 {
		t.Fatalf("a view was torn down after a failed started record: %v", calls.calls)
	}
	rec, loadErr := e.Load("dev")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if rec.State != StateActive {
		t.Fatalf("state = %q after a refused close, want active", rec.State)
	}
}

// TestCloseRecordsFailedPhaseWhenUmountFails keeps the diagnostic
// text of a failed close and leaves the record active.
func TestCloseRecordsFailedPhaseWhenUmountFails(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedMounted(t, e, "dev", "/mnt/dev")
	failure := errors.New("umount boom")
	removed, err := e.Close("dev", func(string) error { return failure })
	if !errors.Is(err, failure) || removed {
		t.Fatalf("Close = %v, %v, want the umount failure", removed, err)
	}
	records := readActivityLog(t, dir, "dev")
	tail := records[len(records)-2:]
	if tail[0].Event != activity.EventUnmount || tail[0].Phase != activity.PhaseFailed {
		t.Fatalf("teardown record = %+v, want a failed unmount", tail[0])
	}
	if tail[0].Seq != 1 || tail[0].Mountpoint != "/mnt/dev" {
		t.Fatalf("teardown record = %+v, want seq 1 at /mnt/dev", tail[0])
	}
	if !strings.Contains(tail[0].Error, "umount boom") {
		t.Fatalf("teardown error = %q, want the umount failure", tail[0].Error)
	}
	last := tail[1]
	if last.Event != activity.EventSessionClose || last.Phase != activity.PhaseFailed {
		t.Fatalf("last record = %+v, want a failed close", last)
	}
	if !strings.Contains(last.Error, "umount boom") {
		t.Fatalf("failed close error = %q, want the umount failure", last.Error)
	}
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != StateActive {
		t.Fatalf("state = %q after a failed close, want active", rec.State)
	}
}

// TestOpenOfClosedSessionRecordsReopen records the open that brings a
// closed record back to active.
func TestOpenOfClosedSessionRecordsReopen(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedMounted(t, e, "dev", "/mnt/dev")
	if _, err := e.Close("dev", func(string) error { return nil }); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if closed.State != StateClosed {
		t.Fatalf("state = %q before reopen, want closed", closed.State)
	}
	e.Health = func(string) bool { return true }
	if err := e.Open("dev", func(Action) (Result, error) {
		t.Fatal("the healthy view must be adopted")
		return Result{}, nil
	}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	records := readActivityLog(t, dir, "dev")
	want := activity.EventSessionOpen + "/" + activity.PhaseStarted + "," +
		activity.EventSessionOpen + "/" + activity.PhaseSucceeded
	if got := activityPhases(records[len(records)-2:]); got != want {
		t.Fatalf("last phases = %q, want %q", got, want)
	}
	reopened, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.State != StateActive || !reopened.ClosedAt.IsZero() {
		t.Fatalf("record = %+v, want an active record without ClosedAt", reopened)
	}
}

// TestRunMountRecordsSeqAndMountpoint pairs every mount log line with
// the action the record stores.
func TestRunMountRecordsSeqAndMountpoint(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	point := "/mnt/dev"
	if _, err := e.RunMount("dev", mountAction(point), func(a Action) (Result, error) {
		return Result{Mountpoint: a.Mountpoint}, nil
	}); err != nil {
		t.Fatalf("RunMount: %v", err)
	}
	records := readActivityLog(t, dir, "dev")
	want := activity.EventMount + "/" + activity.PhaseStarted + "," +
		activity.EventMount + "/" + activity.PhaseSucceeded
	if got := activityPhases(records[1:]); got != want {
		t.Fatalf("phases = %q, want %q", got, want)
	}
	for _, rec := range records[1:] {
		if rec.Seq != 1 || rec.Mountpoint != point {
			t.Fatalf("record = %+v, want seq 1 at %s", rec, point)
		}
	}
}

// TestRunMountRecordsFailedPhaseWhenMountFails keeps the diagnostic
// text of a failed mount and records the failed action.
func TestRunMountRecordsFailedPhaseWhenMountFails(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("mount boom")
	if _, err := e.RunMount("dev", mountAction("/mnt/dev"), func(Action) (Result, error) {
		return Result{}, failure
	}); !errors.Is(err, failure) {
		t.Fatalf("RunMount err = %v, want the mount failure", err)
	}
	records := readActivityLog(t, dir, "dev")
	last := records[len(records)-1]
	if last.Event != activity.EventMount || last.Phase != activity.PhaseFailed {
		t.Fatalf("last record = %+v, want a failed mount", last)
	}
	if !strings.Contains(last.Error, "mount boom") {
		t.Fatalf("failed mount error = %q, want the mount failure", last.Error)
	}
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Actions[0].Status != StatusFailed {
		t.Fatalf("status = %q, want failed", rec.Actions[0].Status)
	}
}

// TestStartedMountActivityFailureLeavesTheActionPending refuses to
// mount when the started record cannot be written. The write-ahead
// action stays pending, so a later Open replays it.
func TestStartedMountActivityFailureLeavesTheActionPending(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	e.Activity = func(string) (*activity.Writer, error) { return nil, errors.New("log boom") }
	mounts := &callLog{}
	_, err := e.RunMount("dev", mountAction("/mnt/dev"), func(a Action) (Result, error) {
		mounts.add("mount %s", a.Mountpoint)
		return Result{Mountpoint: a.Mountpoint}, nil
	})
	if err == nil {
		t.Fatal("RunMount succeeded without its started record")
	}
	if len(mounts.calls) != 0 {
		t.Fatalf("mount ran after a failed started record: %v", mounts.calls)
	}
	rec, loadErr := e.Load("dev")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(rec.Actions) != 1 || rec.Actions[0].Status != StatusPending {
		t.Fatalf("actions = %+v, want one pending action", rec.Actions)
	}
}

// TestMountActivityFailureAfterMountReportsCompletedOperation keeps
// the mounted view and its writeback when only the result record
// fails.
func TestMountActivityFailureAfterMountReportsCompletedOperation(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	logDir := t.TempDir()
	e.Activity = func(string) (*activity.Writer, error) {
		return activity.OpenLog(logDir, logDir, e.Now)
	}
	point := "/mnt/dev"
	result, err := e.RunMount("dev", mountAction(point), func(a Action) (Result, error) {
		// Removing the log directory between the started and the
		// result record fails exactly the outcome append.
		if rmErr := os.RemoveAll(logDir); rmErr != nil {
			t.Fatalf("remove log directory: %v", rmErr)
		}
		return Result{Mountpoint: a.Mountpoint}, nil
	})
	if !errors.Is(err, ErrActivityFailed) {
		t.Fatalf("RunMount err = %v, want ErrActivityFailed", err)
	}
	if result.Mountpoint != point {
		t.Fatalf("mountpoint = %q, want %s", result.Mountpoint, point)
	}
	rec, loadErr := e.Load("dev")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if rec.Actions[0].Status != StatusMounted || rec.Actions[0].Mountpoint != point {
		t.Fatalf("action = %+v, want the mounted view", rec.Actions[0])
	}
}

// TestMountCallbackRecordFailureKeepsTheMountedAction keeps the view a
// mount callback reported as mounted with a lost record instead of
// writing a live action down as failed.
func TestMountCallbackRecordFailureKeepsTheMountedAction(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	point := "/mnt/dev"
	result, err := e.RunMount("dev", mountAction(point), func(a Action) (Result, error) {
		return Result{Mountpoint: a.Mountpoint}, ErrActivityFailed
	})
	if !errors.Is(err, ErrActivityFailed) {
		t.Fatalf("RunMount err = %v, want ErrActivityFailed", err)
	}
	if result.Mountpoint != point {
		t.Fatalf("mountpoint = %q, want %s", result.Mountpoint, point)
	}
	rec, loadErr := e.Load("dev")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if rec.Actions[0].Status != StatusMounted || rec.Actions[0].Mountpoint != point {
		t.Fatalf("action = %+v, want the mounted view", rec.Actions[0])
	}
}

// TestReplayRecordFailureKeepsTheMountedAction keeps a rebuilt view whose
// record is missing, and still reports the failure.
func TestReplayRecordFailureKeepsTheMountedAction(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	point := "/mnt/dev"
	if _, err := e.RunMount("dev", mountAction(point), func(Action) (Result, error) {
		return Result{}, errors.New("mount boom")
	}); err == nil {
		t.Fatal("RunMount accepted a failed mount")
	}
	e.Health = func(string) bool { return false }
	e.Clean = func(string) error { return nil }
	err := e.Open("dev", func(a Action) (Result, error) {
		return Result{Mountpoint: a.Mountpoint}, ErrActivityFailed
	})
	if !errors.Is(err, ErrActivityFailed) {
		t.Fatalf("Open err = %v, want ErrActivityFailed", err)
	}
	rec, loadErr := e.Load("dev")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if rec.Actions[0].Status != StatusMounted || rec.Actions[0].Mountpoint != point {
		t.Fatalf("action = %+v, want the rebuilt view", rec.Actions[0])
	}
}

// TestOpenReplayRecordsMountActivity logs a remount with the sequence
// of the action it rebuilt.
func TestOpenReplayRecordsMountActivity(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("mount boom")
	if _, err := e.RunMount("dev", mountAction("/mnt/dev"), func(Action) (Result, error) {
		return Result{}, failure
	}); !errors.Is(err, failure) {
		t.Fatalf("RunMount err = %v, want the mount failure", err)
	}
	e.Health = func(string) bool { return false }
	e.Clean = func(string) error { return nil }
	if err := e.Open("dev", func(a Action) (Result, error) {
		return Result{Mountpoint: a.Mountpoint}, nil
	}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	records := readActivityLog(t, dir, "dev")
	tail := records[len(records)-4:]
	want := strings.Join([]string{
		activity.EventSessionOpen + "/" + activity.PhaseStarted,
		activity.EventMount + "/" + activity.PhaseStarted,
		activity.EventMount + "/" + activity.PhaseSucceeded,
		activity.EventSessionOpen + "/" + activity.PhaseSucceeded,
	}, ",")
	if got := activityPhases(tail); got != want {
		t.Fatalf("replay phases = %q, want %q", got, want)
	}
	if tail[1].Seq != 1 || tail[1].Mountpoint != "/mnt/dev" {
		t.Fatalf("replay record = %+v, want seq 1 at /mnt/dev", tail[1])
	}
}

// TestReplayRecordsFailedPhaseWhenCleanFails keeps the stale point
// untouched and records the failure when the clean fails.
func TestReplayRecordsFailedPhaseWhenCleanFails(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedMounted(t, e, "dev", "/mnt/dev")
	e.Health = func(string) bool { return false }
	failure := errors.New("clean boom")
	e.Clean = func(string) error { return failure }
	mounts := &callLog{}
	err := e.Open("dev", func(a Action) (Result, error) {
		mounts.add("mount %s", a.Mountpoint)
		return Result{Mountpoint: a.Mountpoint}, nil
	})
	if !errors.Is(err, failure) {
		t.Fatalf("Open err = %v, want the clean failure", err)
	}
	if len(mounts.calls) != 0 {
		t.Fatalf("mount ran after a failed clean: %v", mounts.calls)
	}
	records := readActivityLog(t, dir, "dev")
	tail := records[len(records)-2:]
	if tail[0].Event != activity.EventMount || tail[0].Phase != activity.PhaseFailed {
		t.Fatalf("replay record = %+v, want a failed replay mount", tail[0])
	}
	if tail[0].Seq != 1 || !strings.Contains(tail[0].Error, "clean boom") {
		t.Fatalf("replay record = %+v, want seq 1 with the clean failure", tail[0])
	}
	if tail[1].Event != activity.EventSessionOpen || tail[1].Phase != activity.PhaseFailed {
		t.Fatalf("last record = %+v, want a failed open", tail[1])
	}
}

// TestStartedReplayActivityFailureKeepsTheStaleView refuses to clean
// a stale point when the replay started record cannot be written.
func TestStartedReplayActivityFailureKeepsTheStaleView(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	stored, err := e.Append("dev", mountAction("/mnt/dev"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Writeback("dev", stored.Seq, StatusMounted, "/mnt/dev"); err != nil {
		t.Fatal(err)
	}
	e.Health = func(string) bool { return false }
	calls := 0
	e.Activity = func(name string) (*activity.Writer, error) {
		calls++
		if calls > 1 {
			return nil, errors.New("log boom")
		}
		return activity.OpenLog(e.Dir, filepath.Join(e.Dir, name), e.Now)
	}
	seams := &callLog{}
	e.Clean = func(string) error {
		seams.add("clean")
		return nil
	}
	err = e.Open("dev", func(a Action) (Result, error) {
		seams.add("mount %s", a.Mountpoint)
		return Result{Mountpoint: a.Mountpoint}, nil
	})
	if err == nil {
		t.Fatal("Open succeeded without its replay started record")
	}
	if len(seams.calls) != 0 {
		t.Fatalf("replay side effects ran after a failed started record: %v", seams.calls)
	}
	records := readActivityLog(t, dir, "dev")
	tail := records[len(records)-2:]
	if activityPhases(tail) != activity.EventSessionOpen+"/"+activity.PhaseStarted+","+
		activity.EventSessionOpen+"/"+activity.PhaseFailed {
		t.Fatalf("phases = %q, want one failed open", activityPhases(tail))
	}
}

// TestCloseRecordsUnmountActivityInReverseOrder pairs every teardown
// with the action it took down.
func TestCloseRecordsUnmountActivityInReverseOrder(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedMounted(t, e, "dev", "/mnt/one", "/mnt/two")
	if _, err := e.Close("dev", func(string) error { return nil }); err != nil {
		t.Fatalf("Close: %v", err)
	}
	records := readActivityLog(t, dir, "dev")
	tail := records[len(records)-6:]
	want := strings.Join([]string{
		activity.EventSessionClose + "/" + activity.PhaseStarted,
		activity.EventUnmount + "/" + activity.PhaseStarted,
		activity.EventUnmount + "/" + activity.PhaseSucceeded,
		activity.EventUnmount + "/" + activity.PhaseStarted,
		activity.EventUnmount + "/" + activity.PhaseSucceeded,
		activity.EventSessionClose + "/" + activity.PhaseSucceeded,
	}, ",")
	if got := activityPhases(tail); got != want {
		t.Fatalf("close phases = %q, want %q", got, want)
	}
	if tail[1].Seq != 2 || tail[1].Mountpoint != "/mnt/two" {
		t.Fatalf("first teardown = %+v, want seq 2 at /mnt/two", tail[1])
	}
	if tail[3].Seq != 1 || tail[3].Mountpoint != "/mnt/one" {
		t.Fatalf("second teardown = %+v, want seq 1 at /mnt/one", tail[3])
	}
}

// TestStartedUnmountActivityFailureBlocksTeardown keeps every view up
// when an unmount started record cannot be written.
func TestStartedUnmountActivityFailureBlocksTeardown(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Append("dev", mountAction("/mnt/dev")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.Activity = func(name string) (*activity.Writer, error) {
		calls++
		if calls > 1 {
			return nil, errors.New("log boom")
		}
		return activity.OpenLog(e.Dir, filepath.Join(e.Dir, name), e.Now)
	}
	umounts := &callLog{}
	removed, err := e.Close("dev", func(point string) error {
		umounts.add("umount %s", point)
		return nil
	})
	if err == nil || removed {
		t.Fatalf("Close = %v, %v, want a refused close", removed, err)
	}
	if len(umounts.calls) != 0 {
		t.Fatalf("a view was torn down after a failed started record: %v", umounts.calls)
	}
	records := readActivityLog(t, dir, "dev")
	tail := records[len(records)-2:]
	want := activity.EventSessionClose + "/" + activity.PhaseStarted + "," +
		activity.EventSessionClose + "/" + activity.PhaseFailed
	if got := activityPhases(tail); got != want {
		t.Fatalf("close phases = %q, want %q", got, want)
	}
}

// TestReadOnlyOperationsLeaveTheLogAlone keeps load, show, and list
// free of activity.
func TestReadOnlyOperationsLeaveTheLogAlone(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	before := readActivityLog(t, dir, "dev")
	if _, err := e.Load("dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.List(); err != nil {
		t.Fatal(err)
	}
	after := readActivityLog(t, dir, "dev")
	if len(after) != len(before) {
		t.Fatalf("read-only operations wrote %d records", len(after)-len(before))
	}
}
