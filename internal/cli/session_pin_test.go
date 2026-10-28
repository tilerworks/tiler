package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/container"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/session"
)

// holdActiveSingleFile moves the active single-file leaf — payload,
// manifest, and any _incr chain — into _hold/<id>/ and installs a
// different active payload so pin and name-only mount diverge.
// Returns the held manifest.
func holdActiveSingleFile(t *testing.T, imagesDir, name string, newBody []byte) image.Manifest {
	t.Helper()
	repo, err := image.OpenRepo(imagesDir, name)
	if err != nil {
		t.Fatal(err)
	}
	held, err := image.LoadManifest(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	hold, err := repo.HoldPath(manifestRootID(held))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hold, 0o755); err != nil {
		t.Fatal(err)
	}
	// The held manifest resolves its chain under the hold, so a leaf
	// with incremental layers moves them along, like the real publish
	// hold does; an idle leaf keeps an empty _incr.
	if _, statErr := os.Stat(filepath.Join(repo.Dir, image.IncrDir)); statErr == nil {
		if err := os.Rename(filepath.Join(repo.Dir, image.IncrDir),
			filepath.Join(hold, image.IncrDir)); err != nil {
			t.Fatal(err)
		}
	} else if err := os.MkdirAll(filepath.Join(hold, image.IncrDir), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := image.ImageFile
	if held.Format.Type == image.FormatDMG {
		payload = image.ImageDMG
	}
	src := filepath.Join(repo.Dir, payload)
	dst := filepath.Join(hold, payload)
	if err := os.Rename(src, dst); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo.MetaPath(), filepath.Join(hold, "meta.yaml")); err != nil {
		t.Fatal(err)
	}
	writeSingleActive(t, repo.Dir, name, held.Format.Type, newBody)
	return held
}

// containerBody returns bytes whose magic matches formatType so
// mount's library format check accepts the leaf.
func containerBody(formatType, tag string) []byte {
	switch formatType {
	case image.FormatDMG:
		body := make([]byte, 512)
		copy(body, []byte("koly"))
		copy(body[4:], []byte(tag))
		return body
	default:
		return []byte("hsqs-" + tag)
	}
}

// writeSingleActive installs one single-file active leaf.
func writeSingleActive(t *testing.T, repoDir, name, formatType string, body []byte) image.Manifest {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(repoDir, "_incr"), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := image.ImageFile
	if formatType == image.FormatDMG {
		payload = image.ImageDMG
	}
	path := filepath.Join(repoDir, payload)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	compression := image.CompressionZstd
	if formatType == image.FormatDMG {
		compression = "ulfo"
	}
	manifest := image.Manifest{
		Version: image.ManifestVersion,
		Name:    name,
		Format:  image.Format{Type: formatType, Compression: compression},
		Head:    image.ID(sum),
		Images: []image.ImageMeta{{
			ID:        image.ID(sum),
			Kind:      image.KindFull,
			Size:      info.Size(),
			SHA256:    hex.EncodeToString(sum[:]),
			MtimeUnix: info.ModTime().Unix(),
			CreatedAt: time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC),
			Builder:   "test",
		}},
	}
	storeFixtureMeta(t, repoDir, manifest)
	return manifest
}

// holdActiveBlocks moves the active blocks leaf into _hold/<id>/ and
// installs a different active blocks payload.
func holdActiveBlocks(t *testing.T, imagesDir, name string, newBodies []string) image.Manifest {
	t.Helper()
	repo, err := image.OpenRepo(imagesDir, name)
	if err != nil {
		t.Fatal(err)
	}
	held, err := image.LoadManifest(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	hold, err := repo.HoldPath(manifestRootID(held))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hold, 0o755); err != nil {
		t.Fatal(err)
	}
	srcBlocks := filepath.Join(repo.Dir, "_blocks")
	dstBlocks := filepath.Join(hold, "_blocks")
	if err := os.Rename(srcBlocks, dstBlocks); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo.MetaPath(), filepath.Join(hold, "meta.yaml")); err != nil {
		t.Fatal(err)
	}
	writeBlocksFixture(t, imagesDir, name, newBodies, true)
	return held
}

