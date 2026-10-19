package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/session"
	"github.com/jiangxin/tiler/internal/workspace"
)

// listSessionNames lists the recorded session names in a directory.
func listSessionNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if name, ok := strings.CutSuffix(entry.Name(), ".json"); ok {
			names = append(names, name)
		}
	}
	return names
}

// mustSessionNew creates one session through the command surface.
func mustSessionNew(t *testing.T, lib []string, name string) {
	t.Helper()
	stdout, stderr, _, err := runCmd(t, append(lib, "session", "new", name)...)
	if err != nil {
		t.Fatalf("session new %s: %v\n%s", name, err, stderr)
	}
	if stdout != "created "+name+"\n" {
		t.Fatalf("session new stdout = %q", stdout)
	}
}

// mountOK runs one mount command and checks the printed line.
func mountOK(t *testing.T, lib []string, want string, args ...string) {
	t.Helper()
	stdout, stderr, _, err := runCmd(t, append(lib, args...)...)
	if err != nil {
		t.Fatalf("mount %v: %v\n%s", args, err, stderr)
	}
	if stdout != want {
		t.Fatalf("mount stdout = %q, want %q", stdout, want)
	}
}

func TestMountRecordsActionInFlagSession(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	buildLibrary(t, home, lib, imagesDir, "alice/app")
	withMountSeams(t, nil)
	mustSessionNew(t, lib, "dev")
	view := filepath.Join(work, "view")

	// The library id the action records is the one the ls command
	// reports for the active image.
	stdout, stderr, _, err := runCmd(t, append(lib, "images", "ls", "alice/app")...)
	if err != nil {
		t.Fatalf("images ls: %v\n%s", err, stderr)
	}
	fullID := strings.Fields(stdout)[1]

	mountOK(t, lib, "mounted "+view+"\n",
		"mount", "--session", "dev", "alice/app", view)

	rec := loadSessionRecord(t, sessionsDir(home), "dev")
	if len(rec.Actions) != 1 {
		t.Fatalf("actions = %+v", rec.Actions)
	}
	action := rec.Actions[0]
	if action.Type != session.TypeMount || action.Image != "alice/app" {
		t.Errorf("action = %+v", action)
	}
	if action.Owner != "alice" || action.Repo != "app" || action.FullID != fullID {
		t.Errorf("identity = %+v, want full id %s", action, fullID)
	}
	if action.Mountpoint != view || action.Mode != session.ModeRW {
		t.Errorf("action = %+v", action)
	}
	if action.Status != session.StatusMounted || action.Seq != 1 {
		t.Errorf("action = %+v", action)
	}
	if action.Timestamp.IsZero() {
		t.Error("the action carries no timestamp")
	}

	// A read-only subdir mount records its shape.
	roView := filepath.Join(work, "ro-view")
	mustSessionNew(t, lib, "reader")
	mountOK(t, lib, "mounted "+roView+"\n", "mount", "--session", "reader",
		"--read-only", "--subdir", "src", "alice/app", roView)
	rec = loadSessionRecord(t, sessionsDir(home), "reader")
	action = rec.Actions[0]
	if action.Mode != session.ModeRO || action.Subdir != "src" {
		t.Errorf("action = %+v", action)
	}
}

func TestMountSessionResolutionPriority(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	img := filepath.Join(work, "x.sqsh")
	writeTestFile(t, img, "bytes")
	mustSessionNew(t, lib, "flag-sess")
	mustSessionNew(t, lib, "env-sess")
	sessions := sessionsDir(home)

	// The flag beats the environment.
	t.Setenv(sessionEnvVar, "env-sess")
	view := filepath.Join(work, "view1")
	mountOK(t, lib, "mounted "+view+"\n",
		"mount", "--session", "flag-sess", img, view)
	if rec := loadSessionRecord(t, sessions, "flag-sess"); len(rec.Actions) != 1 {
		t.Errorf("flag session actions = %+v", rec.Actions)
	}
	if rec := loadSessionRecord(t, sessions, "env-sess"); len(rec.Actions) != 0 {
		t.Errorf("env session actions = %+v", rec.Actions)
	}

	// The environment wins over implicit creation.
	view = filepath.Join(work, "view2")
	mountOK(t, lib, "mounted "+view+"\n", "mount", img, view)
	rec := loadSessionRecord(t, sessions, "env-sess")
	if len(rec.Actions) != 1 || rec.Actions[0].Mountpoint != view {
		t.Errorf("env session actions = %+v", rec.Actions)
	}

	// Without any context an implicit auto-* session is created.
	t.Setenv(sessionEnvVar, "")
	view = filepath.Join(work, "view3")
	mountOK(t, lib, "mounted "+view+"\n", "mount", img, view)
	names := listSessionNames(t, sessions)
	if len(names) != 3 {
		t.Fatalf("sessions = %v", names)
	}
	var auto string
	for _, name := range names {
		if strings.HasPrefix(name, "auto-") {
			auto = name
		}
	}
	if auto == "" {
		t.Fatalf("no implicit session in %v", names)
	}
	if rec := loadSessionRecord(t, sessions, auto); len(rec.Actions) != 1 ||
		rec.Actions[0].Mountpoint != view {
		t.Errorf("implicit session actions = %+v", rec.Actions)
	}
}

