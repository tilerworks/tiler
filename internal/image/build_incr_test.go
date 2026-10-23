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
	"github.com/jiangxin/tiler/internal/container"
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
	run := func(_ string, _ []string, args ...string) error {
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

func TestBuildIncrZeroSplitSizeDoesNotRefuse(t *testing.T) {
	cfg := buildConfig(t)
	if cfg.SplitSize != 0 {
		t.Fatalf("split size = %d, want split off", cfg.SplitSize)
	}
	runner := &fakeRunner{body: []byte("never-packed")}
	_, err := BuildIncr(IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t), Run: runner.run,
	})
	if err == nil || strings.Contains(err.Error(), "split") {
		t.Fatalf("err = %v, want a non-split failure", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran: %v", runner.calls)
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

// seedBlocksActive publishes a blocks-form full image for incr tests.
func seedBlocksActive(t *testing.T, cfg config.Config, body []byte, blockSize int64) (Repo, Meta) {
	t.Helper()
	src := sourceDir(t)
	_, err := BuildFull(FullRequest{
		Config:    cfg,
		Name:      "alice/app",
		SrcDir:    src,
		Builder:   "test",
		Now:       fixedNow,
		Run:       (&fakeRunner{body: body}).run,
		BlockSize: blockSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if PayloadKind(loaded) != PayloadBlocks {
		t.Fatalf("active form = %s, want blocks", PayloadKind(loaded))
	}
	return repo, loaded
}

func TestBuildIncrBlocksPublishesChainBlocks(t *testing.T) {
	cfg := buildConfig(t)
	cfg.SplitSize = 24
	repo, active := seedBlocksActive(t, cfg,
		[]byte(strings.Repeat("0123456789abcdef", 4)), 24)
	body := []byte(strings.Repeat("fedcba9876543210", 4)) // 64 bytes
	var smoked []string
	req := IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t),
		Source: Source{Commit: "abc123"}, Now: fixedNow,
		Run: (&fakeRunner{body: body}).run,
		MountSmoke: func(image string) error {
			smoked = append(smoked, image)
			return nil
		},
	}
	res, err := BuildIncr(req)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	wantDir, err := repo.BlocksDirPath(ID(sum))
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != ID(sum) || res.Path != wantDir || res.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("result = %+v, want path %s", res, wantDir)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, dirIncr)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocks incr left _incr/: %v", err)
	}
	parts := [][]byte{body[0:24], body[24:48], body[48:64]}
	for i, part := range parts {
		assertBytes(t, filepath.Join(wantDir, fmt.Sprintf(blockNameFmt, i)), part)
	}
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
		len(entry.Blocks) != 3 || entry.Source != req.Source ||
		!entry.CreatedAt.Equal(fixedNow()) {
		t.Fatalf("chain entry = %+v", entry)
	}
	for i, block := range entry.Blocks {
		partSum := sha256.Sum256(parts[i])
		if block.Name != fmt.Sprintf(blockNameFmt, i) ||
			block.Size != int64(len(parts[i])) ||
			block.SHA256 != hex.EncodeToString(partSum[:]) {
			t.Fatalf("chain blocks[%d] = %+v", i, block)
		}
	}
	if len(smoked) != 1 {
		t.Fatalf("smoke gate saw %v", smoked)
	}
	assertBytes(t, smoked[0], body)
	assertNoTemps(t, repo.Dir)
}

func TestBuildIncrSingleKeepsIncrSqsh(t *testing.T) {
	cfg := buildConfig(t)
	if cfg.SplitSize != 0 {
		t.Fatalf("split size = %d, want 0", cfg.SplitSize)
	}
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	seedActive(t, repo, []byte("base-full"), false)
	body := []byte("single-layer")
	res, err := BuildIncr(IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t),
		Now: fixedNow, Run: (&fakeRunner{body: body}).run, MountSmoke: skipSmoke,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPath, err := repo.IncrPath(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != wantPath {
		t.Fatalf("path = %s, want %s", res.Path, wantPath)
	}
	assertBytes(t, wantPath, body)
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Chain) != 1 || len(loaded.Chain[0].Blocks) != 0 {
		t.Fatalf("chain = %+v", loaded.Chain)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, dirBlocks)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("single incr created _blocks/: %v", err)
	}
}

