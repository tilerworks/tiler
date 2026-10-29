package session

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
	"github.com/jiangxin/tiler/internal/mountinfo"
)

// planEngine builds an engine over one fake mount table whose clock
// writes activity at at, so a scan can expire what it created.
func planEngine(t *testing.T, table []mountinfo.Mount, at time.Time) *Engine {
	t.Helper()
	sessions := t.TempDir()
	e := newEngine(sessions)
	e.SessionsDir = sessions
	e.Now = func() time.Time { return at }
	e.LoadTable = func() ([]mountinfo.Mount, error) { return table, nil }
	return e
}

// planNames lists the candidate names of one plan.
func planNames(plan GCPlan) []string {
	names := make([]string, 0, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		names = append(names, candidate.Name)
	}
	return names
}

// planSkips lists the kept sessions as "name reason" pairs.
func planSkips(plan GCPlan) []string {
	out := make([]string, 0, len(plan.Skips))
	for _, skip := range plan.Skips {
		out = append(out, skip.Name+" "+skip.Reason)
	}
	return out
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestPlanSessionsReportsExpiredActiveAndClosedRecords expires on
// activity alone: a stored state never decides, and candidates come
// back in name order.
func TestPlanSessionsReportsExpiredActiveAndClosedRecords(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	e := planEngine(t, nil, old)
	seedMounted(t, e, "idle", "/ws/idle")
	seedMounted(t, e, "closed", "/ws/closed")
	if _, err := e.Close("closed", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	seedRemovableMounted(t, e, "removable")

	plan, err := e.PlanSessions(testNow)
	if err != nil {
		t.Fatalf("PlanSessions: %v", err)
	}
	if want := []string{"closed", "idle", "removable"}; !sameStrings(planNames(plan), want) {
		t.Fatalf("candidates = %v, want %v", planNames(plan), want)
	}
	if len(plan.Skips) != 0 {
		t.Fatalf("skips = %v, want none", planSkips(plan))
	}
	for _, candidate := range plan.Candidates {
		if !candidate.LastActive.Equal(old) {
			t.Fatalf("%s last active = %v, want %v", candidate.Name, candidate.LastActive, old)
		}
		if candidate.Name == "removable" && !candidate.RemoveOnClose {
			t.Fatalf("%s lost its --rm flag", candidate.Name)
		}
		if candidate.Name == "closed" && candidate.State != StateClosed {
			t.Fatalf("%s state = %q, want the stored closed state", candidate.Name, candidate.State)
		}
	}
	if !plan.Cutoff.Equal(testNow) {
		t.Fatalf("cutoff = %v, want %v", plan.Cutoff, testNow)
	}
}

// TestPlanSessionsKeepsAnAttachedView covers the dead-daemon case: the
// stored state says closed, the host still has the mount.
func TestPlanSessionsKeepsAnAttachedView(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	e := planEngine(t, []mountinfo.Mount{overlayEntry("/ws/view", "/rt/m1")}, old)
	seedMounted(t, e, "dev", "/ws/view")
	if err := e.Writeback("dev", 1, StatusUnmounted, ""); err != nil {
		t.Fatal(err)
	}

	plan, err := e.PlanSessions(testNow)
	if err != nil {
		t.Fatalf("PlanSessions: %v", err)
	}
	if len(plan.Candidates) != 0 {
		t.Fatalf("candidates = %v, want none", planNames(plan))
	}
	if len(plan.Skips) != 1 || plan.Skips[0].Reason != GCSkipAttached ||
		plan.Skips[0].Detail != "/ws/view" {
		t.Fatalf("skips = %+v, want one attached skip naming the view", plan.Skips)
	}
}

// TestPlanSessionsInitializesMissingActivityOnce covers the bootstrap:
// the scan that creates a log never expires its own object, and the
// next scan decides from that timestamp.
func TestPlanSessionsInitializesMissingActivityOnce(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	e := planEngine(t, nil, testNow)
	e.Activity = func(string) (*activity.Writer, error) { return nil, nil }
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	if err := e.Save(Record{Name: "dev", State: StateActive, CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	e.Activity = nil
	cutoff := testNow.Add(time.Hour)

	plan, err := e.PlanSessions(cutoff)
	if err != nil {
		t.Fatalf("PlanSessions: %v", err)
	}
	if len(plan.Candidates) != 0 {
		t.Fatalf("candidates = %v, want the initialized session kept", planNames(plan))
	}
	if len(plan.Skips) != 1 || plan.Skips[0].Reason != GCSkipInitialized {
		t.Fatalf("skips = %+v, want one initialized skip", plan.Skips)
	}
	if lines := readActivityLines(t, e, "dev"); len(lines) != 1 {
		t.Fatalf("records = %d, want one initialize record", len(lines))
	}

	plan, err = e.PlanSessions(cutoff)
	if err != nil {
		t.Fatalf("second PlanSessions: %v", err)
	}
	if want := []string{"dev"}; !sameStrings(planNames(plan), want) {
		t.Fatalf("candidates = %v, want %v from the recorded time", planNames(plan), want)
	}
}

// TestPlanSessionsNeverRenewsExistingActivity keeps repeated scans
// free of activity effects: the log keeps its record and its time.
func TestPlanSessionsNeverRenewsExistingActivity(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	e := planEngine(t, nil, old)
	seedMounted(t, e, "dev", "/ws/view")
	before := readActivityLines(t, e, "dev")

	for range 3 {
		plan, err := e.PlanSessions(old)
		if err != nil {
			t.Fatalf("PlanSessions: %v", err)
		}
		if len(plan.Candidates) != 0 || len(plan.Skips) != 0 {
			t.Fatalf("plan = %v %v, want nothing at the recorded activity", planNames(plan), planSkips(plan))
		}
	}
	if after := readActivityLines(t, e, "dev"); !sameStrings(before, after) {
		t.Fatalf("records = %v, want the unchanged %v", after, before)
	}
}

// readActivityLines returns the session log as raw lines.
func readActivityLines(t *testing.T, e *Engine, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.Dir, name, activity.LogName))
	if err != nil {
		t.Fatal(err)
	}
	return splitLines(string(data))
}

// splitLines splits a log into its encoded lines; every record ends
// with a newline, so the split leaves one empty tail element.
func splitLines(text string) []string {
	lines := strings.Split(text, "\n")
	return lines[:len(lines)-1]
}

// TestPlanSessionsKeepsWhatItCannotInspect never reports a session as
// expired when its record or log cannot be trusted.
func TestPlanSessionsKeepsWhatItCannotInspect(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	t.Run("malformed record", func(t *testing.T) {
		e := planEngine(t, nil, old)
		dir := filepath.Join(e.Dir, "broken")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, recordFileName), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		assertOnlySkip(t, e, GCSkipUnreadable)
	})
	t.Run("damaged log", func(t *testing.T) {
		e := planEngine(t, nil, old)
		seedMounted(t, e, "dev", "/ws/view")
		path := filepath.Join(e.Dir, "dev", activity.LogName)
		if err := os.WriteFile(path, []byte("{truncated"), 0o600); err != nil {
			t.Fatal(err)
		}
		assertOnlySkip(t, e, GCSkipUnreadable)
	})
}

// assertOnlySkip expects one kept session and no candidate.
func assertOnlySkip(t *testing.T, e *Engine, reason string) {
	t.Helper()
	plan, err := e.PlanSessions(testNow)
	if err != nil {
		t.Fatalf("PlanSessions: %v", err)
	}
	if len(plan.Candidates) != 0 {
		t.Fatalf("candidates = %v, want none", planNames(plan))
	}
	if len(plan.Skips) != 1 || plan.Skips[0].Reason != reason || plan.Skips[0].Detail == "" {
		t.Fatalf("skips = %+v, want one %s skip with a reason", plan.Skips, reason)
	}
}

// TestPlanSessionsSkipsABusySession reports contention instead of
// waiting for it or expiring the session anyway.
func TestPlanSessionsSkipsABusySession(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("advisory locks decide contention only on these hosts")
	}
	old := testNow.Add(-90 * 24 * time.Hour)
	e := planEngine(t, nil, old)
	e.Lock = nil
	seedMounted(t, e, "dev", "/ws/view")
	release, err := e.lockFor("dev", 0)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer release()

	assertOnlySkip(t, e, GCSkipBusy)
}

