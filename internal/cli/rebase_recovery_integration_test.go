//go:build linux

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/session"
)

// rebaseFixture is one live writable view of alice/app over the
// subdir source with real work done through it: wip.txt added,
// top.txt rewritten, and sub/gone.txt deleted, whose whiteout
// landed in the upper. oldID is the full image the view serves and
// the fresh record pins as its desired and only stack.
type rebaseFixture struct {
	work         string
	lib          []string
	sessionsRoot string
	repoDir      string
	view         string
	upper        string
	oldID        string
}

// newRebaseFixture builds the image, mounts the session view, works
// through it, and verifies the whiteout the deletion left behind.
func newRebaseFixture(t *testing.T) rebaseFixture {
	t.Helper()
	requireMountTestEnv(t)
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	sessionsRoot := sessionsDir(home)
	buildFullImage(t, lib, "alice/app", subdirSource(t))

	view := filepath.Join(work, "view")
	deferForceUmount(t, sessionsRoot, view)
	mustSessionNew(t, lib, "dev")
	mountOK(t, lib, "mounted "+view+"\n",
		"mount", "--session", "dev", "alice/app", view)

	entry, ok := entryAt(realTable(t), view)
	if !ok || entry.Upper == "" {
		t.Fatalf("no writable overlay entry at %s: %+v", view, entry)
	}
	// The fresh record pins the full image alone: the desired tip
	// is the full id and no served stack exists yet.
	action := onlyMountAction(t, sessionsRoot, "dev")
	if !isPayloadID(action.FullID) {
		t.Fatalf("recorded full id = %q", action.FullID)
	}
	assertStack(t, action, action.FullID, action.FullID, "", "")

	writeTestFile(t, filepath.Join(view, "wip.txt"), "wip\n")
	writeTestFile(t, filepath.Join(view, "top.txt"), "changed\n")
	if err := os.Remove(filepath.Join(view, "sub", "gone.txt")); err != nil {
		t.Fatal(err)
	}
	assertWhiteout(t, filepath.Join(entry.Upper, "sub", "gone.txt"))

	return rebaseFixture{
		work:         work,
		lib:          lib,
		sessionsRoot: sessionsRoot,
		repoDir:      filepath.Join(imagesDir, "alice", "app"),
		view:         view,
		upper:        entry.Upper,
		oldID:        action.FullID,
	}
}

// staleView kills the overlay daemon behind the record's back and
// leaves one post-mortem entry in the dead upper that no rebuild of
// the earlier work can carry: the prune must keep it.
func (fx rebaseFixture) staleView(t *testing.T) {
	t.Helper()
	killDaemon(t, findDaemon(t, "fuse-overlayfs", fx.view))
	waitOverlayDead(t, fx.view)
	writeTestFile(t, filepath.Join(fx.upper, "post.txt"), "post\n")
}

// assertRecoveredReplay replays the session and checks the recovery:
// the view serves the rebased stack plus the post-mortem upper
// entry, the entries the new lower stack already carries are pruned
// from the upper, the deletion stayed deleted, and the record's
// served ids are cleared by the real remount.
func (fx rebaseFixture) assertRecoveredReplay(t *testing.T, wantFull, wantTip string) {
	t.Helper()
	stdout, stderr, _, err := runCmd(t, append(fx.lib, "session", "open", "dev")...)
	if err != nil {
		t.Fatalf("session open: %v\n%s", err, stderr)
	}
	if stdout != "opened dev\n" {
		t.Fatalf("open output = %q", stdout)
	}
	if strings.Contains(stderr, "warning:") {
		t.Fatalf("open warned: %q", stderr)
	}
	assertBytesFile(t, filepath.Join(fx.view, "top.txt"), "changed\n")
	assertBytesFile(t, filepath.Join(fx.view, "wip.txt"), "wip\n")
	assertBytesFile(t, filepath.Join(fx.view, "post.txt"), "post\n")
	if _, err := os.Stat(filepath.Join(fx.view, "sub", "gone.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the deleted file came back in the replayed view: %v", err)
	}

	// The prune removed the entries the new lower stack already
	// serves — the rewrite, the addition, and the stale whiteout —
	// and kept the foreign post-mortem entry.
	assertPruned(t, filepath.Join(fx.upper, "top.txt"))
	assertPruned(t, filepath.Join(fx.upper, "wip.txt"))
	assertPruned(t, filepath.Join(fx.upper, "sub", "gone.txt"))
	assertBytesFile(t, filepath.Join(fx.upper, "post.txt"), "post\n")

	action := onlyMountAction(t, fx.sessionsRoot, "dev")
	assertStack(t, action, wantFull, wantTip, "", "")
	if action.Status != session.StatusMounted {
		t.Fatalf("status = %q, want mounted", action.Status)
	}
}

// closeSession tears the recovered view down and checks that no
// mount or daemon residue survives.
func (fx rebaseFixture) closeSession(t *testing.T) {
	t.Helper()
	if out := mustCmd(t, append(fx.lib, "session", "close", "dev")...); out != "closed dev\n" {
		t.Fatalf("close output = %q", out)
	}
	assertNoResidue(t, fx.sessionsRoot, fx.view)
}