func TestBuildIncrRefusesSplitOnSingleActive(t *testing.T) {
	cfg := buildConfig(t)
	cfg.SplitSize = 24
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	seedActive(t, repo, []byte("base-full"), false)
	runner := &fakeRunner{body: []byte("should-not-pack")}
	_, err = BuildIncr(IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t), Run: runner.run,
	})
	if err == nil || !strings.Contains(err.Error(), "single form") ||
		!strings.Contains(err.Error(), "full image") {
		t.Fatalf("err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran on refused incr: %v", runner.calls)
	}
}

func TestBuildIncrRefusesZeroSplitOnBlocksActive(t *testing.T) {
	cfg := buildConfig(t)
	cfg.SplitSize = 24
	seedBlocksActive(t, cfg, []byte(strings.Repeat("0123456789abcdef", 4)), 24)
	cfg.SplitSize = 0
	runner := &fakeRunner{body: []byte("should-not-pack")}
	_, err := BuildIncr(IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t), Run: runner.run,
	})
	if err == nil || !strings.Contains(err.Error(), "blocks form") ||
		!strings.Contains(err.Error(), "full image") {
		t.Fatalf("err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran on refused incr: %v", runner.calls)
	}
}

func TestBuildIncrBlocksSameDigestIsNoOp(t *testing.T) {
	cfg := buildConfig(t)
	cfg.SplitSize = 24
	repo, _ := seedBlocksActive(t, cfg,
		[]byte(strings.Repeat("0123456789abcdef", 4)), 24)
	body := []byte(strings.Repeat("stable-layer-pad!", 4)) // 64 bytes
	req := IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t), Now: fixedNow,
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
	entries, err := os.ReadDir(filepath.Join(repo.Dir, dirBlocks))
	if err != nil {
		t.Fatal(err)
	}
	// full blocks dir + one incr blocks dir
	if len(entries) != 2 {
		t.Fatalf("blocks entries = %v", entries)
	}
	assertNoTemps(t, repo.Dir)
	if _, err := os.Stat(filepath.Join(repo.Dir, dirIncr)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocks incr left _incr/: %v", err)
	}
}

func TestBuildIncrBlocksRestoresMissingDir(t *testing.T) {
	cfg := buildConfig(t)
	cfg.SplitSize = 24
	repo, _ := seedBlocksActive(t, cfg,
		[]byte(strings.Repeat("0123456789abcdef", 4)), 24)
	body := []byte(strings.Repeat("restore-blocks!!", 4)) // 64 bytes
	req := IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t), Now: fixedNow,
		MountSmoke: skipSmoke,
	}
	req.Run = (&fakeRunner{body: body}).run
	first, err := BuildIncr(req)
	if err != nil {
		t.Fatal(err)
	}
	before, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(first.Path); err != nil {
		t.Fatal(err)
	}
	req.Run = (&fakeRunner{body: body}).run
	second, err := BuildIncr(req)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Path != first.Path || second.SHA256 != first.SHA256 {
		t.Fatalf("restore = %+v, want %+v", second, first)
	}
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Chain) != 1 || len(loaded.Chain[0].Blocks) != len(before.Chain[0].Blocks) {
		t.Fatalf("chain = %+v", loaded.Chain)
	}
	for i, block := range loaded.Chain[0].Blocks {
		want := before.Chain[0].Blocks[i]
		if block.Name != want.Name || block.Size != want.Size || block.SHA256 != want.SHA256 {
			t.Fatalf("blocks[%d] = %+v, want %+v", i, block, want)
		}
	}
	ok, err := dirMatchesBlocks(second.Path, loaded.Chain[0].Blocks)
	if err != nil || !ok {
		t.Fatalf("restored blocks dir match = %v, %v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, dirIncr)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocks restore left _incr/: %v", err)
	}
}

