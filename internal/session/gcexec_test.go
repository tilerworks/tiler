package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
	"github.com/jiangxin/tiler/internal/mountinfo"
)

// mustPlan plans at cutoff and fails the test on error.
func mustPlan(t *testing.T, e *Engine, cutoff time.Time) GCPlan {
	t.Helper()
	plan, err := e.PlanSessions(cutoff)
	if err != nil {
		t.Fatalf("PlanSessions: %v", err)
	}
	return plan
}

// assertExecKept executes plan and expects its only candidate to survive
// with the given reason and its directory untouched.
func assertExecKept(t *testing.T, e *Engine, plan GCPlan, reason string) {
	t.Helper()
	name := plan.Candidates[0].Name
	report, err := e.ExecSessions(plan)
	if err != nil {
		t.Fatalf("ExecSessions: %v", err)
	}
	if len(report.Removed) != 0 {
		t.Fatalf("removed = %v, want none", report.Removed)
	}
	if len(report.Kept) != 1 || report.Kept[0].Name != name ||
		report.Kept[0].Reason != reason || report.Kept[0].Detail == "" {
		t.Fatalf("kept = %+v, want one %s session with a reason", report.Kept, reason)
	}
	if _, err := os.Stat(filepath.Join(e.Dir, name)); err != nil {
		t.Fatalf("the kept session was touched: %v", err)
	}
}

// TestExecSessionsRemovesOnlyTheApprovedPlan deletes the candidates a
// preview offered, including the runtime their views used, and leaves
// every session the plan did not list alone.
func TestExecSessionsRemovesOnlyTheApprovedPlan(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	e := planEngine(t, nil, old)
	seedMounted(t, e, "alpha", "/ws/alpha")
	seedRemovableMounted(t, e, "beta", "/ws/beta")
	retired := attachRuntime(t, e, "alpha", "m1")
	e.Now = func() time.Time { return testNow }
	seedMounted(t, e, "young", "/ws/young")

	plan := mustPlan(t, e, testNow)
	if want := []string{"alpha", "beta"}; !sameStrings(planNames(plan), want) {
		t.Fatalf("candidates = %v, want %v", planNames(plan), want)
	}
	report, err := e.ExecSessions(plan)
	if err != nil {
		t.Fatalf("ExecSessions: %v", err)
	}
	if want := []string{"alpha", "beta"}; !sameStrings(report.Removed, want) {
		t.Fatalf("removed = %v, want %v", report.Removed, want)
	}
	if len(report.Kept) != 0 {
		t.Fatalf("kept = %+v, want none", report.Kept)
	}
	for _, name := range report.Removed {
		if _, err := os.Stat(filepath.Join(e.Dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s survived its removal: %v", name, err)
		}
	}
	if _, err := os.Stat(retired); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the runtime of alpha survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.Dir, "young")); err != nil {
		t.Fatalf("the young session was touched: %v", err)
	}
}

// TestExecSessionsDeletesAnActiveRecordAsItStands covers the record the
// legacy removal refuses: expiry alone decides, so an unattached active
// session is deleted instead of being closed first.
func TestExecSessionsDeletesAnActiveRecordAsItStands(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	e := planEngine(t, nil, old)
	seedMounted(t, e, "dev", "/ws/view")
	if err := e.Remove("dev"); err == nil {
		t.Fatal("the legacy removal accepted an active record")
	}
	plan := mustPlan(t, e, testNow)
	if len(plan.Candidates) != 1 || plan.Candidates[0].State != StateActive {
		t.Fatalf("candidates = %+v, want one active record", plan.Candidates)
	}

	report, err := e.ExecSessions(plan)
	if err != nil {
		t.Fatalf("ExecSessions: %v", err)
	}
	if want := []string{"dev"}; !sameStrings(report.Removed, want) {
		t.Fatalf("removed = %v, want %v", report.Removed, want)
	}
}

