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

// holdActiveSingleFile moves the active single-file leaf into
// _hold/<id>/ and installs a different active payload so pin and
// name-only mount diverge. Returns the held meta.
func holdActiveSingleFile(t *testing.T, imagesDir, name string, newBody []byte) image.Meta {
	t.Helper()
	repo, err := image.OpenRepo(imagesDir, name)
	if err != nil {
		t.Fatal(err)
	}
	held, err := image.Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	hold, err := repo.HoldPath(held.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(hold, "_incr"), 0o755); err != nil {
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
func writeSingleActive(t *testing.T, repoDir, name, formatType string, body []byte) image.Meta {
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
	meta := image.Meta{
		ID:        image.ID(sum),
		Name:      name,
		Version:   image.MetaVersion,
		Kind:      image.KindFull,
		Format:    image.Format{Type: formatType, Compression: image.CompressionZstd},
		Size:      info.Size(),
		SHA256:    hex.EncodeToString(sum[:]),
		MtimeUnix: info.ModTime().Unix(),
		CreatedAt: time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC),
		Builder:   "test",
	}
	if formatType == image.FormatDMG {
		meta.Format.Compression = "ulfo"
	}
	storeFixtureMeta(t, repoDir, meta)
	return meta
}

// holdActiveBlocks moves the active blocks leaf into _hold/<id>/ and
// installs a different active blocks payload.
func holdActiveBlocks(t *testing.T, imagesDir, name string, newBodies []string) image.Meta {
	t.Helper()
	repo, err := image.OpenRepo(imagesDir, name)
	if err != nil {
		t.Fatal(err)
	}
	held, err := image.Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	hold, err := repo.HoldPath(held.ID)
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
	action.FullID = held.ID
	writeSessionRecord(t, sessionsDir(home), "dev", action)

	if _, stderr, _, err := runCmd(t, append(lib, "session", "open", "dev")...); err != nil {
		t.Fatalf("session open: %v\n%s", err, stderr)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("engine calls = %d, want 1", len(f.mountReqs))
	}
	want := filepath.Join(repoDir, "_hold", held.ID, image.ImageFile)
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
	action.FullID = heldMeta.ID
	writeSessionRecord(t, sessionsDir(home), "dev", action)

	if _, stderr, _, err := runCmd(t, append(lib, "session", "open", "dev")...); err != nil {
		t.Fatalf("session open: %v\n%s", err, stderr)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("engine calls = %d, want 1", len(f.mountReqs))
	}
	want := filepath.Join(imagesDir, "alice", "app", "_hold", heldMeta.ID, image.ImageDMG)
	if f.mountReqs[0].Full != want {
		t.Fatalf("Full = %s, want hold %s", f.mountReqs[0].Full, want)
	}
}

func TestSessionOpenReplaysPinnedHoldBlocks(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	heldID := writeBlocksFixture(t, imagesDir, "alice/app", []string{"held-a", "held-b"}, true)
	held := holdActiveBlocks(t, imagesDir, "alice/app", []string{"new-active"})
	if held.ID != heldID {
		t.Fatalf("held id = %s, want %s", held.ID, heldID)
	}
	view := filepath.Join(work, "view")
	f := withMountSeams(t, nil)
	pinPortableHealth(t, f)

	action := libraryAction("alice/app", view, session.ModeRW)
	action.FullID = held.ID
	writeSessionRecord(t, sessionsDir(home), "dev", action)

	if _, stderr, _, err := runCmd(t, append(lib, "session", "open", "dev")...); err != nil {
		t.Fatalf("session open: %v\n%s", err, stderr)
	}
	if f.assembleN != 1 || len(f.assembleBlockPaths) != 1 {
		t.Fatalf("assemble = %d paths=%v", f.assembleN, f.assembleBlockPaths)
	}
	holdMarker := filepath.Join("_hold", held.ID, "_blocks", held.ID)
	for _, path := range f.assembleBlockPaths[0] {
		if !strings.Contains(path, holdMarker) {
			t.Fatalf("block path %s not under hold _blocks", path)
		}
	}
	wantBlk := filepath.Join(imagesDir, "alice", "app", "_hold", held.ID, "_blocks", held.ID, "blk.000")
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
		t.Fatalf("Full = %s, want active %s (held was %s)", f.mountReqs[0].Full, want, held.ID)
	}
}