func TestSessionOpenReplaysPinnedHoldSquashfs(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	repoDir := buildLibrary(t, home, lib, imagesDir, "alice/app")
	held := holdActiveSingleFile(t, imagesDir, "alice/app",
		containerBody(image.FormatSquashfs, "new-active"))
	view := filepath.Join(work, "view")
	f := withMountSeams(t, nil)
	pinPortableHealth(t, f)

	action := libraryAction("alice/app", view, session.ModeRW)
	action.FullID = manifestRootID(held)
	writeSessionRecord(t, sessionsDir(home), "dev", action)

	if _, stderr, _, err := runCmd(t, append(lib, "session", "open", "dev")...); err != nil {
		t.Fatalf("session open: %v\n%s", err, stderr)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("engine calls = %d, want 1", len(f.mountReqs))
	}
	want := filepath.Join(repoDir, "_hold", manifestRootID(held), image.ImageFile)
	if f.mountReqs[0].Full != want {
		t.Fatalf("Full = %s, want hold %s", f.mountReqs[0].Full, want)
	}
	runtime := filepath.Join(sessionsDir(home), "dev", "runtime", "m1")
	if f.mountReqs[0].RuntimeDir != runtime {
		t.Fatalf("runtime = %s, want %s", f.mountReqs[0].RuntimeDir, runtime)
	}
}

func TestSessionOpenReplaysPinnedHoldDMG(t *testing.T) {
	if _, err := container.Lookup(image.FormatDMG); err != nil {
		t.Skip("dmg is not registered on this OS")
	}
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	heldMeta := writeHashedDMGFixture(t, imagesDir, containerBody(image.FormatDMG, "held"))
	_ = holdActiveSingleFile(t, imagesDir, "alice/app",
		containerBody(image.FormatDMG, "new-active"))
	view := filepath.Join(work, "view")
	f := withMountSeams(t, nil)
	pinPortableHealth(t, f)

	action := libraryAction("alice/app", view, session.ModeRW)
	action.FullID = manifestRootID(heldMeta)
	writeSessionRecord(t, sessionsDir(home), "dev", action)

	if _, stderr, _, err := runCmd(t, append(lib, "session", "open", "dev")...); err != nil {
		t.Fatalf("session open: %v\n%s", err, stderr)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("engine calls = %d, want 1", len(f.mountReqs))
	}
	want := filepath.Join(imagesDir, "alice", "app", "_hold", manifestRootID(heldMeta), image.ImageDMG)
	if f.mountReqs[0].Full != want {
		t.Fatalf("Full = %s, want hold %s", f.mountReqs[0].Full, want)
	}
}

func TestSessionOpenReplaysPinnedHoldBlocks(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	heldID := writeBlocksFixture(t, imagesDir, "alice/app", []string{"held-a", "held-b"}, true)
	held := holdActiveBlocks(t, imagesDir, "alice/app", []string{"new-active"})
	if manifestRootID(held) != heldID {
		t.Fatalf("held id = %s, want %s", manifestRootID(held), heldID)
	}
	view := filepath.Join(work, "view")
	f := withMountSeams(t, nil)
	pinPortableHealth(t, f)

	action := libraryAction("alice/app", view, session.ModeRW)
	action.FullID = manifestRootID(held)
	writeSessionRecord(t, sessionsDir(home), "dev", action)

	if _, stderr, _, err := runCmd(t, append(lib, "session", "open", "dev")...); err != nil {
		t.Fatalf("session open: %v\n%s", err, stderr)
	}
	if f.assembleN != 1 || len(f.assembleBlockPaths) != 1 {
		t.Fatalf("assemble = %d paths=%v", f.assembleN, f.assembleBlockPaths)
	}
	holdMarker := filepath.Join("_hold", manifestRootID(held), "_blocks", manifestRootID(held))
	for _, path := range f.assembleBlockPaths[0] {
		if !strings.Contains(path, holdMarker) {
			t.Fatalf("block path %s not under hold _blocks", path)
		}
	}
	wantBlk := filepath.Join(imagesDir, "alice", "app", "_hold", manifestRootID(held), "_blocks", manifestRootID(held), "blk.000")
	if f.assembleBlockPaths[0][0] != wantBlk {
		t.Fatalf("blk.000 = %s, want %s", f.assembleBlockPaths[0][0], wantBlk)
	}
}