func TestMountImplicitSessionIsIsomorphic(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	img := filepath.Join(work, "x.sqsh")
	writeTestFile(t, img, "bytes")
	view := filepath.Join(work, "view")
	mountOK(t, lib, "mounted "+view+"\n", "mount", img, view)

	names := listSessionNames(t, sessionsDir(home))
	if len(names) != 1 || !strings.HasPrefix(names[0], "auto-") {
		t.Fatalf("sessions = %v, want one auto-* record", names)
	}
	auto := names[0]

	// The same command surface operates on the implicit session.
	stdout, stderr, _, err := runCmd(t, append(lib, "session", "show", auto)...)
	if err != nil {
		t.Fatalf("session show: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, auto+" active") || !strings.Contains(stdout, "mount ") {
		t.Fatalf("show stdout = %q", stdout)
	}
	stdout, stderr, _, err = runCmd(t, append(lib, "session", "close", auto)...)
	if err != nil {
		t.Fatalf("session close: %v\n%s", err, stderr)
	}
	if stdout != "closed "+auto+"\n" {
		t.Fatalf("close stdout = %q", stdout)
	}
	stdout, stderr, _, err = runCmd(t, append(lib, "session", "list")...)
	if err != nil {
		t.Fatalf("session list: %v\n%s", err, stderr)
	}
	if stdout != auto+" closed mounts=0\n" {
		t.Fatalf("list stdout = %q", stdout)
	}
	stdout, stderr, _, err = runCmd(t, append(lib, "session", "remove", auto)...)
	if err != nil {
		t.Fatalf("session remove: %v\n%s", err, stderr)
	}
	if stdout != "removed "+auto+"\n" {
		t.Fatalf("remove stdout = %q", stdout)
	}
	if names := listSessionNames(t, sessionsDir(home)); len(names) != 0 {
		t.Fatalf("sessions after remove = %v", names)
	}
}

func TestMountWriteAheadOrdering(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	img := filepath.Join(work, "x.sqsh")
	writeTestFile(t, img, "bytes")
	view := filepath.Join(work, "view")
	sessions := sessionsDir(home)
	mustSessionNew(t, lib, "dev")

	checked := false
	mountEngine = func(req workspace.Request) (*workspace.Workspace, error) {
		// While the engine runs, the pending action is on disk.
		rec := loadSessionRecord(t, sessions, "dev")
		if len(rec.Actions) != 1 {
			t.Errorf("disk during mount = %+v", rec.Actions)
		} else if rec.Actions[0].Status != session.StatusPending {
			t.Errorf("status during mount = %q", rec.Actions[0].Status)
		} else if rec.Actions[0].Mountpoint != view {
			t.Errorf("mountpoint during mount = %q", rec.Actions[0].Mountpoint)
		}
		checked = true
		return f.mount(req)
	}
	mountOK(t, lib, "mounted "+view+"\n", "mount", "--session", "dev", img, view)
	if !checked {
		t.Fatal("the engine never ran")
	}
	// After the run, the outcome is written back.
	rec := loadSessionRecord(t, sessions, "dev")
	if rec.Actions[0].Status != session.StatusMounted {
		t.Fatalf("action = %+v", rec.Actions[0])
	}
}

func TestMountFilePathImageRecordsEmptyIdentity(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	img := filepath.Join(work, "bare.sqsh")
	writeTestFile(t, img, "bytes")
	view := filepath.Join(work, "view")
	mustSessionNew(t, lib, "dev")
	mountOK(t, lib, "mounted "+view+"\n", "mount", "--session", "dev", img, view)

	action := loadSessionRecord(t, sessionsDir(home), "dev").Actions[0]
	if action.Image != img {
		t.Errorf("image = %q, want the reference as given", action.Image)
	}
	if action.Owner != "" || action.Repo != "" || action.FullID != "" {
		t.Errorf("file-path identity = %+v, want empty", action)
	}
}

func TestMountSessionErrors(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	img := filepath.Join(work, "x.sqsh")
	writeTestFile(t, img, "bytes")
	view := filepath.Join(work, "view")

	// A bad flag value is a usage problem.
	_, _, _, err := runCmd(t, append(lib, "mount", "--session", "a/b", img, view)...)
	if ExitCode(err) != 2 {
		t.Fatalf("bad flag: ExitCode(%v) = %d, want 2", err, ExitCode(err))
	}
	// A missing named session is a runtime error.
	_, _, _, err = runCmd(t, append(lib, "mount", "--session", "absent", img, view)...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "session not found") {
		t.Fatalf("missing session: ExitCode(%v) = %d: %v", err, ExitCode(err), err)
	}
	// A bad environment value names the variable and stays runtime.
	t.Setenv(sessionEnvVar, "with space")
	_, _, _, err = runCmd(t, append(lib, "mount", img, view)...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), sessionEnvVar) {
		t.Fatalf("bad env: ExitCode(%v) = %d: %v", err, ExitCode(err), err)
	}
	// A missing environment session is a runtime error too.
	t.Setenv(sessionEnvVar, "absent")
	_, _, _, err = runCmd(t, append(lib, "mount", img, view)...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "session not found") {
		t.Fatalf("missing env session: ExitCode(%v) = %d: %v", err, ExitCode(err), err)
	}
}

