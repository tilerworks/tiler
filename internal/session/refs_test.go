package session

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/image"
)

// aliceRef is the identity the seeded fixtures pin.
func aliceRef() image.Ref {
	return image.Ref{Name: "alice/app", FullID: "0123456789abcdef"}
}

// seedActionRecord writes one record carrying a single action with
// the given status, bypassing the engine for corrupt-shape fixtures.
func seedActionRecord(t *testing.T, e *Engine, name, status string) {
	t.Helper()
	action := mountAction("/ws/view")
	action.Seq = 1
	action.Status = status
	action.Timestamp = testNow
	rec := Record{Name: name, State: StateActive, Actions: []Action{action}, CreatedAt: testNow}
	if err := e.Save(rec); err != nil {
		t.Fatal(err)
	}
}

// servedFullID and servedTipID are the replaced stack ids the
// dual-stack fixtures carry beside the desired pair rebaseFull and
// rebaseTip from the engine fixtures.
const (
	servedFullID = "0123456789abcdef"
	servedTipID  = "9999888877776666"
)

// seedRebasedRecord writes one active record whose single mounted
// action carries both stacks of a rebased view: the desired pair the
// next mount serves and the served pair the live view still serves.
func seedRebasedRecord(t *testing.T, e *Engine, name, status string) {
	t.Helper()
	action := mountAction("/ws/view")
	action.Seq = 1
	action.Status = status
	action.Timestamp = testNow
	action.FullID = rebaseFull
	action.TipID = rebaseTip
	action.ServedID = servedFullID
	action.ServedTip = servedTipID
	rec := Record{Name: name, State: StateActive, Actions: []Action{action}, CreatedAt: testNow}
	if err := e.Save(rec); err != nil {
		t.Fatal(err)
	}
}

// stackProbes are the identities a rebased record must pin: both
// stacks, each asked with and without its tip.
func stackProbes() []image.Ref {
	return []image.Ref{
		{Name: "alice/app", FullID: rebaseFull},
		{Name: "alice/app", FullID: rebaseFull, TipID: rebaseTip},
		{Name: "alice/app", FullID: servedFullID},
		{Name: "alice/app", FullID: servedFullID, TipID: servedTipID},
	}
}

func TestRefsPinsServedAndDesiredStacks(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedRebasedRecord(t, e, "dev", StatusMounted)
	refs := Refs{Dir: dir}

	for _, probe := range stackProbes() {
		held, err := refs.Holds(probe)
		if err != nil || !held {
			t.Fatalf("probe %+v held = %v (%v)", probe, held, err)
		}
	}
	// An unknown full, an unknown tip on a matching full, and
	// another name pin nothing.
	for _, probe := range []image.Ref{
		{Name: "alice/app", FullID: "5555555555555555"},
		{Name: "alice/app", FullID: rebaseFull, TipID: "5555555555555555"},
		{Name: "alice/app", FullID: servedFullID, TipID: "5555555555555555"},
		{Name: "bob/app", FullID: rebaseFull, TipID: rebaseTip},
		{Name: "bob/app", FullID: servedFullID, TipID: servedTipID},
	} {
		held, err := refs.Holds(probe)
		if err != nil || held {
			t.Fatalf("probe %+v held = %v (%v)", probe, held, err)
		}
	}
}

func TestRefsReleasesBothStacksAfterUmount(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedRebasedRecord(t, e, "dev", StatusMounted)
	refs := Refs{Dir: dir}
	if err := e.Writeback("dev", 1, StatusUnmounted, ""); err != nil {
		t.Fatal(err)
	}
	// The live view is down: neither the served nor the desired
	// stack stays pinned through the released action.
	for _, probe := range stackProbes() {
		held, err := refs.Holds(probe)
		if err != nil || held {
			t.Fatalf("probe %+v held = %v (%v) after umount", probe, held, err)
		}
	}
}