func TestSessionOpenEmptyFullIDUsesActive(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	repoDir := buildLibrary(t, home, lib, imagesDir, "alice/app")
	held := holdActiveSingleFile(t, imagesDir, "alice/app",
		containerBody(image.FormatSquashfs, "new-active-body"))
	view := filepath.Join(work, "view")
	f := withMountSeams(t, nil)
	pinPortableHealth(t, f)

	// Empty FullID: name-only mount must use the new active, not hold.
	writeSessionRecord(t, sessionsDir(home), "dev",
		libraryAction("alice/app", view, session.ModeRW))

	if _, stderr, _, err := runCmd(t, append(lib, "session", "open", "dev")...); err != nil {
		t.Fatalf("session open: %v\n%s", err, stderr)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("engine calls = %d, want 1", len(f.mountReqs))
	}
	want := filepath.Join(repoDir, image.ImageFile)
	if f.mountReqs[0].Full != want {
		t.Fatalf("Full = %s, want active %s (held was %s)", f.mountReqs[0].Full, want, manifestRootID(held))
	}
}

// TestSessionOpenReplaysPinnedHoldFreezesRecordedTip covers the
// record shape every new mount produces: a pin with a non-empty
// TipID whose stack an external build replaced. The hold leaf's head
// has advanced past the recorded tip, so replay must fall back to
// _hold/<fullID> and freeze the stack at the recorded tip instead of
// chasing the hold manifest's head, which keeps the recorded upper
// paired with identical lowers.
func TestSessionOpenReplaysPinnedHoldFreezesRecordedTip(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f, merged, upper := rebaseViewFixture(t, home, work)
	fullID := viewFixture(t, home, work, lib, merged)
	repoDir := filepath.Join(imagesDir, "alice", "app")

	// Two in-view incremental builds move the active head past the
	// first layer: full -> tipID -> headID. The flag keeps both
	// builds incremental despite the tiny fake base image.
	writeTestFile(t, filepath.Join(upper, "one.txt"), "layer one")
	mode, tipID := buildInsideView(t, lib, merged, "--incremental")
	if mode != "incr" {
		t.Fatalf("first build mode = %q, want incr", mode)
	}
	writeTestFile(t, filepath.Join(upper, "two.txt"), "layer two")
	mode, headID := buildInsideView(t, lib, merged, "--incremental")
	if mode != "incr" || headID == tipID {
		t.Fatalf("second build = (%q, %s), want a fresh incr head", mode, headID)
	}

	// An external full replacement parks the chain in _hold/<fullID>
	// and installs an active leaf that no longer roots at the pin.
	held := holdActiveSingleFile(t, imagesDir, "alice/app",
		containerBody(image.FormatSquashfs, "new-active"))
	if manifestRootID(held) != fullID {
		t.Fatalf("held root = %s, want %s", manifestRootID(held), fullID)
	}
	if string(held.Head) != headID {
		t.Fatalf("held head = %s, want %s", held.Head, headID)
	}

	// The record froze at the first layer — the shape a mount
	// recorded before the head moved on.
	view := filepath.Join(work, "view")
	writeSessionRecord(t, sessionsDir(home), "dev",
		pinnedAction("alice/app", view, session.ModeRW, fullID, tipID))

	// The FUSE daemon dies: replay resolves the pin through the hold
	// leaf, ending the stack at the recorded tip.
	f.table = nil
	if _, stderr, _, err := runCmd(t, append(lib, "session", "open", "dev")...); err != nil {
		t.Fatalf("session open: %v\n%s", err, stderr)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("engine calls = %d, want 1", len(f.mountReqs))
	}
	req := f.mountReqs[0]
	hold := filepath.Join(repoDir, "_hold", fullID)
	if want := filepath.Join(hold, image.ImageFile); req.Full != want {
		t.Fatalf("Full = %s, want hold %s", req.Full, want)
	}
	wantLayer := filepath.Join(hold, image.IncrDir, tipID+image.SquashExt)
	if len(req.Layers) != 1 || req.Layers[0] != wantLayer {
		t.Fatalf("Layers = %v, want [%s] frozen at the recorded tip, not head %s",
			req.Layers, wantLayer, headID)
	}
	if req.MergedDir != view {
		t.Fatalf("MergedDir = %s, want %s", req.MergedDir, view)
	}
	// The record carries no served ids, so the frozen replay keeps
	// the upper untouched.
	if req.PruneUpper {
		t.Fatal("PruneUpper = true, want a record without served ids to keep the upper")
	}
	// The real remount serves the frozen stack: the desired pin is
	// unchanged and the served ids stay clear.
	action := onlyMountAction(t, sessionsDir(home), "dev")
	assertStack(t, action, fullID, tipID, "", "")
	if action.Status != session.StatusMounted {
		t.Fatalf("status = %q, want mounted", action.Status)
	}
}