// TestExecSessionsKeepsAChangedSession refuses a session that no longer
// matches the snapshot the preview pinned.
func TestExecSessionsKeepsAChangedSession(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	t.Run("record rewritten", func(t *testing.T) {
		e := planEngine(t, nil, old)
		seedMounted(t, e, "dev", "/ws/view")
		plan := mustPlan(t, e, testNow)
		rec, err := e.Load("dev")
		if err != nil {
			t.Fatal(err)
		}
		rec.State = StateClosed
		rec.ClosedAt = old
		if err := e.Save(rec); err != nil {
			t.Fatal(err)
		}
		assertExecKept(t, e, plan, GCSkipChanged)
	})
	t.Run("record recreated", func(t *testing.T) {
		e := planEngine(t, nil, old)
		seedMounted(t, e, "dev", "/ws/view")
		plan := mustPlan(t, e, testNow)
		if err := os.RemoveAll(filepath.Join(e.Dir, "dev")); err != nil {
			t.Fatal(err)
		}
		e.Now = func() time.Time { return testNow }
		seedMounted(t, e, "dev", "/ws/view")
		assertExecKept(t, e, plan, GCSkipChanged)
	})
	t.Run("activity appended", func(t *testing.T) {
		e := planEngine(t, nil, old)
		seedMounted(t, e, "dev", "/ws/view")
		plan := mustPlan(t, e, testNow)
		w, err := e.activityWriter("dev")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Append(activity.Event{
			Event: activity.EventSessionOpen, Phase: activity.PhaseSucceeded, Session: "dev",
		}); err != nil {
			t.Fatal(err)
		}
		assertExecKept(t, e, plan, GCSkipChanged)
	})
	t.Run("activity removed", func(t *testing.T) {
		e := planEngine(t, nil, old)
		seedMounted(t, e, "dev", "/ws/view")
		plan := mustPlan(t, e, testNow)
		if err := os.Remove(filepath.Join(e.Dir, "dev", activity.LogName)); err != nil {
			t.Fatal(err)
		}
		assertExecKept(t, e, plan, GCSkipChanged)
	})
	t.Run("cutoff tightened", func(t *testing.T) {
		e := planEngine(t, nil, old)
		seedMounted(t, e, "dev", "/ws/view")
		plan := mustPlan(t, e, testNow)
		plan.Cutoff = old
		assertExecKept(t, e, plan, GCSkipChanged)
	})
	t.Run("record unreadable", func(t *testing.T) {
		e := planEngine(t, nil, old)
		seedMounted(t, e, "dev", "/ws/view")
		plan := mustPlan(t, e, testNow)
		path := filepath.Join(e.Dir, "dev", recordFileName)
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		assertExecKept(t, e, plan, GCSkipUnreadable)
	})
}

// TestExecSessionsKeepsAMountThatAppeared reads the host again while the
// locks are held, so a view mounted after the preview survives.
func TestExecSessionsKeepsAMountThatAppeared(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	e := planEngine(t, nil, old)
	seedMounted(t, e, "dev", "/ws/view")
	plan := mustPlan(t, e, testNow)
	e.LoadTable = func() ([]mountinfo.Mount, error) {
		return []mountinfo.Mount{overlayEntry("/ws/view", "/rt/m1")}, nil
	}
	assertExecKept(t, e, plan, GCSkipAttached)
}

// TestExecSessionsSkipsAContendedOrUnlockableSession never deletes a
// session whose lock it could not take.
func TestExecSessionsSkipsAContendedOrUnlockableSession(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	t.Run("busy", func(t *testing.T) {
		e := planEngine(t, nil, old)
		seedMounted(t, e, "dev", "/ws/view")
		plan := mustPlan(t, e, testNow)
		e.Lock = func(string, time.Duration) (func(), error) { return nil, ErrLockHeld }
		assertExecKept(t, e, plan, GCSkipBusy)
	})
	t.Run("unlocked", func(t *testing.T) {
		e := planEngine(t, nil, old)
		seedMounted(t, e, "dev", "/ws/view")
		plan := mustPlan(t, e, testNow)
		e.Lock = func(string, time.Duration) (func(), error) { return func() {}, ErrLockUnavailable }
		assertExecKept(t, e, plan, GCSkipUnlocked)
	})
}

