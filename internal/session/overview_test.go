package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

func TestOverviewReconcilesRecordsWithTable(t *testing.T) {
	live, dead, orphanPoint := "/ws/live", "/ws/dead", "/ws/orphan"
	table := []mountinfo.Mount{
		overlayEntry(live, "/sessions/dev/runtime/m1"),
		overlayEntry(dead, "/sessions/gone/runtime/m1"),
		overlayEntry(orphanPoint, "/sessions/orphan/runtime/m1"),
		{Point: "/foreign", FSType: "ext4", Source: "/dev/sda1"},
	}
	e, _ := lookupEngine(t, table, map[string]bool{live: true, orphanPoint: true})
	e.SessionsDir = "/sessions"
	seedMounted(t, e, "dev", live)
	seedMounted(t, e, "gone", dead)
	seedClosed(t, e, "old", testNow)

	overview, err := e.Overview("dev")
	if err != nil {
		t.Fatal(err)
	}
	if overview.Current != "dev" {
		t.Fatalf("Current = %q, want dev", overview.Current)
	}
	if len(overview.Sessions) != 3 {
		t.Fatalf("sessions = %d, want 3", len(overview.Sessions))
	}
	byName := map[string]View{}
	for _, view := range overview.Sessions {
		byName[view.Name] = view
	}

	dev := byName["dev"]
	if !dev.Current || dev.Locked || dev.State != StateActive {
		t.Fatalf("dev = %+v, want the current active record", dev)
	}
	if len(dev.Actions) != 1 || dev.Actions[0].Health != HealthHealthy {
		t.Fatalf("dev actions = %+v, want one healthy mount", dev.Actions)
	}

	gone := byName["gone"]
	if len(gone.Actions) != 1 || gone.Actions[0].Health != HealthStale {
		t.Fatalf("gone actions = %+v, want one stale mount", gone.Actions)
	}

	old := byName["old"]
	if old.State != StateClosed || old.Actions != nil {
		t.Fatalf("old = %+v, want closed without expanded actions", old)
	}

	// The foreign mount has no runtime trace and the claimed views
	// belong to records, so exactly the unclaimed combination lists.
	if len(overview.Orphans) != 1 {
		t.Fatalf("orphans = %+v, want exactly one", overview.Orphans)
	}
	orphan := overview.Orphans[0]
	if orphan.Point != orphanPoint || orphan.FSType != "fuse.fuse-overlayfs" ||
		orphan.RuntimeDir != "/sessions/orphan/runtime/m1" {
		t.Fatalf("orphan = %+v", orphan)
	}
}

func TestOverviewCurrentMismatchIsNotAnError(t *testing.T) {
	e, _ := lookupEngine(t, nil, nil)
	seedMounted(t, e, "dev", "/ws/live")

	overview, err := e.Overview("ghost")
	if err != nil {
		t.Fatal(err)
	}
	if overview.Current != "ghost" {
		t.Fatalf("Current = %q, want the verbatim environment value", overview.Current)
	}
	if len(overview.Sessions) != 1 || overview.Sessions[0].Current {
		t.Fatalf("sessions = %+v, want one unmarked record", overview.Sessions)
	}
}

func TestOverviewMarksLockedRecordsWithoutExpanding(t *testing.T) {
	e, _ := lookupEngine(t, []mountinfo.Mount{
		overlayEntry("/ws/live", "/sessions/busy/runtime/m1"),
	}, map[string]bool{"/ws/live": true})
	e.SessionsDir = "/sessions"
	seedMounted(t, e, "busy", "/ws/live")
	e.Lock = func(name string, _ time.Duration) (func(), error) {
		if name == "busy" {
			return nil, ErrLockHeld
		}
		return func() {}, nil
	}

	overview, err := e.Overview("")
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(overview.Sessions))
	}
	locked := overview.Sessions[0]
	if !locked.Locked || locked.Name != "busy" || locked.State != "" || locked.Actions != nil {
		t.Fatalf("locked view = %+v, want the name-only locked marker", locked)
	}
	// A busy record exposes no claim, so its view transiently lists
	// as an orphan while the lock is held.
	if len(overview.Orphans) != 1 || overview.Orphans[0].Point != "/ws/live" {
		t.Fatalf("orphans = %+v, want the busy record's view", overview.Orphans)
	}
}

func TestOverviewSkipsOrphansWithoutSessionsDir(t *testing.T) {
	table := []mountinfo.Mount{overlayEntry("/ws/orphan", "/sessions/orphan/runtime/m1")}
	e, _ := lookupEngine(t, table, map[string]bool{"/ws/orphan": true})
	e.SessionsDir = ""

	overview, err := e.Overview("")
	if err != nil {
		t.Fatal(err)
	}
	if overview.Orphans != nil {
		t.Fatalf("orphans = %+v, want none without a sessions root", overview.Orphans)
	}
}

func TestOverviewUnmountedActionsStayUnclaimedAndUnannotated(t *testing.T) {
	table := []mountinfo.Mount{overlayEntry("/ws/torn", "/sessions/torn/runtime/m1")}
	e, _ := lookupEngine(t, table, map[string]bool{"/ws/torn": true})
	e.SessionsDir = "/sessions"
	seedMounted(t, e, "torn", "/ws/torn")
	if err := e.Writeback("torn", 1, StatusUnmounted, ""); err != nil {
		t.Fatal(err)
	}

	overview, err := e.Overview("")
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Sessions) != 1 || len(overview.Sessions[0].Actions) != 1 {
		t.Fatalf("sessions = %+v, want the one record", overview.Sessions)
	}
	if health := overview.Sessions[0].Actions[0].Health; health != "" {
		t.Fatalf("health = %q, want no annotation on a torn-down mount", health)
	}
	if len(overview.Orphans) != 1 || overview.Orphans[0].Point != "/ws/torn" {
		t.Fatalf("orphans = %+v, want the still-mounted torn view", overview.Orphans)
	}
}

func TestOverviewEmptyStore(t *testing.T) {
	e, _ := lookupEngine(t, nil, nil)
	e.SessionsDir = "/sessions"

	overview, err := e.Overview("dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Sessions) != 0 || len(overview.Orphans) != 0 {
		t.Fatalf("overview = %+v, want empty", overview)
	}
	if overview.Current != "dev" {
		t.Fatalf("Current = %q, want dev", overview.Current)
	}
}

func TestOverviewReportsCorruptRecord(t *testing.T) {
	e, dir := lookupEngine(t, nil, nil)
	if err := os.MkdirAll(filepath.Join(dir, "broken"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken", "session.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := e.Overview(""); err == nil {
		t.Fatal("Overview succeeded, want the corrupt record reported")
	}
}

func TestOverviewRecordsRemovedAfterListingDropOut(t *testing.T) {
	e, dir := lookupEngine(t, nil, nil)
	seedMounted(t, e, "dev", "/ws/live")
	seedMounted(t, e, "vanish", "/ws/gone")
	if err := os.Remove(filepath.Join(dir, "vanish", "session.json")); err != nil {
		t.Fatal(err)
	}

	overview, err := e.Overview("")
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Sessions) != 1 || overview.Sessions[0].Name != "dev" {
		t.Fatalf("sessions = %+v, want only dev", overview.Sessions)
	}
}

func TestOverviewTableFailurePropagates(t *testing.T) {
	e := newEngine(t.TempDir())
	e.LoadTable = func() ([]mountinfo.Mount, error) {
		return nil, errors.New("table exploded")
	}
	if _, err := e.Overview(""); err == nil {
		t.Fatal("Overview succeeded, want the table failure")
	}
}