// TestPlanSessionsKeepsTheCutoffBoundary keeps a session whose last
// activity equals the cutoff and expires the one nanosecond before.
func TestPlanSessionsKeepsTheCutoffBoundary(t *testing.T) {
	old := testNow.Add(-30 * 24 * time.Hour)
	e := planEngine(t, nil, old)
	seedMounted(t, e, "dev", "/ws/view")

	plan, err := e.PlanSessions(old)
	if err != nil {
		t.Fatalf("PlanSessions: %v", err)
	}
	if len(plan.Candidates) != 0 || len(plan.Skips) != 0 {
		t.Fatalf("plan = %v %v, want the boundary kept", planNames(plan), planSkips(plan))
	}

	plan, err = e.PlanSessions(old.Add(time.Nanosecond))
	if err != nil {
		t.Fatalf("PlanSessions: %v", err)
	}
	if want := []string{"dev"}; !sameStrings(planNames(plan), want) {
		t.Fatalf("candidates = %v, want %v just past the boundary", planNames(plan), want)
	}
}

// TestPlanSessionsPinsTheRevisionItSaw gives a later execution the
// identity and the activity it must find again before deleting.
func TestPlanSessionsPinsTheRevisionItSaw(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	e := planEngine(t, nil, old)
	seedMounted(t, e, "dev", "/ws/view")

	first, err := e.PlanSessions(testNow)
	if err != nil {
		t.Fatalf("PlanSessions: %v", err)
	}
	if len(first.Candidates) != 1 {
		t.Fatalf("candidates = %v, want one", planNames(first))
	}
	pinned := first.Candidates[0]
	if pinned.record.Digest == "" || pinned.record.Size == 0 || pinned.activity.Digest == "" {
		t.Fatalf("candidate = %+v, want a record stamp and an activity revision", pinned)
	}
	rev, err := e.activityLog("dev")
	if err != nil {
		t.Fatal(err)
	}
	current, err := rev.Revision()
	if err != nil {
		t.Fatal(err)
	}
	if current != pinned.activity {
		t.Fatalf("candidate revision = %+v, want the log's %+v", pinned.activity, current)
	}

	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	rec.State = StateClosed
	rec.ClosedAt = old
	if err := e.Save(rec); err != nil {
		t.Fatal(err)
	}
	second, err := e.PlanSessions(testNow)
	if err != nil {
		t.Fatalf("PlanSessions: %v", err)
	}
	if len(second.Candidates) != 1 {
		t.Fatalf("candidates = %v, want one", planNames(second))
	}
	if second.Candidates[0].record == pinned.record {
		t.Fatal("a rewritten record kept the planned stamp")
	}
}

