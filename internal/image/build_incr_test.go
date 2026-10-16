package image

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
)

func upperDir(t *testing.T) string {
	t.Helper()
	upper := t.TempDir()
	if err := os.WriteFile(filepath.Join(upper, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	return upper
}

// skipSmoke keeps unit tests with fake packer output away from the
// default read-only mount gate.
func skipSmoke(string) error { return nil }

func TestBuildIncrAppendsLayerToChain(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	active := seedActive(t, repo, []byte("base-full"), false)
	upper := upperDir(t)
	body := []byte("layer-one")
	runner := &fakeRunner{body: body}
	req := IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upper,
		Source: Source{Commit: "abc123"},
		Now:    fixedNow, Run: runner.run, MountSmoke: skipSmoke,
	}
	res, err := BuildIncr(req)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if res.ID != ID(sum) || res.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("result digest = %s %s", res.ID, res.SHA256)
	}
	wantPath, err := repo.IncrPath(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != wantPath {
		t.Fatalf("result path = %s", res.Path)
	}
	assertBytes(t, wantPath, body)
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != active.ID || len(loaded.Chain) != 1 {
		t.Fatalf("active meta = %+v", loaded)
	}
	entry := loaded.Chain[0]
	if entry.Parent != active.ID || entry.ID != res.ID ||
		entry.Size != int64(len(body)) || entry.SHA256 != res.SHA256 ||
		entry.Source != req.Source || !entry.CreatedAt.Equal(fixedNow()) {
		t.Fatalf("chain entry = %+v", entry)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %v", runner.calls)
	}
	call := runner.calls[0]
	want := []string{"mksquashfs", upper, call[2], "-comp", "zstd", "-noappend", "-xattrs"}
	if !equalArgs(call, want) {
		t.Fatalf("runner call = %q", call)
	}
	incrDir := filepath.Join(repo.Dir, dirIncr)
	if filepath.Dir(call[2]) != incrDir || !strings.HasPrefix(filepath.Base(call[2]), ".") {
		t.Fatalf("temp layer %s is outside the incr directory", call[2])
	}
	assertNoTemps(t, incrDir)
	assertNoTemps(t, repo.Dir)

	nextBody := []byte("layer-two")
	req.Run = (&fakeRunner{body: nextBody}).run
	res2, err := BuildIncr(req)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Chain) != 2 || loaded.Chain[1].Parent != res.ID || loaded.Chain[1].ID != res2.ID {
		t.Fatalf("chain = %+v", loaded.Chain)
	}
}

func TestBuildIncrRequiresActiveFull(t *testing.T) {
	cfg := buildConfig(t)
	runner := &fakeRunner{body: []byte("layer")}
	_, err := BuildIncr(IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t), Run: runner.run,
	})
	if err == nil || !strings.Contains(err.Error(), "no active full image") {
		t.Fatalf("err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran without an active full: %v", runner.calls)
	}
}

func TestBuildIncrRefusesWhenChainFull(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	active := seedActive(t, repo, []byte("base-full"), false)
	ids := []string{
		"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc",
		"dddddddddddddddd", "eeeeeeeeeeeeeeee", "ffffffffffffffff",
		"0000000000000000",
	}
	parent := active.ID
	for _, id := range ids {
		active.Chain = append(active.Chain, validEntry(parent, id))
		parent = id
	}
	if err := Store(repo.MetaPath(), active); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{body: []byte("one-too-many")}
	_, err = BuildIncr(IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t), Run: runner.run,
	})
	if err == nil || !strings.Contains(err.Error(), "new full image") {
		t.Fatalf("err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran on a full chain: %v", runner.calls)
	}
	entries, err := os.ReadDir(filepath.Join(repo.Dir, dirIncr))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("incr entries left behind: %v", entries)
	}
}

func TestBuildIncrSameDigestIsNoOp(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	seedActive(t, repo, []byte("base-full"), false)
	upper := upperDir(t)
	body := []byte("stable-layer")
	req := IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upper, Now: fixedNow,
		MountSmoke: skipSmoke,
	}
	req.Run = (&fakeRunner{body: body}).run
	first, err := BuildIncr(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Run = (&fakeRunner{body: body}).run
	second, err := BuildIncr(req)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Path != first.Path {
		t.Fatalf("rebuild = %+v, want %+v", second, first)
	}
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Chain) != 1 {
		t.Fatalf("chain appended twice: %+v", loaded.Chain)
	}
	entries, err := os.ReadDir(filepath.Join(repo.Dir, dirIncr))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("incr entries = %v", entries)
	}
	assertNoTemps(t, repo.Dir)
}

func TestBuildIncrRestoresMissingLayerFile(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	active := seedActive(t, repo, []byte("base-full"), false)
	body := []byte("layer-lost-its-file")
	sum := sha256.Sum256(body)
	active.Chain = []ChainEntry{realEntry(active.ID, body)}
	if err := Store(repo.MetaPath(), active); err != nil {
		t.Fatal(err)
	}
	req := IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t), Now: fixedNow,
		Run: (&fakeRunner{body: body}).run, MountSmoke: skipSmoke,
	}
	res, err := BuildIncr(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != ID(sum) {
		t.Fatalf("result id = %s", res.ID)
	}
	assertBytes(t, res.Path, body)
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Chain) != 1 {
		t.Fatalf("chain = %+v", loaded.Chain)
	}
}

