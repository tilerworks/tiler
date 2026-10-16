package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
	"github.com/jiangxin/tiler/internal/workspace"
)

// writeSessionRecord seeds one active session record through the
// authoritative store, with the given actions renumbered in order.
func writeSessionRecord(t *testing.T, dir, name string, actions ...session.Action) {
	t.Helper()
	now := time.Date(2026, 10, 17, 3, 15, 0, 0, time.UTC)
	for i := range actions {
		actions[i].Seq = i + 1
		if actions[i].Status == "" {
			actions[i].Status = session.StatusMounted
		}
		if actions[i].Timestamp.IsZero() {
			actions[i].Timestamp = now
		}
	}
	store := session.Store{Dir: dir}
	rec := session.Record{
		Name:      name,
		State:     session.StateActive,
		Actions:   actions,
		CreatedAt: now,
	}
	if err := store.Save(rec); err != nil {
		t.Fatal(err)
	}
}

// libraryAction is one recorded library mount action.
func libraryAction(imageRef, mountpoint, mode string) session.Action {
	return session.Action{
		Type:       session.TypeMount,
		Image:      imageRef,
		Owner:      "alice",
		Repo:       "app",
		FullID:     "0123456789abcdef",
		Mountpoint: mountpoint,
		Mode:       mode,
	}
}

func TestSessionNewListShowRemoveLifecycle(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)

	stdout, stderr, _, err := runCmd(t, append(lib, "session", "new", "dev")...)
	if err != nil {
		t.Fatalf("session new: %v\n%s", err, stderr)
	}
	if stdout != "created dev\n" {
		t.Fatalf("new stdout = %q", stdout)
	}
	// A duplicate is a runtime refusal.
	_, _, _, err = runCmd(t, append(lib, "session", "new", "dev")...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate: ExitCode(%v) = %d", err, ExitCode(err))
	}

	stdout, stderr, _, err = runCmd(t, append(lib, "session", "list")...)
	if err != nil {
		t.Fatalf("session list: %v\n%s", err, stderr)
	}
	if stdout != "dev active mounts=0\n" {
		t.Fatalf("list stdout = %q", stdout)
	}

	stdout, stderr, _, err = runCmd(t, append(lib, "session", "show", "dev")...)
	if err != nil {
		t.Fatalf("session show: %v\n%s", err, stderr)
	}
	if !strings.HasPrefix(stdout, "dev active created=2026-") {
		t.Fatalf("show stdout = %q", stdout)
	}

	// An active record is refused; after close it deletes.
	_, _, _, err = runCmd(t, append(lib, "session", "remove", "dev")...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "close it before removing") {
		t.Fatalf("remove active: ExitCode(%v) = %d: %v", err, ExitCode(err), err)
	}
	if stdout, stderr, err = mustClose(t, lib, "dev"); err != nil {
		t.Fatalf("session close: %v\n%s", err, stderr)
	}
	if stdout != "closed dev\n" {
		t.Fatalf("close stdout = %q", stdout)
	}
	stdout, _, _, err = runCmd(t, append(lib, "session", "list")...)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "dev closed mounts=0\n" {
		t.Fatalf("closed list stdout = %q", stdout)
	}
	stdout, stderr, _, err = runCmd(t, append(lib, "session", "remove", "dev")...)
	if err != nil {
		t.Fatalf("session remove: %v\n%s", err, stderr)
	}
	if stdout != "removed dev\n" {
		t.Fatalf("remove stdout = %q", stdout)
	}
	if stdout, _, _, err = runCmd(t, append(lib, "session", "list")...); err != nil || stdout != "" {
		t.Fatalf("list after remove = %q (%v)", stdout, err)
	}
}

func mustClose(t *testing.T, lib []string, name string) (string, string, error) {
	t.Helper()
	stdout, stderr, _, err := runCmd(t, append(lib, "session", "close", name)...)
	return stdout, stderr, err
}