// TestSessionRebaseReplayPrunesUpperAfterFullRebuild is the full
// closure over the real toolchain: an in-view full rebuild rebases
// the record and parks the replaced stack in a hold the served ids
// pin; after the view dies, one open replays onto the new image,
// prunes the reused upper, clears the served ids, and the unpinned
// hold is archived by the next external publish.
func TestSessionRebaseReplayPrunesUpperAfterFullRebuild(t *testing.T) {
	fx := newRebaseFixture(t)

	// The rebuild publishes the merged content under a fresh id and
	// moves the record; the live view keeps serving the old stack.
	mode, newID := buildInView(t, fx.lib, fx.view, fx.work, "--full")
	if mode != "full" || newID == fx.oldID {
		t.Fatalf("in-view rebuild = (%q, %s), want a fresh full id", mode, newID)
	}
	assertStack(t, onlyMountAction(t, fx.sessionsRoot, "dev"),
		newID, newID, fx.oldID, fx.oldID)

	// The replaced stack rests in a hold, and the served ids pin it
	// while the dead view has not replayed yet.
	holdDir := filepath.Join(fx.repoDir, "_hold", fx.oldID)
	if _, err := os.Stat(holdDir); err != nil {
		t.Fatalf("the replaced stack left no hold: %v", err)
	}
	refs := session.Refs{Dir: fx.sessionsRoot}
	oldRef := image.Ref{Name: "alice/app", FullID: fx.oldID}
	if held, err := refs.Holds(oldRef); err != nil || !held {
		t.Fatalf("the served stack does not pin the hold: held=%v err=%v", held, err)
	}

	fx.staleView(t)
	fx.assertRecoveredReplay(t, newID, newID)

	// The cleared record unpins the replaced stack, so the next
	// external publish archives its hold instead of keeping it.
	if held, err := refs.Holds(oldRef); err != nil || held {
		t.Fatalf("the cleared record still pins the hold: held=%v err=%v", held, err)
	}
	src2 := filepath.Join(fx.work, "src2")
	writeTestFile(t, filepath.Join(src2, "top.txt"), "external rebuild\n")
	if id2 := buildImage(t, fx.lib, "alice/app", src2); id2 == newID {
		t.Fatalf("the external rebuild kept the id %s", newID)
	}
	if _, err := os.Stat(holdDir); !os.IsNotExist(err) {
		t.Fatalf("the unpinned hold survived the publish: %s (%v)", holdDir, err)
	}
	if _, err := os.Stat(filepath.Join(fx.repoDir, "_archives", fx.oldID)); err != nil {
		t.Fatalf("the publish did not archive the hold %s: %v", fx.oldID, err)
	}

	fx.closeSession(t)
}

// TestSessionRebaseReplayPrunesUpperAfterIncrBuild covers the
// incremental half over the real toolchain: an in-view layer build
// rebases only the desired tip; after the view dies, one open
// replays the two-lower cascade, prunes the baked upper entries
// against it, and clears the served ids.
func TestSessionRebaseReplayPrunesUpperAfterIncrBuild(t *testing.T) {
	fx := newRebaseFixture(t)

	// The explicit flag keeps the build incremental despite the
	// tiny base image: the upper becomes one layer on the full
	// root, and only the desired tip moves.
	mode, tipID := buildInView(t, fx.lib, fx.view, fx.work, "--incremental")
	if mode != "incr" {
		t.Fatalf("build mode = %q, want incr", mode)
	}
	assertStack(t, onlyMountAction(t, fx.sessionsRoot, "dev"),
		fx.oldID, tipID, fx.oldID, fx.oldID)
	layer := filepath.Join(fx.repoDir, image.IncrDir, tipID+image.SquashExt)
	if _, err := os.Stat(layer); err != nil {
		t.Fatalf("the new layer is missing: %v", err)
	}

	fx.staleView(t)
	fx.assertRecoveredReplay(t, fx.oldID, tipID)

	// The replayed view stacks the rebased layer over the full
	// root: two lowers, and a daemon serving the new layer file.
	entry, ok := entryAt(realTable(t), fx.view)
	if !ok || len(entry.Lowers) != 2 {
		t.Fatalf("the replayed view is not a two-lower cascade: %+v", entry)
	}
	if _, found := hasDaemon("squashfuse_ll", layer); !found {
		t.Error("no daemon serves the rebased layer")
	}

	fx.closeSession(t)
}

// buildInView runs one build from inside the mounted view — the
// working directory of a developer building in the workspace — and
// returns the mode and the id it printed. These happy paths run the
// real toolchain and must not warn.
func buildInView(t *testing.T, lib []string, view, back string, args ...string) (string, string) {
	t.Helper()
	t.Chdir(view)
	defer t.Chdir(back)
	cmdArgs := append(append([]string{}, lib...), "build")
	stdout, stderr, _, err := runCmd(t, append(cmdArgs, args...)...)
	if err != nil {
		t.Fatalf("in-view build %v: %v\n%s", args, err, stderr)
	}
	fields := strings.Fields(stdout)
	if len(fields) != 3 || fields[1] != "alice/app" || !isPayloadID(fields[2]) {
		t.Fatalf("in-view build stdout = %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("in-view build %v warned: %q", args, stderr)
	}
	return fields[0], fields[2]
}

// assertWhiteout checks one upper entry is the overlay whiteout of
// a file deleted through the live view: a character device with a
// zero rdev.
func assertWhiteout(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Mode&syscall.S_IFMT != syscall.S_IFCHR || st.Rdev != 0 {
		t.Fatalf("%s is not an overlay whiteout: %+v", path, info.Sys())
	}
}

// assertPruned checks one upper path the prune must have removed.
func assertPruned(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s survived the prune: %v", path, err)
	}
}
