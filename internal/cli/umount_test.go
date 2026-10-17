package cli

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
)

func TestUmountPrintsUnmountedWhenRemoved(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	view := filepath.Join(work, "view")
	table := []mountinfo.Mount{{Point: view, FSType: "fuse.fuse-overlayfs", Source: "overlay"}}
	f := withMountSeams(t, table)
	f.removed = 4

	stdout, stderr, _, err := runCmd(t, append(lib, "umount", view)...)
	if err != nil {
		t.Fatalf("umount: %v\n%s", err, stderr)
	}
	if stdout != "unmounted "+view+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if len(f.umountReqs) != 1 {
		t.Fatalf("engine calls = %d", len(f.umountReqs))
	}
	req := f.umountReqs[0]
	if req.Dir != view || req.RuntimeDir != filepath.Join(home, "lib", "runtime") {
		t.Errorf("request = %+v", req)
	}
	if len(req.Table) != 1 || req.Table[0].Point != view {
		t.Errorf("table = %+v", req.Table)
	}
	if req.Reload == nil {
		t.Error("the reload seam is not wired")
	}
	if req.Run == nil {
		t.Fatal("the tool runner seam is not wired")
	}
	if err := req.Run("probe-tool"); err != nil {
		t.Fatal(err)
	}
	if len(f.toolCalls) != 1 || f.toolCalls[0][0] != "probe-tool" {
		t.Errorf("tool calls = %v", f.toolCalls)
	}
}

func TestUmountSilentWhenNothingRemoved(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	f.removed = 0

	stdout, stderr, _, err := runCmd(t, append(lib, "umount", filepath.Join(work, "view"))...)
	if err != nil {
		t.Fatalf("umount: %v\n%s", err, stderr)
	}
	if stdout != "" {
		t.Fatalf("an idempotent no-op printed %q", stdout)
	}
	if len(f.umountReqs) != 1 {
		t.Fatalf("engine calls = %d", len(f.umountReqs))
	}
}

func TestUmountReloadRereadsTableSeam(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	view := filepath.Join(work, "view")
	f := withMountSeams(t, nil)
	f.removed = 1
	other := filepath.Join(home, "lib", "runtime", "up-1-2", "lower-0")
	f.table = []mountinfo.Mount{{Point: other, FSType: "fuse", Source: "/img/image.sqsh"}}

	if _, stderr, _, err := runCmd(t, append(lib, "umount", view)...); err != nil {
		t.Fatalf("umount: %v\n%s", err, stderr)
	}
	entries, err := f.umountReqs[0].Reload()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Point != other {
		t.Fatalf("reload entries = %+v, want the refreshed table", entries)
	}
}

func TestUmountFailureIsRuntimeError(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	f.umountErr = errors.New("workspace: /view is still mounted after unmounting")

	_, _, _, err := runCmd(t, append(lib, "umount", filepath.Join(work, "view"))...)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
}

func TestUmountArgCount(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	view := filepath.Join(work, "view")

	if _, _, _, err := runCmd(t, append(lib, "umount")...); ExitCode(err) != 2 {
		t.Fatalf("no argument: ExitCode(%v) = %d, want 2", err, ExitCode(err))
	}
	if _, _, _, err := runCmd(t, append(lib, "umount", view, view)...); ExitCode(err) != 2 {
		t.Fatalf("two arguments: ExitCode(%v) = %d, want 2", err, ExitCode(err))
	}
}

func TestUmountClosesImplicitSessionAfterLastView(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	view := filepath.Join(work, "view")
	f := withMountSeams(t, nil)
	f.removed = 1
	const auto = "auto-20261017T031500Z-01020304"
	writeSessionRecord(t, sessionsDir(home), auto,
		libraryAction("alice/app", view, session.ModeRW))

	stdout, stderr, _, err := runCmd(t, append(lib, "umount", view)...)
	if err != nil {
		t.Fatalf("umount: %v\n%s", err, stderr)
	}
	if stdout != "unmounted "+view+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	rec := loadSessionRecord(t, sessionsDir(home), auto)
	if rec.State != session.StateClosed || rec.ClosedAt.IsZero() {
		t.Fatalf("record = %+v", rec)
	}
	for _, action := range rec.Actions {
		if action.Type == session.TypeMount && action.Status != session.StatusUnmounted {
			t.Errorf("action = %+v", action)
		}
	}
}

func TestUmountKeepsExplicitSessionAndLiveViewsActive(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	view := filepath.Join(work, "view")
	other := filepath.Join(work, "other")
	f := withMountSeams(t, nil)
	f.removed = 1
	writeSessionRecord(t, sessionsDir(home), "dev",
		libraryAction("alice/app", view, session.ModeRW))
	const auto = "auto-20261017T031500Z-01020304"
	writeSessionRecord(t, sessionsDir(home), auto,
		libraryAction("alice/app", view, session.ModeRW),
		libraryAction("alice/app", other, session.ModeRW))
	// The second view of the implicit session still serves, so
	// neither record may auto-close.
	withSessionSeams(t, func(engine *session.Engine) {
		engine.Health = func(point string) bool { return point == other }
	})

	if _, stderr, _, err := runCmd(t, append(lib, "umount", view)...); err != nil {
		t.Fatalf("umount: %v\n%s", err, stderr)
	}
	rec := loadSessionRecord(t, sessionsDir(home), "dev")
	if rec.State != session.StateActive {
		t.Errorf("explicit record = %+v", rec)
	}
	rec = loadSessionRecord(t, sessionsDir(home), auto)
	if rec.State != session.StateActive {
		t.Errorf("implicit record with a live view = %+v", rec)
	}
}