// TestPlanSessionsFailsWhenTheHostCannotBeRead refuses a whole scan
// whose attachments are unknown.
func TestPlanSessionsFailsWhenTheHostCannotBeRead(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	e := planEngine(t, nil, old)
	seedMounted(t, e, "dev", "/ws/view")
	e.LoadTable = func() ([]mountinfo.Mount, error) { return nil, mountinfo.ErrUnavailable }

	if _, err := e.PlanSessions(testNow); !errors.Is(err, mountinfo.ErrUnavailable) {
		t.Fatalf("PlanSessions error = %v, want the inspection failure", err)
	}
}

// TestPlanSessionsRejectsAZeroCutoff refuses to guess a boundary.
func TestPlanSessionsRejectsAZeroCutoff(t *testing.T) {
	e := planEngine(t, nil, testNow)
	seedMounted(t, e, "dev", "/ws/view")
	if _, err := e.PlanSessions(time.Time{}); err == nil {
		t.Fatal("a zero cutoff was accepted")
	}
}

// TestPlanSessionsTouchesNoSession proves planning is read-only: the
// record, the runtime directory, and the log all survive a scan that
// lists the session as a candidate.
func TestPlanSessionsTouchesNoSession(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	e := planEngine(t, nil, old)
	seedMounted(t, e, "dev", "/ws/view")
	runtime := attachRuntime(t, e, "dev", "m1")
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}

	plan, err := e.PlanSessions(testNow)
	if err != nil {
		t.Fatalf("PlanSessions: %v", err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("candidates = %v, want one", planNames(plan))
	}
	stored, err := e.Load("dev")
	if err != nil {
		t.Fatalf("the record was removed: %v", err)
	}
	if stored.State != rec.State || len(stored.Actions) != len(rec.Actions) {
		t.Fatalf("record = %+v, want the unchanged %+v", stored, rec)
	}
	if _, err := os.Stat(filepath.Join(runtime, "upper")); err != nil {
		t.Fatalf("the runtime directory was touched: %v", err)
	}
}