func TestRefsHoldsActiveLibraryMounts(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedMounted(t, e, "dev", "/ws/view")
	refs := Refs{Dir: dir}

	held, err := refs.Holds(aliceRef())
	if err != nil || !held {
		t.Fatalf("active record held = %v (%v)", held, err)
	}
	for _, probe := range []image.Ref{
		{Name: "alice/app", FullID: "fedcba9876543210"},
		{Name: "bob/app", FullID: "0123456789abcdef"},
		{Name: "alice/other", FullID: "0123456789abcdef"},
	} {
		held, err := refs.Holds(probe)
		if err != nil || held {
			t.Fatalf("probe %+v held = %v (%v)", probe, held, err)
		}
	}
	// A file-path image records no identity and never protects.
	held, err = refs.Holds(image.Ref{})
	if err != nil || held {
		t.Fatalf("empty ref held = %v (%v)", held, err)
	}
	// A closed record pins nothing.
	if _, err := e.Close("dev", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	held, err = refs.Holds(aliceRef())
	if err != nil || held {
		t.Fatalf("closed record held = %v (%v)", held, err)
	}
}

func TestRefsHolderNamesPinningSession(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedMounted(t, e, "dev", "/ws/view")
	refs := Refs{Dir: dir}

	name, held, err := refs.Holder(aliceRef())
	if err != nil || !held || name != "dev" {
		t.Fatalf("Holder = (%q, %v, %v)", name, held, err)
	}
	// Holds is the same answer reduced to its bool.
	held, err = refs.Holds(aliceRef())
	if err != nil || !held {
		t.Fatalf("Holds = (%v, %v)", held, err)
	}
	// A probe nothing pins answers with an empty name.
	probe := image.Ref{Name: "alice/app", FullID: "fedcba9876543210"}
	name, held, err = refs.Holder(probe)
	if err != nil || held || name != "" {
		t.Fatalf("probe Holder = (%q, %v, %v)", name, held, err)
	}
	// After the session closes, nothing is named or held.
	if _, err := e.Close("dev", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	name, held, err = refs.Holder(aliceRef())
	if err != nil || held || name != "" {
		t.Fatalf("closed Holder = (%q, %v, %v)", name, held, err)
	}
}

func TestRefsCountsCrashShapesConservatively(t *testing.T) {
	for _, status := range []string{StatusPending, StatusFailed, StatusMounted} {
		t.Run(status, func(t *testing.T) {
			dir := t.TempDir()
			e := newEngine(dir)
			seedActionRecord(t, e, "dev", status)
			held, err := Refs{Dir: dir}.Holds(aliceRef())
			if err != nil || !held {
				t.Fatalf("status %s held = %v (%v)", status, held, err)
			}
		})
	}
	t.Run(StatusUnmounted, func(t *testing.T) {
		dir := t.TempDir()
		e := newEngine(dir)
		seedActionRecord(t, e, "dev", StatusUnmounted)
		held, err := Refs{Dir: dir}.Holds(aliceRef())
		if err != nil || held {
			t.Fatalf("unmounted held = %v (%v)", held, err)
		}
	})
}

func TestRefsFilePathImageNeverProtects(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	action := mountAction("/ws/view")
	action.Seq = 1
	action.Status = StatusMounted
	action.Name, action.FullID = "", ""
	action.Image = "/somewhere/bare.sqsh"
	rec := Record{Name: "dev", State: StateActive, Actions: []Action{action}, CreatedAt: testNow}
	if err := e.Save(rec); err != nil {
		t.Fatal(err)
	}
	held, err := Refs{Dir: dir}.Holds(aliceRef())
	if err != nil || held {
		t.Fatalf("file-path record held = %v (%v)", held, err)
	}
}

func TestRefsHeldRecordIsConservative(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("advisory locking needs linux")
	}
	dir := t.TempDir()
	e := newEngine(dir)
	seedMounted(t, e, "dev", "/ws/view")
	release, err := e.lockSession("dev", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// A record another process holds may pin the image; the safe
	// side refuses the delete.
	held, err := Refs{Dir: dir}.Holds(aliceRef())
	if err != nil || !held {
		t.Fatalf("held record held = %v (%v)", held, err)
	}
}

func TestRefsToleratesRemovedRecords(t *testing.T) {
	// A record removed after the listing answers "does not hold"
	// instead of failing the whole scan.
	dir := t.TempDir()
	held, err := Refs{Dir: dir}.recordHolds(Store{Dir: dir}, "absent", aliceRef())
	if err != nil || held {
		t.Fatalf("absent record held = %v (%v)", held, err)
	}
}

func TestRefsMissingDirHoldsNothing(t *testing.T) {
	refs := Refs{Dir: filepath.Join(t.TempDir(), "absent")}
	held, err := refs.Holds(aliceRef())
	if err != nil || held {
		t.Fatalf("held = %v err = %v", held, err)
	}
}

func TestRefsReportsCorruptRecords(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "broken"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken", "session.json"), []byte(":::"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Refs{Dir: dir}.Holds(aliceRef())
	if err == nil {
		t.Fatal("a corrupt record was silently dropped")
	}
}
