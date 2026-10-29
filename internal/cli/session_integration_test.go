//go:build linux

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
)

func TestSessionCrashRecoveryRestoresEveryMount(t *testing.T) {
	requireMountTestEnv(t)
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	sessionsRoot := sessionsDir(home)
	buildFullImage(t, lib, "alice/app", subdirSource(t))

	view1 := filepath.Join(work, "view1")
	view2 := filepath.Join(work, "view2")
	deferForceUmount(t, sessionsRoot, view1, view2)
	mustSessionNew(t, lib, "dev")
	mountOK(t, lib, "mounted "+view1+"\n",
		"mount", "--session", "dev", "alice/app", view1)
	mountOK(t, lib, "mounted "+view2+"\n",
		"mount", "--session", "dev", "alice/app", view2)

	// Real work happens through the first view.
	writeTestFile(t, filepath.Join(view1, "wip.txt"), "wip\n")

	rec := loadSessionRecord(t, sessionsDir(home), "dev")
	if len(rec.Actions) != 2 {
		t.Fatalf("actions = %+v", rec.Actions)
	}
	points := []string{view1, view2}
	for i, action := range rec.Actions {
		if action.Seq != i+1 || action.Type != session.TypeMount ||
			action.Status != session.StatusMounted ||
			action.Mountpoint != points[i] {
			t.Errorf("action %d = %+v", i, action)
		}
	}

	// Crash: both overlay daemons die behind the record's back.
	for _, view := range points {
		killDaemon(t, findDaemon(t, "fuse-overlayfs", view))
		waitOverlayDead(t, view)
	}

	// One open replays the record and restores every mount.
	if out := mustCmd(t, append(lib, "session", "open", "dev")...); out != "opened dev\n" {
		t.Fatalf("open output = %q", out)
	}
	assertBytesFile(t, filepath.Join(view1, "wip.txt"), "wip\n")
	assertBytesFile(t, filepath.Join(view1, "top.txt"), "top\n")
	assertBytesFile(t, filepath.Join(view2, "top.txt"), "top\n")

	// The upper data survived the crash: the recovered view serves
	// through a writable overlay again.
	entry, ok := entryAt(realTable(t), view1)
	if !ok || entry.Upper == "" {
		t.Fatalf("no writable overlay entry at %s: %+v", view1, entry)
	}
	assertBytesFile(t, filepath.Join(entry.Upper, "wip.txt"), "wip\n")
	rec = loadSessionRecord(t, sessionsDir(home), "dev")
	for _, action := range rec.Actions {
		if action.Status != session.StatusMounted {
			t.Errorf("action %+v not recovered", action)
		}
	}

	// Recovery is idempotent: a repeated open keeps the same
	// daemons serving instead of remounting healthy views.
	pid1 := findDaemon(t, "fuse-overlayfs", view1)
	pid2 := findDaemon(t, "fuse-overlayfs", view2)
	if out := mustCmd(t, append(lib, "session", "open", "dev")...); out != "opened dev\n" {
		t.Fatalf("repeated open output = %q", out)
	}
	if again := findDaemon(t, "fuse-overlayfs", view1); again != pid1 {
		t.Errorf("view1 daemon changed: %d -> %d", pid1, again)
	}
	if again := findDaemon(t, "fuse-overlayfs", view2); again != pid2 {
		t.Errorf("view2 daemon changed: %d -> %d", pid2, again)
	}

	// The session itself tears every mount down and leaves no
	// residue behind.
	stdout, stderr, _, err := runCmd(t, append(lib, "session", "close", "dev")...)
	if err != nil {
		t.Fatalf("session close: %v\n%s", err, stderr)
	}
	if stdout != "closed dev\n" {
		t.Fatalf("close output = %q", stdout)
	}
	assertNoResidue(t, sessionsRoot, view1)
	assertNoResidue(t, sessionsRoot, view2)
}