func TestBuildIncrBlocksMetaRenameFailureRemovesDir(t *testing.T) {
	cfg := buildConfig(t)
	cfg.SplitSize = 24
	repo, active := seedBlocksActive(t, cfg,
		[]byte(strings.Repeat("0123456789abcdef", 4)), 24)
	body := []byte(strings.Repeat("doomed-blocks-pad", 4)) // 68 bytes
	req := IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t), Now: fixedNow,
		Run: (&fakeRunner{body: body}).run,
	}
	err := buildIncrWithErrorOnMetaRename(t, repo, req)
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
	sum := sha256.Sum256(body)
	doomed, err := repo.BlocksDirPath(ID(sum))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(doomed); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("fresh blocks dir left behind: %v", statErr)
	}
	fullDir, err := repo.BlocksDirPath(active.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(fullDir); statErr != nil {
		t.Fatalf("full blocks dir: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(repo.Dir, dirIncr)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("blocks incr left _incr/: %v", statErr)
	}
	assertNoTemps(t, repo.Dir)
}

func TestBuildIncrInheritsActiveFormat(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	active := seedActive(t, repo, []byte("base-full"), false)
	active.Format = Format{Type: FormatErofs, Compression: "lz4"}
	if err := Store(repo.MetaPath(), active); err != nil {
		t.Fatal(err)
	}
	upper := upperDir(t)
	runner := &fakeRunner{body: []byte("layer-erofs")}
	_, err = BuildIncr(IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upper,
		Now: fixedNow, Run: runner.run, MountSmoke: skipSmoke,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %v", runner.calls)
	}
	call := runner.calls[0]
	if call[0] != "mkfs.erofs" || call[3] != "-zlz4" || call[len(call)-1] != upper {
		t.Fatalf("runner call = %q", call)
	}
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Format != active.Format || len(loaded.Chain) != 1 {
		t.Fatalf("meta = %+v", loaded)
	}
}

func TestBuildIncrDefaultSmokeMountsActiveFormat(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	active := seedActive(t, repo, []byte("base-full"), false)
	active.Format = Format{Type: FormatErofs, Compression: "lz4"}
	if err := Store(repo.MetaPath(), active); err != nil {
		t.Fatal(err)
	}
	format, err := container.Lookup(FormatErofs)
	if err != nil {
		t.Fatal(err)
	}

	prevReady, prevRun := smokeReady, smokeRun
	t.Cleanup(func() {
		smokeReady = prevReady
		smokeRun = prevRun
	})
	var readyType string
	var cmds [][]string
	smokeReady = func(f container.Format) error {
		readyType = f.Type()
		return nil
	}
	smokeRun = func(name string, args ...string) error {
		cmds = append(cmds, append([]string{name}, args...))
		return nil
	}

	runner := &fakeRunner{body: []byte("layer-erofs-smoke")}
	// MountSmoke stays nil so the default gate chooses the command.
	res, err := BuildIncr(IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t),
		Now: fixedNow, Run: runner.run,
	})
	if err != nil {
		t.Fatal(err)
	}
	if readyType != FormatErofs {
		t.Fatalf("smoke checked %q", readyType)
	}
	for _, cmd := range cmds {
		if len(cmd) > 0 && cmd[0] == "squashfuse_ll" {
			t.Fatalf("smoke used squashfuse_ll: %q", cmd)
		}
	}
	if len(cmds) != 2 {
		t.Fatalf("smoke commands = %q", cmds)
	}
	point := cmds[0][len(cmds[0])-1]
	want := format.MountArgv(res.Path, "", "", res.Path, point)
	if !equalArgs(cmds[0], want) {
		t.Fatalf("mount = %q, want %q", cmds[0], want)
	}
	unmount := []string{"fusermount3", "-u", point}
	if !equalArgs(cmds[1], unmount) {
		t.Fatalf("unmount = %q, want %q", cmds[1], unmount)
	}
}

