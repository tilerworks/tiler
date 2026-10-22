package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
	"github.com/jiangxin/tiler/internal/workspace"
)

func TestResolveCombinationRuntimeFromTable(t *testing.T) {
	home, work := withWorkspace(t)
	view := filepath.Join(work, "view")
	runtime := filepath.Join(sessionsDir(home), "dev", "runtime", "m1")
	cfg := config.Config{SessionsDir: sessionsDir(home)}
	got := resolveCombinationRuntime(cfg, view, []workspace.Entry{{
		Point:  view,
		Upper:  filepath.Join(runtime, "upper"),
		Lowers: []string{filepath.Join(runtime, "lower-0")},
	}})
	if got != runtime {
		t.Fatalf("runtime = %q, want %q", got, runtime)
	}
}

func TestResolveCombinationRuntimeFromSession(t *testing.T) {
	home, work := withWorkspace(t)
	view := filepath.Join(work, "view")
	withMountSeams(t, nil)
	writeSessionRecord(t, sessionsDir(home), "dev",
		libraryAction("alice/app", view, session.ModeRW))
	cfg := config.Config{SessionsDir: sessionsDir(home)}
	want := filepath.Join(sessionsDir(home), "dev", "runtime", "m1")
	got := resolveCombinationRuntime(cfg, view, nil)
	if got != want {
		t.Fatalf("runtime = %q, want %q", got, want)
	}
}

func TestResolveCombinationRuntimeStoreFallbackWhenLocked(t *testing.T) {
	home, work := withWorkspace(t)
	view := filepath.Join(work, "view")
	writeSessionRecord(t, sessionsDir(home), "dev",
		libraryAction("alice/app", view, session.ModeRW))
	cfg := config.Config{SessionsDir: sessionsDir(home)}
	withMountSeams(t, nil)
	// Simulate close holding the advisory lock: SessionForPath's
	// non-blocking scan skips the record, so the unlocked store
	// fallback must still recover the combination directory.
	withSessionSeams(t, func(engine *session.Engine) {
		engine.Lock = func(string, time.Duration) (func(), error) {
			return nil, session.ErrLockHeld
		}
	})
	want := filepath.Join(sessionsDir(home), "dev", "runtime", "m1")
	got := resolveCombinationRuntime(cfg, view, nil)
	if got != want {
		t.Fatalf("runtime = %q, want %q", got, want)
	}
}

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
	// The fixture table has no upper/lower, so the combination
	// directory cannot be recovered and the residue sweep is skipped.
	if req.Dir != view || req.RuntimeDir != "" {
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
	other := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "lower-0")
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

func TestUmountRemovesRemovableSessionAfterLastView(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	view := filepath.Join(work, "view")
	f := withMountSeams(t, nil)
	f.removed = 1
	const name = "happy-fox"
	writeRemovableSessionRecord(t, sessionsDir(home), name,
		libraryAction("alice/app", view, session.ModeRW))

	stdout, stderr, _, err := runCmd(t, append(lib, "umount", view)...)
	if err != nil {
		t.Fatalf("umount: %v\n%s", err, stderr)
	}
	if stdout != "unmounted "+view+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(sessionsDir(home), name)); !os.IsNotExist(err) {
		t.Fatalf("removable session dir survived: %v", err)
	}
}

func TestUmountKeepsNonRemovableSessionAfterLastView(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	view := filepath.Join(work, "view")
	f := withMountSeams(t, nil)
	f.removed = 1
	const name = "happy-fox"
	writeSessionRecord(t, sessionsDir(home), name,
		libraryAction("alice/app", view, session.ModeRW))

	if _, stderr, _, err := runCmd(t, append(lib, "umount", view)...); err != nil {
		t.Fatalf("umount: %v\n%s", err, stderr)
	}
	rec := loadSessionRecord(t, sessionsDir(home), name)
	if rec.State != session.StateActive {
		t.Fatalf("non--rm record = %+v, want active", rec)
	}
}