// rebaseViewFixture wires the build and mount seams over one listed
// writable view at merged, so an in-view build detects it. The view
// stays healthy until the test drops it from the fake table.
func rebaseViewFixture(t *testing.T, home, work string) (f *mountFake, merged, upper string) {
	t.Helper()
	imagesDir, _ := libFlags(home)
	merged = filepath.Join(work, "merged")
	upper = filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper")
	withBuildSeams(t, nil)
	f = withMountSeams(t, viewTable(imagesDir, "alice/app", merged, upper))
	pinPortableHealth(t, f)
	return f, merged, upper
}

// buildInsideView runs one in-view build and returns the mode and id
// it printed.
func buildInsideView(t *testing.T, lib []string, merged string, args ...string) (string, string) {
	t.Helper()
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(merged)
	legacyBuild = true
	cmdArgs := append(lib, "build")
	stdout, stderr, _, err := runCmd(t, append(cmdArgs, args...)...)
	if err != nil {
		t.Fatalf("in-view build: %v\n%s", err, stderr)
	}
	fields := strings.Fields(stdout)
	if len(fields) != 3 || fields[1] != "alice/app" || !isPayloadID(fields[2]) {
		t.Fatalf("build stdout = %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("in-view build warned: %q", stderr)
	}
	return fields[0], fields[2]
}

// TestSessionOpenReplaysRebasedIncrStack covers the replay half of an
// in-view incremental build: the rebase moved the desired tip onto
// the new layer, so after the live view dies the next mount resolves
// the rebased stack from the active leaf and the real remount clears
// the served ids it froze at rebase time.
func TestSessionOpenReplaysRebasedIncrStack(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f, merged, upper := rebaseViewFixture(t, home, work)
	fullID := viewFixture(t, home, work, lib, merged)
	repoDir := filepath.Join(imagesDir, "alice", "app")

	// An incremental build from the mounted view rebases the record:
	// the desired tip moves to the new layer while the live view
	// keeps serving the full-only stack.
	writeTestFile(t, filepath.Join(upper, "new.txt"), "layer")
	mode, tipID := buildInsideView(t, lib, merged)
	if mode != "incr" {
		t.Fatalf("build mode = %q, want incr", mode)
	}
	assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"),
		fullID, tipID, fullID, fullID)

	// The FUSE daemon dies: the view vanishes from the table, so the
	// replay remounts the rebased desired stack from the active leaf.
	f.table = nil
	if _, stderr, _, err := runCmd(t, append(lib, "session", "open", "dev")...); err != nil {
		t.Fatalf("session open: %v\n%s", err, stderr)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("engine calls = %d, want 1", len(f.mountReqs))
	}
	req := f.mountReqs[0]
	if want := filepath.Join(repoDir, image.ImageFile); req.Full != want {
		t.Fatalf("Full = %s, want the active %s", req.Full, want)
	}
	wantLayer := filepath.Join(repoDir, image.IncrDir, tipID+image.SquashExt)
	if len(req.Layers) != 1 || req.Layers[0] != wantLayer {
		t.Fatalf("Layers = %v, want [%s]", req.Layers, wantLayer)
	}
	if req.MergedDir != merged {
		t.Fatalf("MergedDir = %s, want %s", req.MergedDir, merged)
	}
	runtime := filepath.Join(sessionsDir(home), "dev", "runtime", "m1")
	if req.RuntimeDir != runtime {
		t.Fatalf("RuntimeDir = %s, want %s", req.RuntimeDir, runtime)
	}
	// The rebased record serves the full-only stack while the
	// desired stack gained a layer: the lower stack changed, so the
	// replay asks the engine to prune the reused upper.
	if !req.PruneUpper {
		t.Fatal("PruneUpper = false, want the rebased lower stack to prune the upper")
	}
	// The real remount serves the desired stack again: the served ids
	// are cleared in the same writeback that records the mount.
	action := onlyMountAction(t, sessionsDir(home), "dev")
	assertStack(t, action, fullID, tipID, "", "")
	if action.Status != session.StatusMounted {
		t.Fatalf("status = %q, want mounted", action.Status)
	}
}

