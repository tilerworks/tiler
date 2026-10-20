package session

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const monthAge = 31 * 24 * time.Hour

// seedClosed writes one closed record with a fixed completion time.
func seedClosed(t *testing.T, e *Engine, name string, closedAt time.Time) {
	t.Helper()
	rec := Record{
		Name:      name,
		State:     StateClosed,
		CreatedAt: closedAt.Add(-time.Hour),
		ClosedAt:  closedAt,
	}
	if err := e.Save(rec); err != nil {
		t.Fatal(err)
	}
}

// assertRecordState checks one record's state on disk.
func assertRecordState(t *testing.T, e *Engine, name, want string) {
	t.Helper()
	rec, err := e.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != want {
		t.Fatalf("%s state = %s, want %s", name, rec.State, want)
	}
}

func TestCleanupRemovesAgedClosedRecords(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedClosed(t, e, "aged", testNow.Add(-monthAge))
	seedClosed(t, e, "fresh", testNow.Add(-time.Hour))
	// The lock file of the aged record goes with it.
	if err := os.WriteFile(filepath.Join(dir, "aged", "session.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	events, err := e.Cleanup(720*time.Hour, false, testNow, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Session != "aged" || events[0].Action != CleanupRemoved {
		t.Fatalf("events = %+v", events)
	}
	if _, err := os.Stat(filepath.Join(dir, "aged")); !os.IsNotExist(err) {
		t.Errorf("aged session dir survived: %v", err)
	}
	assertRecordState(t, e, "fresh", StateClosed)
}

func TestCleanupSeesRemovableAlreadyPurgedByLastUmount(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	e.Health = func(string) bool { return false }
	const name = "happy-fox"
	seedRemovableMounted(t, e, name, "/ws/one")
	closed, err := e.CloseRemovableFor("/ws/one", func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if closed != name {
		t.Fatalf("closed %q, want %q", closed, name)
	}
	// --rm last-view close purges immediately, so age-based Cleanup
	// has nothing left to remove.
	if _, err := e.Load(name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load after --rm close = %v, want ErrNotFound", err)
	}
	events, err := e.Cleanup(720*time.Hour, false, testNow.Add(721*time.Hour), func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %+v, want none", events)
	}
}

func TestCleanupAgeRuleDisabledKeepsEverything(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedClosed(t, e, "aged", testNow.Add(-monthAge))
	events, err := e.Cleanup(0, true, testNow, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %+v", events)
	}
	assertRecordState(t, e, "aged", StateClosed)
}

func TestCleanupNeverDeletesActiveRecords(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	e.Health = func(string) bool { return false }
	// An ancient active record with a dead recorded mount stays on
	// disk and is reported stale, whatever the age rule says.
	action := mountAction("/ws/view")
	action.Seq, action.Status, action.Timestamp = 1, StatusMounted, testNow
	rec := Record{
		Name:      "ancient",
		State:     StateActive,
		Actions:   []Action{action},
		CreatedAt: testNow.Add(-2 * monthAge),
	}
	if err := e.Save(rec); err != nil {
		t.Fatal(err)
	}
	events, err := e.Cleanup(720*time.Hour, false, testNow, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Session != "ancient" || events[0].Action != CleanupStale {
		t.Fatalf("events = %+v", events)
	}
	assertRecordState(t, e, "ancient", StateActive)

	// A healthy mount reports nothing.
	e.Health = func(string) bool { return true }
	events, err = e.Cleanup(720*time.Hour, false, testNow, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("healthy events = %+v", events)
	}
}

func TestCleanupAutocloseFlipsAgedStaleActiveRecords(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	e.Health = func(string) bool { return false }
	action := mountAction("/ws/view")
	action.Seq, action.Status, action.Timestamp = 1, StatusMounted, testNow
	rec := Record{
		Name:      "ancient",
		State:     StateActive,
		Actions:   []Action{action},
		CreatedAt: testNow.Add(-monthAge),
	}
	if err := e.Save(rec); err != nil {
		t.Fatal(err)
	}
	// The default keeps the toggle off: report only.
	events, err := e.Cleanup(720*time.Hour, false, testNow, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != CleanupStale {
		t.Fatalf("toggle-off events = %+v", events)
	}
	assertRecordState(t, e, "ancient", StateActive)

	// Enabled: the record flips to closed, stamped with the pass
	// time, and is not deleted in the same pass.
	events, err = e.Cleanup(720*time.Hour, true, testNow, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Session != "ancient" || events[0].Action != CleanupClosed {
		t.Fatalf("toggle-on events = %+v", events)
	}
	flipped, err := e.Load("ancient")
	if err != nil {
		t.Fatal(err)
	}
	if flipped.State != StateClosed || !flipped.ClosedAt.Equal(testNow) {
		t.Fatalf("flipped record = %+v", flipped)
	}

	// A later pass past the age limit removes it.
	events, err = e.Cleanup(720*time.Hour, true, testNow.Add(monthAge), func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != CleanupRemoved {
		t.Fatalf("second pass events = %+v", events)
	}
}

func TestCleanupAutocloseKeepsHealthyAndYoungRecords(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	healthy := mountAction("/ws/healthy")
	healthy.Seq, healthy.Status, healthy.Timestamp = 1, StatusMounted, testNow
	if err := e.Save(Record{
		Name:      "healthy-old",
		State:     StateActive,
		Actions:   []Action{healthy},
		CreatedAt: testNow.Add(-monthAge),
	}); err != nil {
		t.Fatal(err)
	}
	young := mountAction("/ws/young")
	young.Seq, young.Status, young.Timestamp = 1, StatusMounted, testNow
	if err := e.Save(Record{
		Name:      "stale-young",
		State:     StateActive,
		Actions:   []Action{young},
		CreatedAt: testNow.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	e.Health = func(point string) bool { return point == "/ws/healthy" }
	events, err := e.Cleanup(720*time.Hour, true, testNow, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Session != "stale-young" || events[0].Action != CleanupStale {
		t.Fatalf("events = %+v", events)
	}
	assertRecordState(t, e, "healthy-old", StateActive)
	assertRecordState(t, e, "stale-young", StateActive)
}

func TestCleanupEmptyActiveRecords(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.New("fresh"); err != nil {
		t.Fatal(err)
	}
	// Toggle off: an empty aged session has nothing to recover and
	// nothing to report.
	events, err := e.Cleanup(720*time.Hour, false, testNow.Add(2*monthAge), func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("toggle-off events = %+v", events)
	}
	// Toggle on: the abandoned session flips to closed.
	events, err = e.Cleanup(720*time.Hour, true, testNow.Add(2*monthAge), func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != CleanupClosed {
		t.Fatalf("an aged empty session should autoclose: %+v", events)
	}
}

func TestCleanupSkipsHeldRecords(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("advisory locking needs linux")
	}
	dir := t.TempDir()
	e := newEngine(dir)
	seedClosed(t, e, "aged", testNow.Add(-monthAge))
	release, err := e.lockSession("aged", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	events, err := e.Cleanup(720*time.Hour, false, testNow, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("a held record was cleaned: %+v", events)
	}
	assertRecordState(t, e, "aged", StateClosed)
}

func TestCleanupPurgesStaleRemovableRecord(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	e.Health = func(string) bool { return false }
	action := mountAction("/ws/view")
	action.Seq, action.Status, action.Timestamp = 1, StatusMounted, testNow
	if err := e.Save(Record{
		Name:          "rmdev",
		State:         StateActive,
		Actions:       []Action{action},
		CreatedAt:     testNow.Add(-time.Hour),
		RemoveOnClose: true,
	}); err != nil {
		t.Fatal(err)
	}
	var purged []string
	e.RemoveRuntime = func(point string) error {
		purged = append(purged, point)
		return nil
	}
	// The --rm purge is age-independent: a young record whose only
	// view is dead is torn down all the same, unlike a plain record.
	events, err := e.Cleanup(720*time.Hour, false, testNow, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Session != "rmdev" || events[0].Action != CleanupRemoved {
		t.Fatalf("events = %+v", events)
	}
	if len(purged) != 1 || purged[0] != "/ws/view" {
		t.Fatalf("purged = %v", purged)
	}
	// The record is deleted, not flipped to closed.
	if _, statErr := os.Stat(filepath.Join(dir, "rmdev")); !os.IsNotExist(statErr) {
		t.Fatalf("rmdev session dir survived: %v", statErr)
	}
}

func TestCleanupKeepsHealthyRemovableRecord(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	e.Health = func(string) bool { return true }
	action := mountAction("/ws/view")
	action.Seq, action.Status, action.Timestamp = 1, StatusMounted, testNow
	if err := e.Save(Record{
		Name:          "rmdev",
		State:         StateActive,
		Actions:       []Action{action},
		CreatedAt:     testNow.Add(-2 * monthAge),
		RemoveOnClose: true,
	}); err != nil {
		t.Fatal(err)
	}
	purged := false
	e.RemoveRuntime = func(string) error {
		purged = true
		return nil
	}
	// A --rm session with a live view is never purged, whatever its
	// age: in-use data is off limits even for an opt-in removal.
	events, err := e.Cleanup(720*time.Hour, true, testNow, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("a healthy --rm record was cleaned: %+v", events)
	}
	if purged {
		t.Fatal("a healthy --rm record was purged")
	}
	assertRecordState(t, e, "rmdev", StateActive)
}

func TestCleanupSkipsHeldRemovableRecord(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("advisory locking needs linux")
	}
	dir := t.TempDir()
	e := newEngine(dir)
	e.Health = func(string) bool { return false }
	action := mountAction("/ws/view")
	action.Seq, action.Status, action.Timestamp = 1, StatusMounted, testNow
	if err := e.Save(Record{
		Name:          "rmdev",
		State:         StateActive,
		Actions:       []Action{action},
		CreatedAt:     testNow.Add(-time.Hour),
		RemoveOnClose: true,
	}); err != nil {
		t.Fatal(err)
	}
	release, err := e.lockSession("rmdev", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	purged := false
	e.RemoveRuntime = func(string) error {
		purged = true
		return nil
	}
	// A record another process holds is skipped this round, so an
	// in-flight --rm session is never purged under its owner.
	events, err := e.Cleanup(720*time.Hour, false, testNow, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("a held --rm record was cleaned: %+v", events)
	}
	if purged {
		t.Fatal("a held --rm record was purged")
	}
	assertRecordState(t, e, "rmdev", StateActive)
}

func TestCleanupToleratesRemovedRecords(t *testing.T) {
	// A record a concurrent pass removed between the listing and
	// the lock produces no event and no error.
	dir := t.TempDir()
	e := newEngine(dir)
	event, err := e.removeExpired("absent", 720*time.Hour, testNow)
	if err != nil || event != nil {
		t.Fatalf("removeExpired = %+v (%v)", event, err)
	}
	event, err = e.closeStale("absent", testNow, func(string) bool { return false })
	if err != nil || event != nil {
		t.Fatalf("closeStale = %+v (%v)", event, err)
	}
}

func TestCleanupReportsCorruptRecords(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedClosed(t, e, "good", testNow.Add(-time.Hour))
	if err := os.MkdirAll(filepath.Join(dir, "broken"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken", "session.json"), []byte(":::"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := e.Cleanup(720*time.Hour, false, testNow, func(string) error { return nil })
	if err == nil {
		t.Fatal("a corrupt record was silently dropped")
	}
	assertRecordState(t, e, "good", StateClosed)
}