// realEntry is a chain member whose digest fields describe body, so
// verification of the layer file can pass.
func realEntry(parent string, body []byte) ChainEntry {
	sum := sha256.Sum256(body)
	return ChainEntry{
		ID:        ID(sum),
		Parent:    parent,
		Size:      int64(len(body)),
		SHA256:    hex.EncodeToString(sum[:]),
		MtimeUnix: 1710000001,
		CreatedAt: fixedNow(),
	}
}

func TestBuildIncrVerifiesPublishedLayer(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	active := seedActive(t, repo, []byte("base-full"), false)
	req := IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t), Now: fixedNow,
		Run: (&fakeRunner{body: []byte("good-layer")}).run, MountSmoke: skipSmoke,
	}
	incrDir := filepath.Join(repo.Dir, dirIncr)
	_, err = buildIncr(req, hooks{
		rename: func(oldpath, newpath string) error {
			if filepath.Dir(newpath) == incrDir && strings.HasSuffix(newpath, squashExt) {
				// Simulate storage that corrupts the layer as it lands.
				if writeErr := os.WriteFile(newpath, []byte("corrupted"), 0o644); writeErr != nil {
					return writeErr
				}
				return os.Remove(oldpath)
			}
			return os.Rename(oldpath, newpath)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "does not match sha256") {
		t.Fatalf("err = %v", err)
	}
	loaded, loadErr := Load(repo.MetaPath())
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if loaded.ID != active.ID || len(loaded.Chain) != 0 {
		t.Fatalf("meta changed despite the failed verification: %+v", loaded)
	}
	entries, readErr := os.ReadDir(incrDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("corrupt layer left behind: %v", entries)
	}
	assertNoTemps(t, repo.Dir)
}

func TestBuildIncrReuseDetectsCorruptLayer(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	active := seedActive(t, repo, []byte("base-full"), false)
	body := []byte("layer-with-corrupt-twin")
	active.Chain = []ChainEntry{realEntry(active.ID, body)}
	if err := Store(repo.MetaPath(), active); err != nil {
		t.Fatal(err)
	}
	incrDir := filepath.Join(repo.Dir, dirIncr)
	if err := os.MkdirAll(incrDir, 0o755); err != nil {
		t.Fatal(err)
	}
	corrupt, err := repo.IncrPath(active.Chain[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corrupt, []byte("corrupted-on-disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t), Now: fixedNow,
		Run: (&fakeRunner{body: body}).run, MountSmoke: skipSmoke,
	}
	_, err = BuildIncr(req)
	if err == nil || !strings.Contains(err.Error(), "does not match sha256") {
		t.Fatalf("err = %v", err)
	}
	assertBytes(t, corrupt, []byte("corrupted-on-disk"))
	loaded, loadErr := Load(repo.MetaPath())
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(loaded.Chain) != 1 {
		t.Fatalf("chain = %+v", loaded.Chain)
	}
}

func TestBuildIncrSubdirMovesUpperWithoutCopy(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	active := seedActive(t, repo, []byte("base-full"), false)
	upper := upperDir(t)
	device := filepath.Join(upper, "whiteout")
	deviceOK := true
	if err := syscall.Mknod(device, syscall.S_IFCHR|0o644, 0); err != nil {
		deviceOK = false
		t.Logf("mknod unavailable; skipping only the device assertion: %v", err)
	}
	var packedSrc string
	run := func(_ string, args ...string) error {
		packedSrc = args[0]
		if data, readErr := os.ReadFile(filepath.Join(args[0], "new.txt")); readErr != nil || string(data) != "new" {
			return fmt.Errorf("upper content missing under the prefix: %v", readErr)
		}
		if _, statErr := os.Stat(upper); !errors.Is(statErr, os.ErrNotExist) {
			return errors.New("upper was copied instead of moved")
		}
		if deviceOK {
			info, lerr := os.Lstat(filepath.Join(args[0], "whiteout"))
			if lerr != nil || info.Mode()&os.ModeCharDevice == 0 {
				return fmt.Errorf("whiteout is not a character device after the move: %v", lerr)
			}
		}
		return os.WriteFile(args[1], []byte("subdir-layer"), 0o644)
	}
	res, err := BuildIncr(IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upper, Subdir: "a/b",
		Now: fixedNow, Run: run, MountSmoke: skipSmoke,
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(filepath.Dir(packedSrc)) != "a" {
		t.Fatalf("packed source %s does not sit under the prefix", packedSrc)
	}
	root := filepath.Dir(filepath.Dir(packedSrc))
	if filepath.Dir(root) != filepath.Dir(upper) || !strings.HasPrefix(filepath.Base(root), ".") {
		t.Fatalf("temp root %s is not a hidden directory beside the upper", root)
	}
	assertBytes(t, filepath.Join(upper, "new.txt"), []byte("new"))
	if deviceOK {
		info, lerr := os.Lstat(device)
		if lerr != nil || info.Mode()&os.ModeCharDevice == 0 {
			t.Fatalf("whiteout did not survive the round trip: %v", lerr)
		}
	}
	if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temp root left behind: %v", statErr)
	}
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Chain) != 1 || loaded.Chain[0].Parent != active.ID || loaded.Chain[0].ID != res.ID {
		t.Fatalf("chain = %+v", loaded.Chain)
	}
}

func TestBuildIncrRejectsBadSubdir(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	seedActive(t, repo, []byte("base-full"), false)
	upper := upperDir(t)
	for _, subdir := range []string{"/abs/path", "../escape", "a/../b", "./rel"} {
		runner := &fakeRunner{body: []byte("never-packed")}
		_, err := BuildIncr(IncrRequest{
			Config: cfg, Name: "alice/app", Upper: upper, Subdir: subdir, Run: runner.run,
		})
		if err == nil {
			t.Errorf("subdir %q was accepted", subdir)
		}
		if len(runner.calls) != 0 {
			t.Errorf("packer ran for subdir %q: %v", subdir, runner.calls)
		}
	}
	assertBytes(t, filepath.Join(upper, "new.txt"), []byte("new"))
}

func TestBuildIncrMetaRenameFailureRemovesLayer(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	active := seedActive(t, repo, []byte("base-full"), false)
	req := IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t), Now: fixedNow,
		Run: (&fakeRunner{body: []byte("doomed-layer")}).run,
	}
	err = buildIncrWithErrorOnMetaRename(t, repo, req)
	if err == nil || !strings.Contains(err.Error(), "meta rename failed") {
		t.Fatalf("err = %v", err)
	}
	loaded, loadErr := Load(repo.MetaPath())
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if loaded.ID != active.ID || len(loaded.Chain) != 0 {
		t.Fatalf("old meta changed: %+v", loaded)
	}
	entries, readErr := os.ReadDir(filepath.Join(repo.Dir, dirIncr))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("incr entries left behind: %v", entries)
	}
	assertNoTemps(t, repo.Dir)
}