// TestExecSessionsReportsAMissingSession ignores a session a concurrent
// pass already removed.
func TestExecSessionsReportsAMissingSession(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	e := planEngine(t, nil, old)
	seedMounted(t, e, "dev", "/ws/view")
	plan := mustPlan(t, e, testNow)
	if err := os.RemoveAll(filepath.Join(e.Dir, "dev")); err != nil {
		t.Fatal(err)
	}

	report, err := e.ExecSessions(plan)
	if err != nil {
		t.Fatalf("ExecSessions: %v", err)
	}
	if len(report.Removed) != 0 {
		t.Fatalf("removed = %v, want none", report.Removed)
	}
	if len(report.Kept) != 1 || report.Kept[0].Reason != GCSkipGone {
		t.Fatalf("kept = %+v, want one gone session", report.Kept)
	}
}

// TestExecSessionsKeepsSessionsOutsideThePlan proves the execution is
// restricted to the approved snapshot: it never rescans the store.
func TestExecSessionsKeepsSessionsOutsideThePlan(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	e := planEngine(t, nil, old)
	for _, name := range []string{"alpha", "beta"} {
		seedMounted(t, e, name, "/ws/"+name)
	}
	plan := mustPlan(t, e, testNow)
	approved := GCPlan{Cutoff: plan.Cutoff, Candidates: plan.Candidates[:1]}

	report, err := e.ExecSessions(approved)
	if err != nil {
		t.Fatalf("ExecSessions: %v", err)
	}
	if want := []string{"alpha"}; !sameStrings(report.Removed, want) {
		t.Fatalf("removed = %v, want %v", report.Removed, want)
	}
	if _, err := os.Stat(filepath.Join(e.Dir, "beta")); err != nil {
		t.Fatalf("the unapproved session was touched: %v", err)
	}
}

// TestExecSessionsRejectsAZeroCutoff refuses to delete against a boundary
// nobody computed.
func TestExecSessionsRejectsAZeroCutoff(t *testing.T) {
	e := planEngine(t, nil, testNow)
	seedMounted(t, e, "dev", "/ws/view")
	if _, err := e.ExecSessions(GCPlan{}); err == nil {
		t.Fatal("a zero cutoff was accepted")
	}
	if _, err := os.Stat(filepath.Join(e.Dir, "dev")); err != nil {
		t.Fatalf("the session was touched: %v", err)
	}
}

// TestExecSessionsReportsAPartialRemoval keeps the outcome of a directory
// that could not be removed completely.
func TestExecSessionsReportsAPartialRemoval(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes a read-only directory")
	}
	old := testNow.Add(-90 * 24 * time.Hour)
	e := planEngine(t, nil, old)
	seedMounted(t, e, "dev", "/ws/view")
	runtime := attachRuntime(t, e, "dev", "m1")
	if err := os.WriteFile(filepath.Join(runtime, "upper", "file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	upper := filepath.Join(runtime, "upper")
	if err := os.Chmod(upper, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(upper, 0o755) })
	plan := mustPlan(t, e, testNow)

	report, err := e.ExecSessions(plan)
	if err == nil {
		t.Fatal("a failed removal was reported as success")
	}
	if len(report.Removed) != 0 {
		t.Fatalf("removed = %v, want none", report.Removed)
	}
	if len(report.Kept) != 1 || report.Kept[0].Reason != GCSkipFailed {
		t.Fatalf("kept = %+v, want one failed session", report.Kept)
	}
	if _, err := os.Stat(filepath.Join(e.Dir, "dev")); err != nil {
		t.Fatalf("the partially removed session is gone: %v", err)
	}
}