func TestSessionUsageErrors(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	tests := []struct {
		name string
		args []string
	}{
		{name: "new without a name", args: []string{"session", "new"}},
		{name: "new with two names", args: []string{"session", "new", "a", "b"}},
		{name: "new with a path-shaped name", args: []string{"session", "new", "a/b"}},
		{name: "new with a dot name", args: []string{"session", "new", ".."}},
		{name: "open without a name", args: []string{"session", "open"}},
		{name: "open with a blank name", args: []string{"session", "open", " "}},
		{name: "close without a name", args: []string{"session", "close"}},
		{name: "remove without a name", args: []string{"session", "remove"}},
		{name: "show without a name", args: []string{"session", "show"}},
		{name: "list with an argument", args: []string{"session", "list", "dev"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := runCmd(t, append(lib, tc.args...)...)
			if ExitCode(err) != 2 {
				t.Fatalf("ExitCode(%v) = %d, want 2", err, ExitCode(err))
			}
		})
	}
}

func TestSessionUnknownNameIsRuntimeError(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	for _, args := range [][]string{
		{"session", "open", "absent"},
		{"session", "close", "absent"},
		{"session", "remove", "absent"},
		{"session", "show", "absent"},
	} {
		_, _, _, err := runCmd(t, append(lib, args...)...)
		if ExitCode(err) != 1 {
			t.Fatalf("%v: ExitCode(%v) = %d, want 1", args, err, ExitCode(err))
		}
	}
}

func TestSessionOpenReplaysMissingMountsAndIsIdempotent(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	repoDir := buildLibrary(t, home, lib, imagesDir, "alice/app")
	view := filepath.Join(work, "view")
	f := withMountSeams(t, nil)
	writeSessionRecord(t, sessionsDir(home), "dev",
		libraryAction("alice/app", view, session.ModeRW))

	stdout, stderr, _, err := runCmd(t, append(lib, "session", "open", "dev")...)
	if err != nil {
		t.Fatalf("session open: %v\n%s", err, stderr)
	}
	if stdout != "opened dev\n" {
		t.Fatalf("open stdout = %q", stdout)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("engine calls = %d, want 1", len(f.mountReqs))
	}
	req := f.mountReqs[0]
	if req.Full != filepath.Join(repoDir, "image.sqsh") || req.MergedDir != view {
		t.Fatalf("request = %+v", req)
	}
	if req.ReadOnly {
		t.Errorf("a recorded rw view replayed read-only: %+v", req)
	}
	rec := loadSessionRecord(t, sessionsDir(home), "dev")
	if rec.Actions[0].Status != session.StatusMounted {
		t.Fatalf("action = %+v", rec.Actions[0])
	}

	// With the view listed and readable, a repeated open is a pure
	// no-op: the fake engine is never called again.
	if err := os.MkdirAll(view, 0o755); err != nil {
		t.Fatal(err)
	}
	f.table = []mountinfo.Mount{{Point: view, FSType: "fuse.fuse-overlayfs"}}
	stdout, stderr, _, err = runCmd(t, append(lib, "session", "open", "dev")...)
	if err != nil {
		t.Fatalf("second open: %v\n%s", err, stderr)
	}
	if stdout != "opened dev\n" {
		t.Fatalf("second open stdout = %q", stdout)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("a healthy view was remounted: %d calls", len(f.mountReqs))
	}
}

func TestSessionOpenResolvesFileImagesAndModes(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	bare := filepath.Join(work, "bare.sqsh")
	writeTestFile(t, bare, "not really squashfs")
	roView := filepath.Join(work, "ro-view")
	subView := filepath.Join(work, "sub-view")
	writeSessionRecord(t, sessionsDir(home), "dev",
		session.Action{
			Type: session.TypeMount, Image: bare,
			Mountpoint: roView, Mode: session.ModeRO,
		},
		session.Action{
			Type: session.TypeMount, Image: bare,
			Mountpoint: subView, Mode: session.ModeRW, Subdir: "src/lib",
		})

	if _, stderr, _, err := runCmd(t, append(lib, "session", "open", "dev")...); err != nil {
		t.Fatalf("session open: %v\n%s", err, stderr)
	}
	if len(f.mountReqs) != 2 {
		t.Fatalf("engine calls = %d, want 2", len(f.mountReqs))
	}
	first, second := f.mountReqs[0], f.mountReqs[1]
	if first.Full != bare || !first.ReadOnly || first.Subdir != "" {
		t.Errorf("read-only replay = %+v", first)
	}
	if second.Full != bare || second.ReadOnly || second.Subdir != "src/lib" {
		t.Errorf("subdir replay = %+v", second)
	}
	if first.MergedDir != roView || second.MergedDir != subView {
		t.Errorf("replay order or targets wrong: %+v %+v", first, second)
	}
}