// TestPlanSessionsReadsTheHostOnce keeps one scan cheap: the mount
// table, or the hdiutil listing, is a single snapshot shared by every
// session instead of one host query per record.
func TestPlanSessionsReadsTheHostOnce(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	e := planEngine(t, nil, old)
	for _, name := range []string{"alpha", "beta", "gamma"} {
		seedMounted(t, e, name, "/ws/"+name)
	}
	loads := 0
	e.LoadTable = func() ([]mountinfo.Mount, error) {
		loads++
		return nil, nil
	}

	plan, err := e.PlanSessions(testNow)
	if err != nil {
		t.Fatalf("PlanSessions: %v", err)
	}
	if len(plan.Candidates) != 3 {
		t.Fatalf("candidates = %v, want all three sessions", planNames(plan))
	}
	if loads != 1 {
		t.Fatalf("table loads = %d, want one snapshot for the whole scan", loads)
	}
}

// TestPlanSessionsSnapshotsTheDiskListingOnce covers the other host
// shortcut: where one hdiutil info listing drives the guard, the scan
// reads it once and decides every session from it.
func TestPlanSessionsSnapshotsTheDiskListingOnce(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	e := planEngine(t, nil, old)
	for _, name := range []string{"alpha", "beta", "gamma"} {
		seedMounted(t, e, name, "/ws/"+name)
	}
	loads := 0
	e.LoadDiskImages = func() ([]DiskImage, error) {
		loads++
		return []DiskImage{{Point: "/ws/alpha"}, {Point: "/ws/beta"}}, nil
	}

	plan, err := e.PlanSessions(testNow)
	if err != nil {
		t.Fatalf("PlanSessions: %v", err)
	}
	if want := []string{"gamma"}; !sameStrings(planNames(plan), want) {
		t.Fatalf("candidates = %v, want %v from the listing", planNames(plan), want)
	}
	if want := []string{"alpha attached", "beta attached"}; !sameStrings(planSkips(plan), want) {
		t.Fatalf("skips = %v, want %v", planSkips(plan), want)
	}
	if loads != 1 {
		t.Fatalf("listing loads = %d, want one snapshot for the whole scan", loads)
	}
}

// TestPlanSessionsSeparatesALockFailureFromContention never blames
// another process for a lock it could not take, and still reads a
// platform that offers no lock at all.
func TestPlanSessionsSeparatesALockFailureFromContention(t *testing.T) {
	old := testNow.Add(-90 * 24 * time.Hour)
	t.Run("failure", func(t *testing.T) {
		e := planEngine(t, nil, old)
		seedMounted(t, e, "dev", "/ws/view")
		e.Lock = func(string, time.Duration) (func(), error) {
			return nil, os.ErrPermission
		}

		plan, err := e.PlanSessions(testNow)
		if err != nil {
			t.Fatalf("PlanSessions: %v", err)
		}
		if len(plan.Candidates) != 0 {
			t.Fatalf("candidates = %v, want none", planNames(plan))
		}
		if len(plan.Skips) != 1 || plan.Skips[0].Reason != GCSkipUnreadable {
			t.Fatalf("skips = %+v, want one unreadable skip", plan.Skips)
		}
		if detail := plan.Skips[0].Detail; !strings.Contains(detail, os.ErrPermission.Error()) {
			t.Fatalf("detail = %q, want the lock failure it hit", detail)
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		e := planEngine(t, nil, old)
		seedMounted(t, e, "dev", "/ws/view")
		e.Lock = func(string, time.Duration) (func(), error) {
			return func() {}, ErrLockUnavailable
		}

		plan, err := e.PlanSessions(testNow)
		if err != nil {
			t.Fatalf("PlanSessions: %v", err)
		}
		if want := []string{"dev"}; !sameStrings(planNames(plan), want) {
			t.Fatalf("candidates = %v, want %v read unlocked", planNames(plan), want)
		}
	})
}
