package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testNow is the fixed clock the lifecycle tests record with.
var testNow = time.Date(2026, 10, 17, 3, 15, 0, 0, time.UTC)

// newEngine returns an engine with a no-op lock seam, so lifecycle
// unit tests run on any platform, and a fixed clock. The real flock
// has its own Linux tests.
func newEngine(dir string) *Engine {
	return &Engine{
		Store: Store{Dir: dir},
		Now:   func() time.Time { return testNow },
		Lock: func(string, time.Duration) (func(), error) {
			return func() {}, nil
		},
	}
}

// callLog records seam invocations in call order.
type callLog struct {
	calls []string
}

func (l *callLog) add(format string, args ...any) {
	l.calls = append(l.calls, fmt.Sprintf(format, args...))
}

// mountAction is one recorded mount for the lifecycle fixtures.
func mountAction(point string) Action {
	return Action{
		Type:       TypeMount,
		Image:      "alice/app",
		Owner:      "alice",
		Repo:       "app",
		FullID:     "0123456789abcdef",
		Mountpoint: point,
		Mode:       ModeRW,
	}
}

// seedMounted creates one active record whose actions are all
// mounted, through the real write-ahead path.
func seedMounted(t *testing.T, e *Engine, name string, points ...string) {
	t.Helper()
	if err := e.New(name); err != nil {
		t.Fatal(err)
	}
	mount := func(a Action) (Result, error) {
		return Result{Mountpoint: a.Mountpoint}, nil
	}
	for _, point := range points {
		if _, err := e.RunMount(name, mountAction(point), mount); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNewCreatesActiveRecordAndRejectsDuplicates(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != StateActive || len(rec.Actions) != 0 {
		t.Fatalf("record = %+v", rec)
	}
	if !rec.CreatedAt.Equal(testNow) {
		t.Errorf("CreatedAt = %v, want the injected clock", rec.CreatedAt)
	}
	err = e.New("dev")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate New error = %v", err)
	}
	// Implicit auto-* sessions join the duplicate check.
	auto, err := ImplicitName(testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.New(auto); err != nil {
		t.Fatal(err)
	}
	if err := e.New(auto); err == nil {
		t.Fatal("duplicate implicit New accepted")
	}
}

func TestRunMountIsWriteAheadAndWritesBack(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	point := filepath.Join(dir, "view")
	var log callLog
	mount := func(a Action) (Result, error) {
		// Write-ahead: the pending action is on disk before the
		// engine runs.
		rec, err := Store{Dir: dir}.Load("dev")
		if err != nil {
			return Result{}, err
		}
		if len(rec.Actions) != 1 || rec.Actions[0].Status != StatusPending {
			return Result{}, fmt.Errorf("disk during mount = %+v", rec.Actions)
		}
		log.add("mount %d %s", a.Seq, a.Mountpoint)
		return Result{Mountpoint: a.Mountpoint, Upper: filepath.Join(dir, "upper")}, nil
	}
	// The type is forced to mount, whatever the caller passed.
	action := mountAction(point)
	action.Type = ""
	result, err := e.RunMount("dev", action, mount)
	if err != nil {
		t.Fatal(err)
	}
	if result.Mountpoint != point {
		t.Errorf("result = %+v", result)
	}
	if strings.Join(log.calls, ",") != "mount 1 "+point {
		t.Errorf("calls = %v", log.calls)
	}
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	stored := rec.Actions[0]
	if stored.Type != TypeMount || stored.Status != StatusMounted {
		t.Fatalf("stored = %+v", stored)
	}
	if stored.Seq != 1 || !stored.Timestamp.Equal(testNow) {
		t.Errorf("stored = %+v", stored)
	}
}

func TestRunMountFailureKeepsWriteAheadRecord(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("engine exploded")
	_, err := e.RunMount("dev", mountAction(filepath.Join(dir, "view")),
		func(Action) (Result, error) { return Result{}, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("RunMount error = %v, want the execution error", err)
	}
	rec, loadErr := e.Load("dev")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(rec.Actions) != 1 || rec.Actions[0].Status != StatusFailed {
		t.Fatalf("actions = %+v", rec.Actions)
	}
}

func TestRunMountMissingRecordFails(t *testing.T) {
	e := newEngine(t.TempDir())
	_, err := e.RunMount("absent", mountAction("/view"),
		func(Action) (Result, error) { return Result{}, nil })
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("RunMount error = %v, want ErrNotFound", err)
	}
}

func TestRunMountRejectsClosedSession(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedMounted(t, e, "dev", filepath.Join(dir, "view"))
	if err := e.Close("dev", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	_, err := e.RunMount("dev", mountAction(filepath.Join(dir, "other")),
		func(Action) (Result, error) { return Result{}, nil })
	if err == nil || !strings.Contains(err.Error(), "create a new session") {
		t.Fatalf("RunMount error = %v", err)
	}
	rec, loadErr := e.Load("dev")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(rec.Actions) != 1 {
		t.Fatalf("a refused mount was recorded: %+v", rec.Actions)
	}
}

func TestOpenSkipsHealthyMounts(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	view1 := filepath.Join(dir, "view1")
	view2 := filepath.Join(dir, "view2")
	seedMounted(t, e, "dev", view1, view2)
	var log callLog
	e.Health = func(point string) bool {
		log.add("health %s", point)
		return true
	}
	e.Clean = func(point string) error {
		log.add("clean %s", point)
		return nil
	}
	mount := func(a Action) (Result, error) {
		log.add("mount %d", a.Seq)
		return Result{Mountpoint: a.Mountpoint}, nil
	}
	// Repeated opens skip every healthy mount: no side effects.
	for i := 0; i < 2; i++ {
		if err := e.Open("dev", mount); err != nil {
			t.Fatal(err)
		}
	}
	want := fmt.Sprintf("health %s,health %s,health %s,health %s",
		view1, view2, view1, view2)
	if strings.Join(log.calls, ",") != want {
		t.Fatalf("calls = %v", log.calls)
	}
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range rec.Actions {
		if action.Status != StatusMounted {
			t.Errorf("action %+v changed", action)
		}
	}
}

func TestOpenReplaysStaleMountsInRecordOrder(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	view1 := filepath.Join(dir, "view1")
	view2 := filepath.Join(dir, "view2")
	seedMounted(t, e, "dev", view1, view2)
	// A crashed earlier replay left the second action failed; both
	// points are stale now.
	var log callLog
	e.Health = func(string) bool { return false }
	e.Clean = func(point string) error {
		log.add("clean %s", filepath.Base(point))
		return nil
	}
	mount := func(a Action) (Result, error) {
		log.add("mount %d %s", a.Seq, filepath.Base(a.Mountpoint))
		return Result{Mountpoint: a.Mountpoint, Reused: false}, nil
	}
	if err := e.Open("dev", mount); err != nil {
		t.Fatal(err)
	}
	want := "clean view1,mount 1 view1,clean view2,mount 2 view2"
	if strings.Join(log.calls, ",") != want {
		t.Fatalf("calls = %q, want %q", strings.Join(log.calls, ","), want)
	}
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range rec.Actions {
		if action.Status != StatusMounted {
			t.Errorf("action %+v not recovered", action)
		}
	}
}

func TestOpenRemountsTornDownViewsAndIgnoresUmountActions(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	view1 := filepath.Join(dir, "view1")
	view2 := filepath.Join(dir, "view2")
	seedMounted(t, e, "dev", view1, view2)
	// A view torn down behind the record's back and a recorded umount
	// action both face the same rule: reality decides, and a dead view
	// remounts even when the record still calls it unmounted.
	if err := e.Writeback("dev", 1, StatusUnmounted, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Append("dev", Action{Type: TypeUmount, Mountpoint: view2}); err != nil {
		t.Fatal(err)
	}
	var log callLog
	e.Health = func(string) bool { return false }
	e.Clean = func(point string) error {
		log.add("clean %s", filepath.Base(point))
		return nil
	}
	mount := func(a Action) (Result, error) {
		log.add("mount %d", a.Seq)
		return Result{Mountpoint: a.Mountpoint}, nil
	}
	if err := e.Open("dev", mount); err != nil {
		t.Fatal(err)
	}
	want := "clean view1,mount 1,clean view2,mount 2"
	if strings.Join(log.calls, ",") != want {
		t.Fatalf("calls = %q, want %q", strings.Join(log.calls, ","), want)
	}
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range rec.Actions {
		if action.Type == TypeMount && action.Status != StatusMounted {
			t.Errorf("action %+v not recovered", action)
		}
	}
}

func TestOpenReconcilesHealthyPendingActionWithoutRemount(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	view := filepath.Join(dir, "view")
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	// A crash between the execution and its writeback leaves the
	// action pending while the view itself is healthy.
	if _, err := e.Append("dev", mountAction(view)); err != nil {
		t.Fatal(err)
	}
	var log callLog
	e.Health = func(string) bool { return true }
	e.Clean = func(point string) error {
		log.add("clean %s", filepath.Base(point))
		return nil
	}
	mount := func(a Action) (Result, error) {
		log.add("mount %d", a.Seq)
		return Result{Mountpoint: a.Mountpoint}, nil
	}
	if err := e.Open("dev", mount); err != nil {
		t.Fatal(err)
	}
	if len(log.calls) != 0 {
		t.Fatalf("a healthy view was cleaned or remounted: %v", log.calls)
	}
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Actions[0].Status != StatusMounted {
		t.Fatalf("action = %+v, want the reconciled mounted status", rec.Actions[0])
	}
	// A repeated open stays a pure no-op.
	if err := e.Open("dev", mount); err != nil {
		t.Fatal(err)
	}
	if len(log.calls) != 0 {
		t.Fatalf("a repeated open acted: %v", log.calls)
	}
}

func TestOpenReopensClosedSession(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	view := filepath.Join(dir, "view")
	seedMounted(t, e, "dev", view)
	if err := e.Close("dev", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var log callLog
	e.Health = func(string) bool { return false }
	e.Clean = func(string) error {
		log.add("clean")
		return nil
	}
	mount := func(a Action) (Result, error) {
		log.add("mount %d", a.Seq)
		return Result{Mountpoint: a.Mountpoint}, nil
	}
	if err := e.Open("dev", mount); err != nil {
		t.Fatal(err)
	}
	if strings.Join(log.calls, ",") != "clean,mount 1" {
		t.Fatalf("calls = %v", log.calls)
	}
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != StateActive || !rec.ClosedAt.IsZero() {
		t.Fatalf("record = %+v, want an active record with no close time", rec)
	}
	if rec.Actions[0].Status != StatusMounted {
		t.Errorf("action = %+v, want mounted", rec.Actions[0])
	}
	// A repeated open against a healthy view stays a pure no-op.
	e.Health = func(string) bool { return true }
	log.calls = nil
	if err := e.Open("dev", mount); err != nil {
		t.Fatal(err)
	}
	if len(log.calls) != 0 {
		t.Fatalf("a repeated open acted: %v", log.calls)
	}
}

func TestOpenMountFailureMarksFailedAndStops(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	view1 := filepath.Join(dir, "view1")
	view2 := filepath.Join(dir, "view2")
	seedMounted(t, e, "dev", view1, view2)
	e.Health = func(string) bool { return false }
	e.Clean = func(string) error { return nil }
	var mounted []int
	mount := func(a Action) (Result, error) {
		mounted = append(mounted, a.Seq)
		if a.Seq == 1 {
			return Result{}, errors.New("daemon died")
		}
		return Result{Mountpoint: a.Mountpoint}, nil
	}
	err := e.Open("dev", mount)
	if err == nil || !strings.Contains(err.Error(), "replay seq 1") {
		t.Fatalf("Open error = %v", err)
	}
	if len(mounted) != 1 {
		t.Fatalf("replay continued past the failure: %v", mounted)
	}
	rec, loadErr := e.Load("dev")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if rec.Actions[0].Status != StatusFailed {
		t.Errorf("failed action = %+v", rec.Actions[0])
	}
	if rec.Actions[1].Status != StatusMounted {
		t.Errorf("later action = %+v", rec.Actions[1])
	}
}

func TestOpenCleanFailureAborts(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedMounted(t, e, "dev", filepath.Join(dir, "view"))
	e.Health = func(string) bool { return false }
	e.Clean = func(string) error { return errors.New("fusermount3: busy") }
	err := e.Open("dev", func(Action) (Result, error) { return Result{}, nil })
	if err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("Open error = %v", err)
	}
}

func TestCloseUnmountsInReverseOrderThenCloses(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	view1 := filepath.Join(dir, "view1")
	view2 := filepath.Join(dir, "view2")
	seedMounted(t, e, "dev", view1, view2)
	var log callLog
	umount := func(point string) error {
		log.add("umount %s", filepath.Base(point))
		return nil
	}
	if err := e.Close("dev", umount); err != nil {
		t.Fatal(err)
	}
	if strings.Join(log.calls, ",") != "umount view2,umount view1" {
		t.Fatalf("calls = %v", log.calls)
	}
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != StateClosed || !rec.ClosedAt.Equal(testNow) {
		t.Fatalf("record = %+v", rec)
	}
	for _, action := range rec.Actions {
		if action.Status != StatusUnmounted {
			t.Errorf("action %+v not unmounted", action)
		}
	}
	// Closing again is a no-op, and the closed record can be removed.
	if err := e.Close("dev", func(string) error {
		t.Fatal("a closed session unmounted again")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCloseTearsDownPendingCrashAction(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	view := filepath.Join(dir, "view")
	seedMounted(t, e, "dev", view)
	// A crash between the write-ahead append and the writeback
	// leaves a pending action whose mount may still be up.
	if _, err := e.Append("dev", mountAction(filepath.Join(dir, "crashed"))); err != nil {
		t.Fatal(err)
	}
	var log callLog
	if err := e.Close("dev", func(point string) error {
		log.add("umount %s", filepath.Base(point))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(log.calls, ",") != "umount crashed,umount view" {
		t.Fatalf("calls = %v", log.calls)
	}
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != StateClosed {
		t.Fatalf("record = %+v", rec)
	}
}

func TestCloseUmountFailureKeepsRecordActive(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedMounted(t, e, "dev", filepath.Join(dir, "view"))
	err := e.Close("dev", func(string) error { return errors.New("device busy") })
	if err == nil || !strings.Contains(err.Error(), "device busy") {
		t.Fatalf("Close error = %v", err)
	}
	rec, loadErr := e.Load("dev")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if rec.State != StateActive {
		t.Fatalf("a failed close closed the record: %+v", rec)
	}
}

func TestRemoveRejectsActiveAndDeletesClosed(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	view := filepath.Join(dir, "view")
	seedMounted(t, e, "dev", view)
	err := e.Remove("dev")
	if err == nil || !strings.Contains(err.Error(), "close it before removing") {
		t.Fatalf("Remove error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "dev.json")); statErr != nil {
		t.Fatalf("refused remove deleted the record: %v", statErr)
	}
	if err := e.Close("dev", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove("dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Load("dev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load error = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dev.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file survived: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("remove left residue: %v", entries)
	}
}

func TestRemoveMissingRecordFails(t *testing.T) {
	e := newEngine(t.TempDir())
	if err := e.Remove("absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Remove error = %v, want ErrNotFound", err)
	}
}

func TestEngineOperationsUseRealLocksOnLinux(t *testing.T) {
	// The default lock seam is the real flock: a plain engine still
	// serializes its own operations without a test seam.
	dir := t.TempDir()
	e := &Engine{Store: Store{Dir: dir}, Now: func() time.Time { return testNow }}
	if err := e.New("dev"); err != nil {
		if errors.Is(err, ErrLockUnavailable) {
			t.Skip("advisory locking needs linux")
		}
		t.Fatal(err)
	}
	seed := func(Action) (Result, error) { return Result{Mountpoint: "/view"}, nil }
	if _, err := e.RunMount("dev", mountAction("/view"), seed); err != nil {
		t.Fatal(err)
	}
	if err := e.Close("dev", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove("dev"); err != nil {
		t.Fatal(err)
	}
}