// TestSessionOpenKeepsServedStackOfHealthyRebasedView guards the
// reuse side: a rebased view that is still listed and readable is
// adopted without a remount, so it keeps serving the replaced stack
// and the record keeps the served ids that pin it.
func TestSessionOpenKeepsServedStackOfHealthyRebasedView(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f, merged, upper := rebaseViewFixture(t, home, work)
	fullID := viewFixture(t, home, work, lib, merged)

	writeTestFile(t, filepath.Join(upper, "new.txt"), "layer")
	mode, tipID := buildInsideView(t, lib, merged)
	if mode != "incr" {
		t.Fatalf("build mode = %q, want incr", mode)
	}
	assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"),
		fullID, tipID, fullID, fullID)

	// The view stays healthy: open adopts it and touches nothing.
	if _, stderr, _, err := runCmd(t, append(lib, "session", "open", "dev")...); err != nil {
		t.Fatalf("session open: %v\n%s", err, stderr)
	}
	if len(f.mountReqs) != 0 {
		t.Fatalf("a healthy view was remounted: %d calls", len(f.mountReqs))
	}
	assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"),
		fullID, tipID, fullID, fullID)
}

// TestSessionOpenReplaysRebasedFullAndFreesOldHold covers the full
// closure: an in-view full rebuild rebases the record and parks the
// replaced stack in a hold the served ids pin; after the dead view
// replays onto the new active stack, the real remount clears the
// served ids, and the next publish archives the now unpinned hold.
func TestSessionOpenReplaysRebasedFullAndFreesOldHold(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f, merged, _ := rebaseViewFixture(t, home, work)
	oldID := viewFixture(t, home, work, lib, merged)
	repoDir := filepath.Join(imagesDir, "alice", "app")

	// A full rebuild from the mounted view rebases the record onto
	// the new image; the replaced stack rests in _hold/<oldID>.
	writeTestFile(t, filepath.Join(merged, "base.txt"), "changed in view")
	mode, newID := buildInsideView(t, lib, merged, "--full")
	if mode != "full" || newID == oldID {
		t.Fatalf("in-view rebuild = (%q, %s), want a fresh full id", mode, newID)
	}
	assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"),
		newID, newID, oldID, oldID)
	holdDir := filepath.Join(repoDir, "_hold", oldID)
	if _, err := os.Stat(holdDir); err != nil {
		t.Fatalf("the replaced stack left no hold: %v", err)
	}
	// While the served ids name the replaced stack, the record pins
	// it and the hold survives the archive question.
	refs := session.Refs{Dir: sessionsDir(home)}
	oldRef := image.Ref{Name: "alice/app", FullID: oldID}
	if held, err := refs.Holds(oldRef); err != nil || !held {
		t.Fatalf("served stack does not pin the hold: held=%v err=%v", held, err)
	}

	// The FUSE daemon dies: the replay mounts the rebased stack from
	// the active leaf and the real remount clears the served ids.
	f.table = nil
	if _, stderr, _, err := runCmd(t, append(lib, "session", "open", "dev")...); err != nil {
		t.Fatalf("session open: %v\n%s", err, stderr)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("engine calls = %d, want 1", len(f.mountReqs))
	}
	req := f.mountReqs[0]
	if want := filepath.Join(repoDir, image.ImageFile); req.Full != want {
		t.Fatalf("Full = %s, want the active %s", req.Full, want)
	}
	if len(req.Layers) != 0 {
		t.Fatalf("Layers = %v, want none", req.Layers)
	}
	// The rebuild replaced the whole stack: the served ids resolve
	// through the hold leaf to other members than the desired stack,
	// so the replay asks the engine to prune the reused upper.
	if !req.PruneUpper {
		t.Fatal("PruneUpper = false, want the replaced lower stack to prune the upper")
	}
	assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"),
		newID, newID, "", "")
	if held, err := refs.Holds(oldRef); err != nil || held {
		t.Fatalf("the cleared record still pins the hold: held=%v err=%v", held, err)
	}

	// The next publish archives the unpinned hold; the stack the
	// record now pins stays held.
	src2 := filepath.Join(work, "src2")
	writeTestFile(t, filepath.Join(src2, "base.txt"), "external rebuild")
	if newID2 := buildImage(t, lib, "alice/app", src2); newID2 == newID {
		t.Fatalf("external rebuild kept the id %s", newID)
	}
	if _, err := os.Stat(holdDir); !os.IsNotExist(err) {
		t.Fatalf("the unpinned hold survived the publish: %s (%v)", holdDir, err)
	}
	archDir := filepath.Join(repoDir, "_archives", oldID)
	if _, err := os.Stat(archDir); err != nil {
		t.Fatalf("the publish did not archive the hold %s: %v", archDir, err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "_hold", newID)); err != nil {
		t.Fatalf("the pinned stack lost its hold: %v", err)
	}
}