func TestSessionReverseLookupAndVanishedEntryRecovery(t *testing.T) {
	requireMountTestEnv(t)
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	sessionsRoot := sessionsDir(home)
	buildFullImage(t, lib, "alice/app", subdirSource(t))

	view := filepath.Join(work, "view")
	deferForceUmount(t, sessionsRoot, view)
	mustSessionNew(t, lib, "dev")
	mountOK(t, lib, "mounted "+view+"\n", "mount", "--session", "dev", "alice/app", view)
	writeTestFile(t, filepath.Join(view, "sub", "wip.txt"), "wip\n")

	engine := &session.Engine{
		Store:       session.Store{Dir: sessionsDir(home)},
		LoadTable:   mountinfo.Load,
		SessionsDir: sessionsDir(home),
	}

	// A deep path, the merged root, and the runtime upper all
	// resolve to the session while the view is healthy.
	deep := filepath.Join(view, "sub", "deep", "x.txt")
	entry, ok := entryAt(realTable(t), view)
	if !ok || entry.Upper == "" {
		t.Fatalf("no writable overlay entry at %s: %+v", view, entry)
	}
	for _, path := range []string{deep, view, entry.Upper} {
		hit, err := engine.SessionForPath(path)
		if err != nil {
			t.Fatalf("SessionForPath(%s): %v", path, err)
		}
		if hit.Record.Name != "dev" || hit.Mountpoint != view || hit.Stale {
			t.Fatalf("SessionForPath(%s) = %+v", path, hit)
		}
	}

	// A killed daemon makes the covering view stale while the table
	// still lists it.
	killDaemon(t, findDaemon(t, "fuse-overlayfs", view))
	waitOverlayDead(t, view)
	hit, err := engine.SessionForPath(deep)
	if err != nil {
		t.Fatalf("SessionForPath(deep) after kill: %v", err)
	}
	if hit.Record.Name != "dev" || !hit.Stale {
		t.Fatalf("SessionForPath(deep) after kill = %+v", hit)
	}

	// A lazily detached view vanishes from the table; the recorded
	// mountpoints still resolve the session as stale.
	lazy := exec.Command("fusermount3", "-uz", view)
	if out, lazyErr := lazy.CombinedOutput(); lazyErr != nil {
		t.Fatalf("fusermount3 -uz %s: %v: %s", view, lazyErr, out)
	}
	if _, listed := entryAt(realTable(t), view); listed {
		t.Fatalf("the lazy unmount left a table entry at %s", view)
	}
	hit, err = engine.SessionForPath(deep)
	if err != nil {
		t.Fatalf("SessionForPath(deep) after lazy unmount: %v", err)
	}
	if hit.Record.Name != "dev" || !hit.Stale {
		t.Fatalf("SessionForPath(deep) after lazy unmount = %+v", hit)
	}

	// One open rebuilds the vanished view with the upper data.
	if out := mustCmd(t, append(lib, "session", "open", "dev")...); out != "opened dev\n" {
		t.Fatalf("open output = %q", out)
	}
	assertBytesFile(t, filepath.Join(view, "sub", "wip.txt"), "wip\n")
	assertBytesFile(t, filepath.Join(view, "top.txt"), "top\n")

	// A path no recorded view covers reports a clear error.
	_, err = engine.SessionForPath(work)
	if err == nil || !strings.Contains(err.Error(), "no session covers") {
		t.Fatalf("SessionForPath(work) error = %v", err)
	}
}

func TestSessionEndToEndWithImplicitSessionAndGC(t *testing.T) {
	requireMountTestEnv(t)
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	sessionsRoot := sessionsDir(home)
	buildFullImage(t, lib, "alice/app", subdirSource(t))

	view := filepath.Join(work, "view")
	proj := filepath.Join(work, "proj")
	deferForceUmount(t, sessionsRoot, view, proj)

	// A plain mount creates one generated session and records the
	// mount in it.
	generated := mountCreatedOK(t, lib, "mounted "+view+"\n", "mount", "alice/app", view)
	names := listSessionNames(t, sessionsDir(home))
	if len(names) != 1 || names[0] != generated {
		t.Fatalf("sessions = %v, want [%s]", names, generated)
	}
	writeTestFile(t, filepath.Join(view, "sub", "wip.txt"), "wip\n")
	if out := mustCmd(t, append(lib, "session", "list")...); !strings.Contains(out, generated) ||
		!strings.Contains(out, "active") {
		t.Fatalf("list output = %q", out)
	}

	// Close tears the view down and keeps the record.
	if out := mustCmd(t, append(lib, "session", "close", generated)...); out != "closed "+generated+"\n" {
		t.Fatalf("close output = %q", out)
	}
	if _, err := os.Stat(filepath.Join(view, "sub", "wip.txt")); err == nil {
		t.Fatal("the closed session still serves its view")
	}

	// Open resumes the closed session: the view remounts with the
	// upper data and the record becomes active again.
	if out := mustCmd(t, append(lib, "session", "open", generated)...); out != "opened "+generated+"\n" {
		t.Fatalf("open output = %q", out)
	}
	assertBytesFile(t, filepath.Join(view, "sub", "wip.txt"), "wip\n")
	rec := loadSessionRecord(t, sessionsDir(home), generated)
	if rec.State != session.StateActive || !rec.ClosedAt.IsZero() {
		t.Fatalf("record = %+v, want an active record with no close time", rec)
	}

	// Close again; session GC with zero retention can remove the idle record.
	if out := mustCmd(t, append(lib, "session", "close", generated)...); out != "closed "+generated+"\n" {
		t.Fatalf("second close output = %q", out)
	}
	if out := mustCmd(t, append(lib, "session", "gc", "--expire=0", "--yes")...); !strings.Contains(out, "removed "+generated+"\n") {
		t.Fatalf("gc output = %q", out)
	}
	if left := listSessionNames(t, sessionsDir(home)); len(left) != 0 {
		t.Fatalf("sessions after gc = %v", left)
	}

	// The explicit flow shares the same surface end to end.
	mustSessionNew(t, lib, "proj")
	mountOK(t, lib, "mounted "+proj+"\n", "mount", "--session", "proj", "alice/app", proj)
	assertBytesFile(t, filepath.Join(proj, "top.txt"), "top\n")
	if out := mustCmd(t, append(lib, "session", "close", "proj")...); out != "closed proj\n" {
		t.Fatalf("close output = %q", out)
	}
	if out := mustCmd(t, append(lib, "session", "remove", "proj")...); out != "removed proj\n" {
		t.Fatalf("remove output = %q", out)
	}
	if left := listSessionNames(t, sessionsDir(home)); len(left) != 0 {
		t.Fatalf("sessions after remove = %v", left)
	}
}