func TestSessionOpenReportsUnavailableEnvironment(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	bare := filepath.Join(work, "bare.sqsh")
	writeTestFile(t, bare, "bytes")
	view := filepath.Join(work, "view")
	writeSessionRecord(t, sessionsDir(home), "dev", session.Action{
		Type: session.TypeMount, Image: bare,
		Mountpoint: view, Mode: session.ModeRW,
	})

	f.mountErr = fmt.Errorf("%w: /dev/fuse is missing", workspace.ErrUnavailable)
	_, _, _, err := runCmd(t, append(lib, "session", "open", "dev")...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "tiler doctor") {
		t.Fatalf("unavailable: ExitCode(%v) = %d: %v", err, ExitCode(err), err)
	}
	// The failed replay is written back, so a later open retries it.
	rec := loadSessionRecord(t, sessionsDir(home), "dev")
	if rec.Actions[0].Status != session.StatusFailed {
		t.Fatalf("action = %+v", rec.Actions[0])
	}
}

func TestSessionCloseUnmountsInReverseOrder(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	view1 := filepath.Join(work, "view1")
	view2 := filepath.Join(work, "view2")
	writeSessionRecord(t, sessionsDir(home), "dev",
		libraryAction("alice/app", view1, session.ModeRW),
		libraryAction("alice/app", view2, session.ModeRW))

	stdout, stderr, _, err := runCmd(t, append(lib, "session", "close", "dev")...)
	if err != nil {
		t.Fatalf("session close: %v\n%s", err, stderr)
	}
	if stdout != "closed dev\n" {
		t.Fatalf("close stdout = %q", stdout)
	}
	if len(f.umountReqs) != 2 {
		t.Fatalf("umount calls = %d, want 2", len(f.umountReqs))
	}
	if f.umountReqs[0].Dir != view2 || f.umountReqs[1].Dir != view1 {
		t.Fatalf("umount order = %s then %s", f.umountReqs[0].Dir, f.umountReqs[1].Dir)
	}
	rec := loadSessionRecord(t, sessionsDir(home), "dev")
	if rec.State != session.StateClosed || rec.ClosedAt.IsZero() {
		t.Fatalf("record = %+v", rec)
	}
	for _, action := range rec.Actions {
		if action.Status != session.StatusUnmounted {
			t.Errorf("action = %+v", action)
		}
	}
}

func TestSessionShowRendersActionLog(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	view := filepath.Join(work, "view")
	action := libraryAction("alice/app", view, session.ModeRW)
	action.Subdir = "src"
	writeSessionRecord(t, sessionsDir(home), "dev", action)

	stdout, stderr, _, err := runCmd(t, append(lib, "session", "show", "dev")...)
	if err != nil {
		t.Fatalf("session show: %v\n%s", err, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("show stdout = %q", stdout)
	}
	if !strings.HasPrefix(lines[0], "dev active created=2026-10-17T03:15:00Z") {
		t.Errorf("header = %q", lines[0])
	}
	want := fmt.Sprintf("  1 mount alice/app %s mode=rw status=mounted "+
		"at=2026-10-17T03:15:00Z subdir=src", view)
	if lines[1] != want {
		t.Errorf("action line = %q, want %q", lines[1], want)
	}
}

func TestSessionGroupWithoutSubcommandShowsHelp(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	stdout, stderr, _, err := runCmd(t, append(lib, "session")...)
	if err != nil {
		t.Fatalf("session: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout+stderr, "Usage:") {
		t.Fatalf("no help printed: %q %q", stdout, stderr)
	}
}

// loadSessionRecord reads one record for assertions.
func loadSessionRecord(t *testing.T, dir, name string) session.Record {
	t.Helper()
	rec, err := session.Store{Dir: dir}.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}