func TestMountFailureRecordsFailedAction(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	img := filepath.Join(work, "x.sqsh")
	writeTestFile(t, img, "bytes")
	view := filepath.Join(work, "view")
	mustSessionNew(t, lib, "dev")

	f.mountErr = errors.New("workspace: view already mounts a different view")
	_, _, _, err := runCmd(t, append(lib, "mount", "--session", "dev", img, view)...)
	if ExitCode(err) != 1 {
		t.Fatalf("conflict: ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
	// The write-ahead record survives with the failed outcome, so
	// a later open can replay it.
	action := loadSessionRecord(t, sessionsDir(home), "dev").Actions[0]
	if action.Status != session.StatusFailed {
		t.Fatalf("action = %+v", action)
	}

	f.mountErr = fmt.Errorf("%w: squashfuse_ll is missing", workspace.ErrUnavailable)
	_, _, _, err = runCmd(t, append(lib, "mount", "--session", "dev", img, view)...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "tiler doctor") {
		t.Fatalf("unavailable: ExitCode(%v) = %d: %v", err, ExitCode(err), err)
	}
	if rec := loadSessionRecord(t, sessionsDir(home), "dev"); len(rec.Actions) != 2 {
		t.Fatalf("actions = %+v", rec.Actions)
	}
}

func TestMountReuseKeepsOutputAndRecords(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	f.reused = true
	img := filepath.Join(work, "x.sqsh")
	writeTestFile(t, img, "bytes")
	view := filepath.Join(work, "view")
	mustSessionNew(t, lib, "dev")
	mountOK(t, lib, "already mounted "+view+"\n", "mount", "--session", "dev", img, view)

	action := loadSessionRecord(t, sessionsDir(home), "dev").Actions[0]
	if action.Status != session.StatusMounted {
		t.Fatalf("action = %+v", action)
	}
}

func TestMountImplicitSessionDiscardsFailedAndReusedRecords(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	img := filepath.Join(work, "x.sqsh")
	writeTestFile(t, img, "bytes")
	view := filepath.Join(work, "view")
	sessions := sessionsDir(home)

	// A failed implicit mount deletes its record instead of leaving
	// an active name that pins the image.
	f.mountErr = errors.New("workspace: refusing to restack")
	_, _, _, err := runCmd(t, append(lib, "mount", img, view)...)
	if ExitCode(err) != 1 {
		t.Fatalf("failed mount: ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
	if names := listSessionNames(t, sessions); len(names) != 0 {
		t.Fatalf("sessions after failure = %v, want none", names)
	}

	// A successful implicit mount keeps its record.
	f.mountErr = nil
	mountOK(t, lib, "mounted "+view+"\n", "mount", img, view)
	names := listSessionNames(t, sessions)
	if len(names) != 1 || !strings.HasPrefix(names[0], "auto-") {
		t.Fatalf("sessions = %v, want one auto-* record", names)
	}

	// An implicit remount that merely adopts the healthy view leaves
	// no second record behind.
	f.reused = true
	mountOK(t, lib, "already mounted "+view+"\n", "mount", img, view)
	if names := listSessionNames(t, sessions); len(names) != 1 {
		t.Fatalf("sessions after reuse = %v, want the original record only", names)
	}
	rec := loadSessionRecord(t, sessions, names[0])
	if len(rec.Actions) != 1 || rec.Actions[0].Status != session.StatusMounted {
		t.Fatalf("original record actions = %+v", rec.Actions)
	}
}

func TestMountIntoClosedSessionIsRefused(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	img := filepath.Join(work, "x.sqsh")
	writeTestFile(t, img, "bytes")
	view := filepath.Join(work, "view")
	mustSessionNew(t, lib, "dev")
	if _, stderr, _, err := runCmd(t, append(lib, "session", "close", "dev")...); err != nil {
		t.Fatalf("session close: %v\n%s", err, stderr)
	}
	_, _, _, err := runCmd(t, append(lib, "mount", "--session", "dev", img, view)...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "create a new session") {
		t.Fatalf("closed session: ExitCode(%v) = %d: %v", err, ExitCode(err), err)
	}
}

func TestMountRemovableMarksImplicitSession(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	img := filepath.Join(work, "x.sqsh")
	writeTestFile(t, img, "bytes")
	view := filepath.Join(work, "view")
	sessions := sessionsDir(home)

	// --rm on an implicit mount marks the auto-* record it creates.
	mountOK(t, lib, "mounted "+view+"\n", "mount", "--rm", img, view)
	names := listSessionNames(t, sessions)
	if len(names) != 1 || !strings.HasPrefix(names[0], "auto-") {
		t.Fatalf("sessions = %v, want one auto-* record", names)
	}
	if rec := loadSessionRecord(t, sessions, names[0]); !rec.RemoveOnClose {
		t.Fatalf("implicit --rm record not marked: %+v", rec)
	}
	// show flags the implicit --rm session with the rm marker.
	stdout, stderr, _, err := runCmd(t, append(lib, "session", "show", names[0])...)
	if err != nil {
		t.Fatalf("session show: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, names[0]+" active rm") {
		t.Fatalf("show stdout = %q", stdout)
	}
}

func TestMountRemovableRejectsNamedSession(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	img := filepath.Join(work, "x.sqsh")
	writeTestFile(t, img, "bytes")
	view := filepath.Join(work, "view")
	mustSessionNew(t, lib, "dev")

	// --rm cannot target a session named by --session; the usage
	// error points at the named-session route instead.
	_, _, _, err := runCmd(t, append(lib, "mount", "--rm", "--session", "dev", img, view)...)
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "session new --rm") {
		t.Fatalf("--session: ExitCode(%v) = %d: %v", err, ExitCode(err), err)
	}
	// Nor one named by the environment variable.
	t.Setenv(sessionEnvVar, "dev")
	_, _, _, err = runCmd(t, append(lib, "mount", "--rm", img, view)...)
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), sessionEnvVar) {
		t.Fatalf("env: ExitCode(%v) = %d: %v", err, ExitCode(err), err)
	}
	// The named session is untouched: a refused --rm mount records
	// no action and creates no implicit session.
	if rec := loadSessionRecord(t, sessionsDir(home), "dev"); len(rec.Actions) != 0 {
		t.Fatalf("a refused --rm mount recorded an action: %+v", rec.Actions)
	}
	if names := listSessionNames(t, sessionsDir(home)); len(names) != 1 {
		t.Fatalf("sessions = %v, want only the named record", names)
	}
}
