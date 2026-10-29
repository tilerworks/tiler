//go:build linux || darwin

package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/filelock"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/mountinfo"
)

// execVersionFixture builds one closed session over a live version reference
// and returns the plan that offers it.
func execVersionFixture(t *testing.T) (*Engine, ReferenceCoordinator, Action, GCPlan) {
	t.Helper()
	e, c, action := versionEngine(t)
	e.SessionsDir = e.Dir
	e.LoadTable = func() ([]mountinfo.Mount, error) { return nil, nil }
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RunMount("dev", action, func(stored Action) (Result, error) {
		return Result{Mountpoint: stored.Mountpoint}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Close("dev", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	plan := mustPlan(t, e, testNow.Add(time.Hour))
	if len(plan.Candidates) != 1 || plan.Candidates[0].Name != "dev" {
		t.Fatalf("candidates = %v, want the closed session", planNames(plan))
	}
	return e, c, action, plan
}

// TestExecSessionsRevokesReferencesBeforeRemoving deletes the session through
// the coordinated path: the record, its references, and its directory all go
// away together.
func TestExecSessionsRevokesReferencesBeforeRemoving(t *testing.T) {
	e, c, action, plan := execVersionFixture(t)
	requireVersionOwners(t, c, action, 1, 1)

	report, err := e.ExecSessions(plan)
	if err != nil {
		t.Fatalf("ExecSessions: %v", err)
	}
	if want := []string{"dev"}; !sameStrings(report.Removed, want) {
		t.Fatalf("removed = %v, want %v", report.Removed, want)
	}
	if _, err := c.LoadVersion("dev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the record survived: %v", err)
	}
	requireVersionOwners(t, c, action, 0, 0)
	if _, err := os.Stat(filepath.Join(e.Dir, "dev")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the session directory survived: %v", err)
	}
}

// TestExecSessionsRechecksTheHostUnderTheCoordinatedLocks proves the
// attachment answer that authorizes a deletion is read while both
// coordinated locks are held, so no mount can interleave behind it.
func TestExecSessionsRechecksTheHostUnderTheCoordinatedLocks(t *testing.T) {
	e, c, _, plan := execVersionFixture(t)
	e.LoadTable = func() ([]mountinfo.Mount, error) {
		if release, err := image.LockLibrary(c.ImagesDir, 0); err == nil {
			release()
			t.Error("the attachment guard read the host outside the library lock")
		} else if !errors.Is(err, filelock.ErrHeld) {
			t.Errorf("library lock probe = %v", err)
		}
		if release, err := c.lockSession("dev", 0); err == nil {
			release()
			t.Error("the attachment guard read the host outside the session lock")
		} else if !errors.Is(err, ErrLockHeld) {
			t.Errorf("session lock probe = %v", err)
		}
		return nil, nil
	}

	report, err := e.ExecSessions(plan)
	if err != nil {
		t.Fatalf("ExecSessions: %v", err)
	}
	if want := []string{"dev"}; !sameStrings(report.Removed, want) {
		t.Fatalf("removed = %v, want %v", report.Removed, want)
	}
}

// TestExecSessionsKeepsASessionWhenReferencesFail refuses the whole deletion
// when the reference bookkeeping cannot be trusted, so an authorized record
// never loses its protection halfway.
func TestExecSessionsKeepsASessionWhenReferencesFail(t *testing.T) {
	e, _, _, plan := execVersionFixture(t)
	path, err := e.referencePath("journal.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := e.ExecSessions(plan)
	if err == nil {
		t.Fatal("a reference failure was reported as success")
	}
	if len(report.Removed) != 0 {
		t.Fatalf("removed = %v, want none", report.Removed)
	}
	if len(report.Kept) != 1 || report.Kept[0].Reason != GCSkipFailed {
		t.Fatalf("kept = %+v, want one failed session", report.Kept)
	}
	if _, err := os.Stat(filepath.Join(e.Dir, "dev")); err != nil {
		t.Fatalf("the session is gone: %v", err)
	}
}

// TestExecSessionsSkipsABusyVersionedSession reports contention from the
// coordinated locks instead of waiting for it.
func TestExecSessionsSkipsABusyVersionedSession(t *testing.T) {
	e, _, _, plan := execVersionFixture(t)
	release, err := e.lockSession("dev", 0)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer release()

	report, err := e.ExecSessions(plan)
	if err != nil {
		t.Fatalf("ExecSessions: %v", err)
	}
	if len(report.Removed) != 0 {
		t.Fatalf("removed = %v, want none", report.Removed)
	}
	if len(report.Kept) != 1 || report.Kept[0].Reason != GCSkipBusy {
		t.Fatalf("kept = %+v, want one busy session", report.Kept)
	}
	if _, err := os.Stat(filepath.Join(e.Dir, "dev")); err != nil {
		t.Fatalf("the busy session was touched: %v", err)
	}
}

// TestExecSessionsSkipsALockedLibrary covers the other coordinated lock: a
// library held by a concurrent build reports contention too.
func TestExecSessionsSkipsALockedLibrary(t *testing.T) {
	e, c, _, plan := execVersionFixture(t)
	release, err := image.LockLibrary(c.ImagesDir, 0)
	if err != nil {
		t.Fatalf("library lock: %v", err)
	}
	defer release()

	report, err := e.ExecSessions(plan)
	if err != nil {
		t.Fatalf("ExecSessions: %v", err)
	}
	if len(report.Removed) != 0 {
		t.Fatalf("removed = %v, want none", report.Removed)
	}
	if len(report.Kept) != 1 || report.Kept[0].Reason != GCSkipBusy {
		t.Fatalf("kept = %+v, want one busy session", report.Kept)
	}
	if _, err := os.Stat(filepath.Join(e.Dir, "dev")); err != nil {
		t.Fatalf("the session was touched while the library was locked: %v", err)
	}
}