func buildIncrWithErrorOnMetaRename(t *testing.T, repo Repo, req IncrRequest) error {
	t.Helper()
	_, err := buildIncr(req, hooks{
		rename: func(oldpath, newpath string) error {
			if newpath == repo.MetaPath() {
				return errors.New("meta rename failed")
			}
			return os.Rename(oldpath, newpath)
		},
	})
	return err
}

func TestBuildIncrPackerFailureLeavesNoLayer(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	seedActive(t, repo, []byte("base-full"), false)
	runner := &fakeRunner{err: errors.New("mksquashfs: exit status 1: boom")}
	_, err = BuildIncr(IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t), Run: runner.run,
	})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	loaded, loadErr := Load(repo.MetaPath())
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(loaded.Chain) != 0 {
		t.Fatalf("chain changed: %+v", loaded.Chain)
	}
	entries, readErr := os.ReadDir(filepath.Join(repo.Dir, dirIncr))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("incr entries left behind: %v", entries)
	}
	assertNoTemps(t, repo.Dir)
}

func TestBuildIncrMountSmokeGate(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	seedActive(t, repo, []byte("base-full"), false)
	var smoked []string
	req := IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t), Now: fixedNow,
		Run: (&fakeRunner{body: []byte("smoked-layer")}).run,
		MountSmoke: func(image string) error {
			smoked = append(smoked, image)
			return nil
		},
	}
	res, err := BuildIncr(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(smoked) != 1 || smoked[0] != res.Path {
		t.Fatalf("smoke gate saw %v", smoked)
	}
	req.Run = (&fakeRunner{body: []byte("rejected-layer")}).run
	req.MountSmoke = func(string) error { return errors.New("smoke: unreadable image") }
	_, err = BuildIncr(req)
	if err == nil || !strings.Contains(err.Error(), "quality gate") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildIncrRefusesSplitStorage(t *testing.T) {
	cfg := buildConfig(t)
	cfg.SplitEnabled = true
	cfg.SplitMode = config.SplitModeMembers
	runner := &fakeRunner{body: []byte("never-packed")}
	_, err := BuildIncr(IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t), Run: runner.run,
	})
	if err == nil || !strings.Contains(err.Error(), "split") {
		t.Fatalf("err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran despite split storage: %v", runner.calls)
	}
}

func TestBuildIncrUpperMustBeDirectory(t *testing.T) {
	cfg := buildConfig(t)
	_, err := BuildIncr(IncrRequest{
		Config: cfg, Name: "alice/app",
		Upper: filepath.Join(t.TempDir(), "absent"),
	})
	if err == nil || !strings.Contains(err.Error(), "upper") {
		t.Fatalf("err = %v", err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = BuildIncr(IncrRequest{Config: cfg, Name: "alice/app", Upper: file})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("err = %v", err)
	}
}
