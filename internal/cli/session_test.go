package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	writeSessionRecordMarked(t, dir, name, false, actions...)
}

// writeRemovableSessionRecord seeds one active --rm session record.
func writeRemovableSessionRecord(t *testing.T, dir, name string, actions ...session.Action) {
	t.Helper()
	writeSessionRecordMarked(t, dir, name, true, actions...)
}

func writeSessionRecordMarked(t *testing.T, dir, name string, removable bool, actions ...session.Action) {
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
	versioned := true
	for _, action := range actions {
		if len(action.TipID) != 64 || (action.Name != "" && len(action.FullID) != 64) ||
			(action.ServedID != "" && (len(action.ServedID) != 64 || len(action.ServedTip) != 64)) {
			versioned = false
		}
	}
	rec := session.Record{Name: name, State: session.StateActive,
		Actions: actions, CreatedAt: now, RemoveOnClose: removable}
	var err error
	if versioned {
		for i := range actions {
			actions[i].RuntimeID = fmt.Sprintf("m%d", i+1)
		}
		rec, err = session.NewVersionRecord(name, now)
		if err == nil {
			rec.Actions, rec.RemoveOnClose = actions, removable
			err = store.SaveVersion(rec)
		}
	} else {
		err = store.Save(rec)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// libraryAction is one recorded library mount action. FullID stays
// empty so fixtures without a real pin keep name-only ResolveMount.
func libraryAction(imageRef, mountpoint, mode string) session.Action {
	return session.Action{
		Type:       session.TypeMount,
		Image:      imageRef,
		Name:       "alice/app",
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
	if !strings.Contains(stdout, "SESSION") || !strings.Contains(stdout, "dev") ||
		!strings.Contains(stdout, "active") {
		t.Fatalf("list stdout = %q", stdout)
	}

	stdout, stderr, _, err = runCmd(t, append(lib, "session", "show", "dev")...)
	if err != nil {
		t.Fatalf("session show: %v\n%s", err, stderr)
	}
	if !strings.HasPrefix(stdout, "dev · active · missing-mounts\n") {
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
	if !strings.Contains(stdout, "dev") || !strings.Contains(stdout, "closed") {
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

func TestSessionReadCommandsRejectLegacyRecords(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	store := session.Store{Dir: sessionsDir(home)}
	if err := store.Save(session.Record{
		Name: "legacy", State: session.StateActive, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"session", "list"}, {"session", "show", "legacy"}, {"session", "show", "legacy", "--json"}, {"status", "-s", "legacy"}} {
		stdout, _, _, err := runCmd(t, append(lib, args...)...)
		if ExitCode(err) != 1 || !strings.Contains(err.Error(), "session: parse") ||
			!strings.Contains(err.Error(), "unsupported record version 0") ||
			!strings.Contains(err.Error(), "rebuild sessions") {
			t.Errorf("%v: stdout = %q, error = %v", args, stdout, err)
		}
	}
}

func mustClose(t *testing.T, lib []string, name string) (string, string, error) {
	t.Helper()
	stdout, stderr, _, err := runCmd(t, append(lib, "session", "close", name)...)
	return stdout, stderr, err
}

func TestSessionNewGeneratesName(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)

	stdout, stderr, _, err := runCmd(t, append(lib, "session", "new")...)
	if err != nil {
		t.Fatalf("session new: %v\n%s", err, stderr)
	}
	name, ok := strings.CutPrefix(stdout, "created ")
	if !ok || !strings.HasSuffix(name, "\n") {
		t.Fatalf("new stdout = %q, want created <name>", stdout)
	}
	name = strings.TrimSuffix(name, "\n")
	if !generatedSessionName.MatchString(name) {
		t.Fatalf("generated name %q has unexpected shape", name)
	}
	rec := loadSessionRecord(t, sessionsDir(home), name)
	if rec.Name != name || rec.State != session.StateActive || rec.RemoveOnClose {
		t.Fatalf("record = %+v", rec)
	}

	// --rm without a name also generates and marks the record.
	stdout, stderr, _, err = runCmd(t, append(lib, "session", "new", "--rm")...)
	if err != nil {
		t.Fatalf("session new --rm: %v\n%s", err, stderr)
	}
	rmName, ok := strings.CutPrefix(stdout, "created ")
	if !ok || !strings.HasSuffix(rmName, "\n") {
		t.Fatalf("--rm stdout = %q", stdout)
	}
	rmName = strings.TrimSuffix(rmName, "\n")
	if !generatedSessionName.MatchString(rmName) || rmName == name {
		t.Fatalf("generated --rm name %q (earlier %q)", rmName, name)
	}
	if rec := loadSessionRecord(t, sessionsDir(home), rmName); !rec.RemoveOnClose {
		t.Fatalf("generated --rm record not marked: %+v", rec)
	}
}

func TestSessionUsageErrors(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	tests := []struct {
		name string
		args []string
	}{
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

// pinPortableHealth installs a health seam for platforms whose real
// probe always reports unavailable. A point is healthy when the fake
// table lists it and the directory still opens, which is the
// listed-and-readable half of the Linux classification. Linux keeps
// the real probe.
func pinPortableHealth(t *testing.T, f *mountFake) {
	t.Helper()
	if runtime.GOOS == "linux" {
		return
	}
	withSessionSeams(t, func(engine *session.Engine) {
		engine.Health = func(point string) bool {
			return mountListed(f.table, point) && dirAnswers(point)
		}
	})
}

func mountListed(table []mountinfo.Mount, point string) bool {
	for _, m := range table {
		if m.Point == point {
			return true
		}
	}
	return false
}

func dirAnswers(point string) bool {
	file, err := os.Open(point)
	if err != nil {
		return false
	}
	_ = file.Close()
	return true
}

func TestSessionOpenResolvesFileImagesAndModes(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	bare := filepath.Join(work, "bare.sqsh")
	writeSquashFile(t, bare)
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
	writeSquashFile(t, bare)
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

func TestSessionShowRendersLayeredMounts(t *testing.T) {
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
	for _, want := range []string{
		"dev · active · missing-mounts\n",
		"Created:",
		"📁 alice/app:",
		compactStatusPath(view, work),
		"[rw · mounted · stale]",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("show missing %q:\n%s", want, stdout)
		}
	}
}

func TestSessionNewRemovableClosesRemovesAndPurges(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	var purged []string
	withSessionSeams(t, func(engine *session.Engine) {
		engine.RemoveRuntime = func(point string) error {
			purged = append(purged, point)
			return nil
		}
	})
	img := filepath.Join(work, "x.sqsh")
	writeSquashFile(t, img)
	view := filepath.Join(work, "view")
	sessions := sessionsDir(home)

	// A --rm session is created marked, and show flags it.
	stdout, stderr, _, err := runCmd(t, append(lib, "session", "new", "--rm", "dev")...)
	if err != nil {
		t.Fatalf("session new --rm: %v\n%s", err, stderr)
	}
	if stdout != "created dev\n" {
		t.Fatalf("new stdout = %q", stdout)
	}
	if rec := loadSessionRecord(t, sessions, "dev"); !rec.RemoveOnClose {
		t.Fatalf("record not marked --rm: %+v", rec)
	}
	stdout, stderr, _, err = runCmd(t, append(lib, "session", "show", "dev")...)
	if err != nil {
		t.Fatalf("session show: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "dev · active · remove on close · missing-mounts\n") {
		t.Fatalf("show stdout = %q", stdout)
	}

	// Mounting into the named --rm session records a live view.
	mountOK(t, lib, "mounted "+view+"\n", "mount", "--session", "dev", img, view)

	// Closing it deletes the record, purges the runtime of the view,
	// and reports "removed" rather than the audit-keeping "closed".
	stdout, stderr, _, err = runCmd(t, append(lib, "session", "close", "dev")...)
	if err != nil {
		t.Fatalf("session close: %v\n%s", err, stderr)
	}
	if stdout != "removed dev\n" {
		t.Fatalf("close stdout = %q", stdout)
	}
	if len(purged) != 1 || purged[0] != view {
		t.Fatalf("purged = %v, want [%s]", purged, view)
	}
	if names := listSessionNames(t, sessions); len(names) != 0 {
		t.Fatalf("sessions after --rm close = %v, want none", names)
	}
}

func TestSessionGroupWithoutSubcommandListsSessions(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	stdout, stderr, _, err := runCmd(t, append(lib, "session")...)
	if err != nil {
		t.Fatalf("session: %v\n%s", err, stderr)
	}
	if stdout != "💡 Use 'ls' to hide this hint; use '-h' to see more subcommands.\n\n" || stderr != "" {
		t.Fatalf("unexpected empty session listing: %q %q", stdout, stderr)
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
