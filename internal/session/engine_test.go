package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
		Name:       "alice/app",
		FullID:     "0123456789abcdef",
		Mountpoint: point,
		Mode:       ModeRW,
	}
}

// seedMounted creates one active record whose actions are all
// mounted, through the real write-ahead path.
func seedMounted(t *testing.T, e *Engine, name string, points ...string) {
	t.Helper()
	seedMountedMarked(t, e, name, false, points...)
}

// seedRemovableMounted creates one active --rm record whose actions
// are all mounted.
func seedRemovableMounted(t *testing.T, e *Engine, name string, points ...string) {
	t.Helper()
	seedMountedMarked(t, e, name, true, points...)
}

func seedMountedMarked(t *testing.T, e *Engine, name string, removable bool, points ...string) {
	t.Helper()
	var err error
	if removable {
		err = e.NewRemovable(name)
	} else {
		err = e.New(name)
	}
	if err != nil {
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
	if err == nil || !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate New error = %v", err)
	}
	// Generated adjective-animal names join the duplicate check.
	generated, err := GenerateName()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.New(generated); err != nil {
		t.Fatal(err)
	}
	if err := e.New(generated); err == nil {
		t.Fatal("duplicate generated New accepted")
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
	if _, err := e.Close("dev", func(string) error { return nil }); err != nil {
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

// The stack ids of the served-stack fixtures: a desired tip and the
// served pair a rebuild left behind while the live view still serves
// the replaced stack.
const (
	fixtureTip       = "fedcba9876543210"
	fixtureServedID  = "abcdef0123456789"
	fixtureServedTip = "9999888877776666"
)

// seedServedMounted records one mounted action carrying a desired
// tip and served stack ids, through the real write-ahead path.
func seedServedMounted(t *testing.T, e *Engine, name, point string) {
	t.Helper()
	action := mountAction(point)
	action.TipID = fixtureTip
	action.ServedID = fixtureServedID
	action.ServedTip = fixtureServedTip
	stored, err := e.Append(name, action)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Writeback(name, stored.Seq, StatusMounted, ""); err != nil {
		t.Fatal(err)
	}
}

func TestOpenReplayClearsServedAfterRealRemount(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	view1 := filepath.Join(dir, "view1")
	view2 := filepath.Join(dir, "view2")
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	seedServedMounted(t, e, "dev", view1)
	seedServedMounted(t, e, "dev", view2)
	// view1 is stale and remounts for real; view2 stays healthy and
	// is skipped, so its live view still serves the replaced stack.
	e.Health = func(point string) bool { return point == view2 }
	e.Clean = func(string) error { return nil }
	mount := func(a Action) (Result, error) {
		return Result{Mountpoint: a.Mountpoint}, nil
	}
	if err := e.Open("dev", mount); err != nil {
		t.Fatal(err)
	}
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	remounted := rec.Actions[0]
	if remounted.Status != StatusMounted {
		t.Fatalf("remounted action = %+v, want mounted", remounted)
	}
	if remounted.ServedID != "" || remounted.ServedTip != "" {
		t.Errorf("a real remount kept the served stack: %+v", remounted)
	}
	if remounted.FullID != "0123456789abcdef" || remounted.TipID != fixtureTip {
		t.Errorf("a real remount changed the desired stack: %+v", remounted)
	}
	skipped := rec.Actions[1]
	if skipped.ServedID != fixtureServedID || skipped.ServedTip != fixtureServedTip {
		t.Errorf("a healthy skipped view lost its served stack: %+v", skipped)
	}
}

func TestOpenReplayKeepsServedOnReusedView(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	view := filepath.Join(dir, "view")
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	seedServedMounted(t, e, "dev", view)
	e.Health = func(string) bool { return false }
	e.Clean = func(string) error { return nil }
	// The engine adopted an already-healthy view: nothing remounted
	// for real, so the record must keep describing the stack the
	// live view actually serves.
	mount := func(a Action) (Result, error) {
		return Result{Mountpoint: a.Mountpoint, Reused: true}, nil
	}
	if err := e.Open("dev", mount); err != nil {
		t.Fatal(err)
	}
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	action := rec.Actions[0]
	if action.Status != StatusMounted {
		t.Fatalf("action = %+v, want mounted", action)
	}
	if action.ServedID != fixtureServedID || action.ServedTip != fixtureServedTip {
		t.Errorf("an adopted view lost its served stack: %+v", action)
	}
}

// TestOpenReconcileOfProvenMountClearsServed pins the heal of the
// mount-succeeded-but-not-yet-written window: a crash after a real
// remount left the action pending or failed while its served ids
// still name the replaced stack. Reality proves the execution ran,
// so the reconcile to mounted empties the served pair in the same
// writeback instead of stranding it beside status=mounted forever.
func TestOpenReconcileOfProvenMountClearsServed(t *testing.T) {
	for _, status := range []string{StatusPending, StatusFailed} {
		t.Run(status, func(t *testing.T) {
			dir := t.TempDir()
			e := newEngine(dir)
			view := filepath.Join(dir, "view")
			if err := e.New("dev"); err != nil {
				t.Fatal(err)
			}
			action := mountAction(view)
			action.TipID = fixtureTip
			action.ServedID = fixtureServedID
			action.ServedTip = fixtureServedTip
			if _, err := e.Append("dev", action); err != nil {
				t.Fatal(err)
			}
			if status == StatusFailed {
				if err := e.Writeback("dev", 1, StatusFailed, ""); err != nil {
					t.Fatal(err)
				}
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
				t.Fatalf("a proven mount was cleaned or remounted: %v", log.calls)
			}
			rec, err := e.Load("dev")
			if err != nil {
				t.Fatal(err)
			}
			got := rec.Actions[0]
			if got.Status != StatusMounted {
				t.Fatalf("action = %+v, want the reconciled mounted status", got)
			}
			if got.ServedID != "" || got.ServedTip != "" {
				t.Errorf("the reconcile kept the replaced served stack: %+v", got)
			}
			if got.TipID != fixtureTip {
				t.Errorf("the reconcile changed the desired stack: %+v", got)
			}
		})
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
	if _, err := e.Close("dev", func(string) error { return nil }); err != nil {
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
	if _, err := e.Close("dev", umount); err != nil {
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
	if _, err := e.Close("dev", func(string) error {
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
	if _, err := e.Close("dev", func(point string) error {
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
	_, err := e.Close("dev", func(string) error { return errors.New("device busy") })
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

func TestNewRemovableMarksRecord(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	if err := e.NewRemovable("dev"); err != nil {
		t.Fatal(err)
	}
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.RemoveOnClose || rec.State != StateActive {
		t.Fatalf("record = %+v", rec)
	}
	// The duplicate refusal still holds for a --rm name.
	if err := e.NewRemovable("dev"); err == nil {
		t.Fatal("duplicate NewRemovable accepted")
	}
	// A plain New leaves the marker clear.
	if err := e.New("plain"); err != nil {
		t.Fatal(err)
	}
	plain, err := e.Load("plain")
	if err != nil {
		t.Fatal(err)
	}
	if plain.RemoveOnClose {
		t.Fatalf("a plain New was marked --rm: %+v", plain)
	}
}

func TestCloseRemovablePurgesRuntimeAndDeletesRecord(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	view1 := filepath.Join(dir, "view1")
	view2 := filepath.Join(dir, "view2")
	if err := e.NewRemovable("dev"); err != nil {
		t.Fatal(err)
	}
	mount := func(a Action) (Result, error) {
		return Result{Mountpoint: a.Mountpoint}, nil
	}
	for _, point := range []string{view1, view2} {
		if _, err := e.RunMount("dev", mountAction(point), mount); err != nil {
			t.Fatal(err)
		}
	}
	var purged, umounted []string
	e.RemoveRuntime = func(point string) error {
		purged = append(purged, filepath.Base(point))
		return nil
	}
	removed, err := e.Close("dev", func(point string) error {
		umounted = append(umounted, filepath.Base(point))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("a --rm close did not report removed")
	}
	// Every recorded view's runtime is purged exactly once, in
	// record order, while the teardown itself runs in reverse.
	if strings.Join(purged, ",") != "view1,view2" {
		t.Fatalf("purged = %v", purged)
	}
	if strings.Join(umounted, ",") != "view2,view1" {
		t.Fatalf("umounted = %v", umounted)
	}
	// The record and its lock file are gone, not kept closed.
	if _, err := e.Load("dev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load after --rm close = %v, want ErrNotFound", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "dev")); !os.IsNotExist(statErr) {
		t.Fatalf("dev session dir survived: %v", statErr)
	}
}

func TestClosePlainRecordKeepsAuditAndSkipsPurge(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedMounted(t, e, "dev", filepath.Join(dir, "view"))
	purged := false
	e.RemoveRuntime = func(string) error {
		purged = true
		return nil
	}
	removed, err := e.Close("dev", func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("a plain close reported removed")
	}
	if purged {
		t.Fatal("a plain close purged the runtime")
	}
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != StateClosed || !rec.ClosedAt.Equal(testNow) {
		t.Fatalf("record = %+v", rec)
	}
}

func TestCloseRemovableDeletesRecordDespitePurgeError(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	view := filepath.Join(dir, "view")
	if err := e.NewRemovable("dev"); err != nil {
		t.Fatal(err)
	}
	mount := func(a Action) (Result, error) {
		return Result{Mountpoint: a.Mountpoint}, nil
	}
	if _, err := e.RunMount("dev", mountAction(view), mount); err != nil {
		t.Fatal(err)
	}
	e.RemoveRuntime = func(string) error { return errors.New("EBUSY") }
	removed, err := e.Close("dev", func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "EBUSY") {
		t.Fatalf("Close error = %v, want the purge failure", err)
	}
	if !removed {
		t.Fatal("a failed purge blocked the removal report")
	}
	// The record is deleted anyway: dropping it unpins the image,
	// the primary goal of a --rm close, and the purge error rides
	// along without blocking it.
	if _, loadErr := e.Load("dev"); !errors.Is(loadErr, ErrNotFound) {
		t.Fatalf("Load after failed purge = %v, want ErrNotFound", loadErr)
	}
}

func TestDiscardDeletesRecordWithoutLiveAction(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	e.Health = func(string) bool { return false }
	const name = "happy-fox"
	if err := e.New(name); err != nil {
		t.Fatal(err)
	}
	// One failed action: the mount never came up.
	if _, err := e.Append(name, mountAction("/ws/one")); err != nil {
		t.Fatal(err)
	}
	if err := e.Writeback(name, 1, StatusFailed, ""); err != nil {
		t.Fatal(err)
	}
	deleted, err := e.Discard(name, false)
	if err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("Discard kept a record with only a failed action")
	}
	if _, err := e.Load(name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load error = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Errorf("session dir stat error = %v, want not-exist", err)
	}
}

func TestDiscardKeepsServingRecords(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	const name = "happy-fox"

	// A mounted action that was not an adoption describes a view the
	// session serves, so the record stays even when the view died:
	// it is the recovery source for a later open.
	seedMounted(t, e, name, "/ws/one")
	deleted, err := e.Discard(name, false)
	if err != nil || deleted {
		t.Fatalf("Discard = %v, %v, want false, nil", deleted, err)
	}
	assertRecordState(t, e, name, StateActive)

	// The same record becomes discardable when the mount merely
	// adopted a view another session already serves.
	deleted, err = e.Discard(name, true)
	if err != nil || !deleted {
		t.Fatalf("Discard = %v, %v, want true, nil", deleted, err)
	}
	if _, err := e.Load(name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load error = %v, want ErrNotFound", err)
	}

	// A named empty/reused record is discardable regardless of name
	// shape: Discard no longer gates on an auto-* prefix.
	seedMounted(t, e, "dev", "/ws/two")
	deleted, err = e.Discard("dev", true)
	if err != nil || !deleted {
		t.Fatalf("Discard named reused = %v, %v, want true, nil", deleted, err)
	}
	if _, err := e.Load("dev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load error = %v, want ErrNotFound", err)
	}
}

func TestDiscardMissingRecordIsNoop(t *testing.T) {
	e := newEngine(t.TempDir())
	e.Health = func(string) bool { return false }
	deleted, err := e.Discard("happy-fox", false)
	if err != nil || deleted {
		t.Fatalf("Discard = %v, %v, want false, nil", deleted, err)
	}
}

func TestReleaseMountMarksActionUnmountedWithoutClosing(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	e.Health = func(string) bool { return false }
	const name = "happy-fox"
	seedMounted(t, e, name, "/ws/one")
	if err := e.ReleaseMount("/ws/one"); err != nil {
		t.Fatal(err)
	}
	rec, err := e.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != StateActive {
		t.Fatalf("state = %s, want active", rec.State)
	}
	if rec.Actions[0].Status != StatusUnmounted {
		t.Fatalf("action = %+v, want unmounted", rec.Actions[0])
	}
}

func TestCloseRemovableForDeletesIdleRemovable(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	e.Health = func(string) bool { return false }
	const name = "happy-fox"
	seedRemovableMounted(t, e, name, "/ws/one")
	var torn []string
	closed, err := e.CloseRemovableFor("/ws/one", func(point string) error {
		torn = append(torn, point)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if closed != name {
		t.Fatalf("closed %q, want %q", closed, name)
	}
	if len(torn) != 1 || torn[0] != "/ws/one" {
		t.Errorf("umount calls = %v", torn)
	}
	if _, err := e.Load(name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load after --rm close = %v, want ErrNotFound", err)
	}
}

func TestCloseRemovableForKeepsNonRemovableAndLiveViews(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	const removable = "happy-fox"
	e.Health = func(point string) bool { return point == "/ws/two" }
	seedRemovableMounted(t, e, removable, "/ws/one", "/ws/two")
	seedMounted(t, e, "dev", "/ws/three")
	seedMounted(t, e, "plain", "/ws/four")
	never := func(string) error { return errors.New("teardown attempted") }

	// The --rm record still serves /ws/two, so the umount of
	// /ws/one is not its last view.
	name, err := e.CloseRemovableFor("/ws/one", never)
	if err != nil || name != "" {
		t.Fatalf("CloseRemovableFor = %q, %v", name, err)
	}
	assertRecordState(t, e, removable, StateActive)

	// A dead non--rm session never auto-closes, and a point no
	// record covers is a no-op.
	e.Health = func(string) bool { return false }
	for _, miss := range []string{"/ws/three", "/ws/four", "/ws/absent"} {
		name, err = e.CloseRemovableFor(miss, never)
		if err != nil || name != "" {
			t.Fatalf("CloseRemovableFor(%s) = %q, %v", miss, name, err)
		}
	}
	assertRecordState(t, e, "dev", StateActive)
	assertRecordState(t, e, "plain", StateActive)
	assertRecordState(t, e, removable, StateActive)
}

func TestCloseRemovableForRechecksHealthUnderLock(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	const name = "happy-fox"
	seedRemovableMounted(t, e, name, "/ws/one")
	// The scan sees a dead view; a concurrent mount revives it
	// before the mutating lock re-checks the rule.
	calls := 0
	e.Health = func(string) bool { calls++; return calls > 1 }
	closed, err := e.CloseRemovableFor("/ws/one", func(string) error {
		return errors.New("teardown attempted")
	})
	if err != nil || closed != "" {
		t.Fatalf("CloseRemovableFor = %q, %v", closed, err)
	}
	if calls < 2 {
		t.Errorf("health calls = %d, want the under-lock re-check", calls)
	}
	assertRecordState(t, e, name, StateActive)
}

func TestCloseRemovableForSkipsBusyRecords(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("advisory locking needs linux")
	}
	dir := t.TempDir()
	e := newEngine(dir)
	e.Health = func(string) bool { return false }
	const name = "happy-fox"
	seedRemovableMounted(t, e, name, "/ws/one")
	release, err := e.lockSession(name, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	closed, err := e.CloseRemovableFor("/ws/one", func(string) error {
		return errors.New("teardown attempted")
	})
	if err != nil || closed != "" {
		t.Fatalf("CloseRemovableFor = %q, %v", closed, err)
	}
	assertRecordState(t, e, name, StateActive)
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
	if _, statErr := os.Stat(filepath.Join(dir, "dev", "session.json")); statErr != nil {
		t.Fatalf("refused remove deleted the record: %v", statErr)
	}
	if _, err := e.Close("dev", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove("dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Load("dev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load error = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dev")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session dir survived: %v", err)
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
	if _, err := e.Close("dev", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove("dev"); err != nil {
		t.Fatal(err)
	}
}