func TestBuildIncrAcceptsMatchingFormat(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	seedActive(t, repo, []byte("base-full"), false)
	upper := upperDir(t)
	runner := &fakeRunner{body: []byte("layer-match")}
	_, err = BuildIncr(IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upper,
		Format: FormatSquashfs, Compression: CompressionZstd,
		Now: fixedNow, Run: runner.run, MountSmoke: skipSmoke,
	})
	if err != nil {
		t.Fatal(err)
	}
	call := runner.calls[0]
	want := []string{"mksquashfs", upper, call[2], "-comp", "zstd", "-noappend", "-xattrs"}
	if !equalArgs(call, want) {
		t.Fatalf("runner call = %q", call)
	}
}

func TestBuildIncrRefusesFormatConflict(t *testing.T) {
	cases := []struct {
		name        string
		format      string
		compression string
		want        string
	}{
		{name: "format", format: FormatErofs, want: "--format erofs"},
		{name: "compression", compression: "gzip", want: "--compression gzip"},
		{
			name: "both", format: FormatErofs, compression: "lz4",
			want: "--format erofs, --compression lz4",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := buildConfig(t)
			repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
			if err != nil {
				t.Fatal(err)
			}
			seedActive(t, repo, []byte("base-full"), false)
			runner := &fakeRunner{body: []byte("should-not-pack")}
			_, err = BuildIncr(IncrRequest{
				Config: cfg, Name: "alice/app", Upper: upperDir(t),
				Format: tc.format, Compression: tc.compression,
				Run: runner.run, MountSmoke: skipSmoke,
			})
			if err == nil || !errors.Is(err, ErrFormatConflict) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("packer ran: %v", runner.calls)
			}
		})
	}
}

func TestBuildIncrRefusesIllegalActiveFormat(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	seedActive(t, repo, []byte("base-full"), false)
	// Store refuses this pair, so the test writes the bytes the way a
	// hand-edited meta.yaml would.
	body, err := os.ReadFile(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(body), "type: squashfs", "type: erofs", 1)
	text = strings.Replace(text, "compression: zstd", "compression: xz", 1)
	if err := os.WriteFile(repo.MetaPath(), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{body: []byte("should-not-pack")}
	_, err = BuildIncr(IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t),
		Run: runner.run, MountSmoke: skipSmoke,
	})
	if err == nil || !strings.Contains(err.Error(), `does not allow "xz"`) {
		t.Fatalf("err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran: %v", runner.calls)
	}
}

func TestBuildIncrStopsWhenSpaceIsShort(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	seedActive(t, repo, []byte("base-full"), false)
	stubSpace(t, 0, nil)
	runner := &fakeRunner{body: []byte("should-not-pack")}
	_, err = BuildIncr(IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t),
		Run: runner.run, MountSmoke: skipSmoke,
	})
	if err == nil || !strings.Contains(err.Error(), "not enough free space") {
		t.Fatalf("err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran: %v", runner.calls)
	}
}

func TestBuildIncrBlocksStopsWhenSpaceIsShort(t *testing.T) {
	cfg := buildConfig(t)
	cfg.SplitSize = 24
	seedBlocksActive(t, cfg, []byte("base-blocks-image-bytes!!"), 24)
	stubSpace(t, 0, nil)
	runner := &fakeRunner{body: []byte("should-not-pack")}
	_, err := BuildIncr(IncrRequest{
		Config: cfg, Name: "alice/app", Upper: upperDir(t),
		Run: runner.run, MountSmoke: skipSmoke,
	})
	if err == nil || !strings.Contains(err.Error(), "not enough free space") {
		t.Fatalf("err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran: %v", runner.calls)
	}
}