func TestUmountKeepsNamedSessionAndLiveRemovableViewsActive(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	view := filepath.Join(work, "view")
	other := filepath.Join(work, "other")
	f := withMountSeams(t, nil)
	f.removed = 1
	writeSessionRecord(t, sessionsDir(home), "dev",
		libraryAction("alice/app", view, session.ModeRW))
	const removable = "happy-fox"
	writeRemovableSessionRecord(t, sessionsDir(home), removable,
		libraryAction("alice/app", view, session.ModeRW),
		libraryAction("alice/app", other, session.ModeRW))
	// The second view of the --rm session still serves, so
	// neither record may auto-close.
	withSessionSeams(t, func(engine *session.Engine) {
		engine.Health = func(point string) bool { return point == other }
	})

	if _, stderr, _, err := runCmd(t, append(lib, "umount", view)...); err != nil {
		t.Fatalf("umount: %v\n%s", err, stderr)
	}
	rec := loadSessionRecord(t, sessionsDir(home), "dev")
	if rec.State != session.StateActive {
		t.Errorf("named record = %+v", rec)
	}
	rec = loadSessionRecord(t, sessionsDir(home), removable)
	if rec.State != session.StateActive {
		t.Errorf("--rm record with a live view = %+v", rec)
	}
}

func TestUmountDiskClosesRemovableSession(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	view := filepath.Join(work, "view")
	if err := os.Mkdir(view, 0o755); err != nil {
		t.Fatal(err)
	}
	sessions := sessionsDir(home)
	shadow := filepath.Join(sessions, "dev", "runtime", "m1", "upper.shadow")
	if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("shadow-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRemovableSessionRecord(t, sessions, "dev",
		libraryAction("alice/app", view, session.ModeRW))
	volumes := []statusVolume{{
		image:  "/images/alice/app/image.dmg",
		shadow: shadow,
		point:  view,
	}}
	installDiskUmount(t, &volumes, nil)

	stdout, stderr, _, err := runCmd(t, append(lib, "umount", view)...)
	if err != nil {
		t.Fatalf("umount: %v\n%s", err, stderr)
	}
	if stdout != "unmounted "+view+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if strings.Contains(stderr, "mount table") || strings.Contains(stderr, "removable session close") {
		t.Fatalf("stderr = %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(sessions, "dev")); !os.IsNotExist(err) {
		t.Fatalf("--rm session dir survived: %v", err)
	}
	if _, err := os.Stat(shadow); !os.IsNotExist(err) {
		t.Fatalf("--rm left the shadow: %v", err)
	}
}

func TestUmountDiskDetachFailureLeavesSessionMounted(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	view := filepath.Join(work, "view")
	if err := os.Mkdir(view, 0o755); err != nil {
		t.Fatal(err)
	}
	sessions := sessionsDir(home)
	shadow := filepath.Join(sessions, "dev", "runtime", "m1", "upper.shadow")
	if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("shadow-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRemovableSessionRecord(t, sessions, "dev",
		libraryAction("alice/app", view, session.ModeRW))
	volumes := []statusVolume{{
		image:  "/images/alice/app/image.dmg",
		shadow: shadow,
		point:  view,
	}}
	installDiskUmount(t, &volumes, errors.New("hdiutil: detach failed"))

	_, stderr, _, err := runCmd(t, append(lib, "umount", view)...)
	if err == nil || !strings.Contains(err.Error()+"\n"+stderr, "detach failed") {
		t.Fatalf("err = %v\n%s", err, stderr)
	}
	if strings.Contains(stderr, "mount table") || strings.Contains(err.Error(), "mount table") {
		t.Fatalf("detach failure blamed the mount table: %v\n%s", err, stderr)
	}
	rec := loadSessionRecord(t, sessions, "dev")
	if rec.State != session.StateActive || !rec.RemoveOnClose {
		t.Fatalf("record = %+v, want active --rm", rec)
	}
	if len(rec.Actions) != 1 || rec.Actions[0].Status != session.StatusMounted {
		t.Fatalf("detach failure marked the view unmounted: %+v", rec.Actions)
	}
	if body, err := os.ReadFile(shadow); err != nil || string(body) != "shadow-bytes" {
		t.Fatalf("shadow = %q, %v", body, err)
	}
}

func TestUmountDiskKeepsRemovableSessionWhileAnotherVolumeIsAttached(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	view := filepath.Join(work, "view")
	other := filepath.Join(work, "other")
	if err := os.Mkdir(view, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	sessions := sessionsDir(home)
	shadow := filepath.Join(sessions, "dev", "runtime", "m1", "upper.shadow")
	otherShadow := filepath.Join(sessions, "dev", "runtime", "m2", "upper.shadow")
	for _, path := range []string{shadow, otherShadow} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("shadow-bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeRemovableSessionRecord(t, sessions, "dev",
		libraryAction("alice/app", view, session.ModeRW),
		libraryAction("alice/app", other, session.ModeRW))
	volumes := []statusVolume{
		{image: "/images/alice/app/image.dmg", shadow: shadow, point: view},
		{image: "/images/alice/app/image.dmg", shadow: otherShadow, point: other},
	}
	installDiskUmount(t, &volumes, nil)

	stdout, stderr, _, err := runCmd(t, append(lib, "umount", view)...)
	if err != nil {
		t.Fatalf("umount: %v\n%s", err, stderr)
	}
	if stdout != "unmounted "+view+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if strings.Contains(stderr, "mount table") || strings.Contains(stderr, "removable session close") {
		t.Fatalf("stderr = %q", stderr)
	}
	rec := loadSessionRecord(t, sessions, "dev")
	if rec.State != session.StateActive || !rec.RemoveOnClose {
		t.Fatalf("record = %+v, want active --rm", rec)
	}
	if status := mountStatus(rec, view); status != session.StatusUnmounted {
		t.Fatalf("detached view status = %q", status)
	}
	if status := mountStatus(rec, other); status != session.StatusMounted {
		t.Fatalf("live view status = %q", status)
	}
	if _, err := os.Stat(otherShadow); err != nil {
		t.Fatalf("live shadow: %v", err)
	}
}

func mountStatus(rec session.Record, point string) string {
	for _, action := range rec.Actions {
		if action.Type == session.TypeMount && action.Mountpoint == point {
			return action.Status
		}
	}
	return ""
}

// installDiskUmount makes umount take the hdiutil path. detachErr
// fails the detach and leaves the listing unchanged. A nil error
// drops the detached point from later info snapshots, which is what
// hdiutil info reports after a successful detach.
func installDiskUmount(t *testing.T, volumes *[]statusVolume, detachErr error) {
	t.Helper()
	restoreDiskSeams(t)
	loadMountTable = func() ([]mountinfo.Mount, error) {
		return nil, mountinfo.ErrUnavailable
	}
	readHdiutilInfo = func() ([]byte, error) {
		return hdiutilStatusPlist(*volumes), nil
	}
	runHdiutil = func(argv []string, _ error) ([]byte, error) {
		if len(argv) > 1 && argv[1] == "detach" {
			if detachErr != nil {
				return nil, detachErr
			}
			point := argv[len(argv)-1]
			next := make([]statusVolume, 0, len(*volumes))
			for _, vol := range *volumes {
				if vol.point == point {
					continue
				}
				next = append(next, vol)
			}
			*volumes = next
			return nil, nil
		}
		if len(argv) > 1 && argv[1] == "info" {
			return hdiutilStatusPlist(*volumes), nil
		}
		return nil, nil
	}
	withSessionSeams(t, func(engine *session.Engine) {
		engine.LoadDiskImages = func() ([]session.DiskImage, error) {
			return diskImagesFromInfo(readHdiutilInfo)
		}
	})
}
