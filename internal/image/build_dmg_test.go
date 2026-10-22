package image

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/container"
	"github.com/jiangxin/tiler/internal/workspace"
)

type dmgRunner struct {
	body  []byte
	err   error
	calls [][]string
	// afterConvertShadow, when set, is written to the -shadow path
	// after a successful convert, mimicking hdiutil rewriting the
	// live shadow.
	afterConvertShadow []byte
}

func (d *dmgRunner) run(name string, _ []string, args ...string) error {
	d.calls = append(d.calls, append([]string{name}, args...))
	if d.body != nil {
		if err := os.WriteFile(dmgArgOut(args), d.body, 0o644); err != nil {
			return err
		}
	}
	if d.err == nil && d.afterConvertShadow != nil {
		if shadow := dmgArgShadow(args); shadow != "" {
			if err := os.WriteFile(shadow, d.afterConvertShadow, 0o644); err != nil {
				return err
			}
		}
	}
	return d.err
}

func dmgArgOut(args []string) string {
	for i, arg := range args {
		if arg == "-o" && i+1 < len(args) {
			return args[i+1]
		}
	}
	if len(args) == 0 {
		return ""
	}
	return args[len(args)-1]
}

func dmgArgShadow(args []string) string {
	for i, arg := range args {
		if arg == "-shadow" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func writeShadow(t *testing.T, sessions, name string, seq int, body []byte) string {
	t.Helper()
	shadow, err := container.DMGShadowPath(sessions, name, seq)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return shadow
}

func TestBuildDMGDirectoryUsesSrcfolder(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	src := sourceDir(t)
	body := []byte("created-dmg")
	runner := &dmgRunner{body: body}
	res, err := BuildDMG(DMGRequest{
		Config: cfg, Name: "alice/app", SrcDir: src,
		Compression: "ulmo", Builder: "test", Run: runner.run,
		Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %q", runner.calls)
	}
	temp := dmgArgOut(runner.calls[0][1:])
	want, err := container.HdiutilCreateArgv(src, "ulmo", "alice/app", temp)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(runner.calls[0], want) {
		t.Fatalf("argv = %q, want %q", runner.calls[0], want)
	}
	if strings.Contains(strings.Join(runner.calls[0], " "), "-shadow") {
		t.Fatalf("directory build used a shadow: %q", runner.calls[0])
	}
	if _, statErr := os.Stat(temp); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temp survived: %v", statErr)
	}
	assertBytes(t, filepath.Join(repo.Dir, ImageDMG), body)
	if _, statErr := os.Stat(repo.ImagePath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("directory build wrote image.sqsh")
	}
	meta, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != res.ID || meta.Format.Type != FormatDMG || meta.Format.Compression != "ulmo" ||
		len(meta.Chain) != 0 || meta.SHA256 != res.SHA256 {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestBuildDMGEmptyCompressionIsULFO(t *testing.T) {
	cfg := buildConfig(t)
	src := sourceDir(t)
	runner := &dmgRunner{body: []byte("default-ulfo")}
	if _, err := BuildDMG(DMGRequest{
		Config: cfg, Name: "alice/app", SrcDir: src, Builder: "test", Run: runner.run,
	}); err != nil {
		t.Fatal(err)
	}
	temp := dmgArgOut(runner.calls[0][1:])
	want, err := container.HdiutilCreateArgv(src, "", "alice/app", temp)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(runner.calls[0], want) || !slices.Contains(runner.calls[0], "ULFO") {
		t.Fatalf("argv = %q, want %q", runner.calls[0], want)
	}
}

func TestBakeDMGUsesShadowAndArchivesPrevious(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	oldBody := []byte("old-base")
	old := seedDMGActive(t, repo, oldBody)
	shadowBody := []byte("shadow-bytes")
	shadow := writeShadow(t, cfg.SessionsDir, "dev", 1, shadowBody)
	baked := []byte("baked-dmg")
	runner := &dmgRunner{body: baked}
	res, err := BuildDMG(DMGRequest{
		Config: cfg, Name: "alice/app", Shadow: shadow, Builder: "test", Run: runner.run,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(repo.Dir, ImageDMG)
	temp := dmgArgOut(runner.calls[0][1:])
	want, err := container.HdiutilConvertArgv(base, "", "ulfo", shadow, temp)
	if err != nil {
		t.Fatal(err)
	}
	// Convert reads the active image. Publish then archives that file,
	// so the recorded source path is the active image.dmg.
	if len(runner.calls) != 1 || !slices.Equal(runner.calls[0], want) {
		t.Fatalf("argv = %q, want %q", runner.calls, want)
	}
	assertBytes(t, shadow, shadowBody)
	if _, statErr := os.Stat(temp); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temp survived: %v", statErr)
	}
	assertBytes(t, base, baked)
	arch := filepath.Join(repo.Dir, "archives", old.ID, ImageDMG)
	assertBytes(t, arch, oldBody)
	meta, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != res.ID || meta.ID == old.ID || len(meta.Chain) != 0 ||
		meta.Format.Compression != "ulfo" {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestBakeDMGKeepsReferencedBaseInHold(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	oldBody := []byte("pinned-base")
	old := seedDMGActive(t, repo, oldBody)
	shadow := writeShadow(t, cfg.SessionsDir, "dev", 1, []byte("keep"))
	runner := &dmgRunner{body: []byte("next-dmg")}
	if _, err := BuildDMG(DMGRequest{
		Config: cfg, Name: "alice/app", Shadow: shadow, Builder: "test",
		Refs: holdSet{ids: map[string]bool{old.ID: true}}, Run: runner.run,
	}); err != nil {
		t.Fatal(err)
	}
	hold, err := repo.HoldPath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(hold, ImageDMG), oldBody)
	assertBytes(t, shadow, []byte("keep"))
	if _, statErr := os.Stat(filepath.Join(repo.Dir, "archives", old.ID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("referenced base was archived: %v", statErr)
	}
}

func TestBakeDMGAgainConvertsHeldBase(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	oldBody := []byte("pinned-base")
	old := seedDMGActive(t, repo, oldBody)
	shadowBody := []byte("live-shadow")
	shadow := writeShadow(t, cfg.SessionsDir, "dev", 1, shadowBody)
	pins := map[string]bool{old.ID: true}
	runner := &dmgRunner{body: []byte("first-bake")}
	first := DMGRequest{
		Config: cfg, Name: "alice/app", Shadow: shadow, MountedID: old.ID,
		Builder: "test", Refs: holdSet{ids: pins}, Run: runner.run,
	}
	if _, err := BuildDMG(first); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(repo.Dir, ImageDMG)
	if len(runner.calls) != 1 || runner.calls[0][2] != active {
		t.Fatalf("first argv = %q, want image %s", runner.calls, active)
	}
	// A newer session can pin the image the first bake published.
	// This shadow still belongs to the held base. Change the shadow
	// so the idle fingerprint misses and convert runs again.
	published, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	pins[published.ID] = true
	shadowBody = []byte("live-shadow-again")
	if err := os.WriteFile(shadow, shadowBody, 0o644); err != nil {
		t.Fatal(err)
	}
	runner.body = []byte("second-bake")
	if _, err := BuildDMG(first); err != nil {
		t.Fatal(err)
	}
	hold, err := repo.HoldPath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	held := filepath.Join(hold, ImageDMG)
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %d", len(runner.calls))
	}
	temp := dmgArgOut(runner.calls[1][1:])
	want, err := container.HdiutilConvertArgv(held, "", "ulfo", shadow, temp)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(runner.calls[1], want) {
		t.Fatalf("argv = %q, want %q", runner.calls[1], want)
	}
	assertBytes(t, shadow, shadowBody)
	assertBytes(t, held, oldBody)
}

func TestBakeDMGIdleSkipsConvertWhenShadowUnchanged(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	old := seedDMGActive(t, repo, []byte("old-base"))
	shadow := writeShadow(t, cfg.SessionsDir, "dev", 1, []byte("pre-shadow"))
	postShadow := []byte("post-shadow")
	runner := &dmgRunner{body: []byte("first-bake"), afterConvertShadow: postShadow}
	req := DMGRequest{
		Config: cfg, Name: "alice/app", Shadow: shadow,
		MountedID: old.ID, Builder: "test",
		Refs: holdSet{ids: map[string]bool{old.ID: true}}, Run: runner.run,
	}
	first, err := BuildDMG(req)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	wantContent, err := shadowContentSHA256(shadow)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ContentSHA256 != wantContent {
		t.Fatalf("content_sha256 = %s, want post-convert %s", meta.ContentSHA256, wantContent)
	}
	assertBytes(t, shadow, postShadow)
	runner.body = []byte("must-not-publish")
	runner.afterConvertShadow = []byte("must-not-rewrite")
	second, err := BuildDMG(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("idle bake ran hdiutil again: calls = %q", runner.calls)
	}
	if second.ID != first.ID {
		t.Fatalf("idle id = %s, want %s", second.ID, first.ID)
	}
	assertBytes(t, filepath.Join(repo.Dir, ImageDMG), []byte("first-bake"))
	assertBytes(t, shadow, postShadow)
	if _, err := os.Stat(filepath.Join(repo.Dir, "archives")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("idle bake archived: %v", err)
	}
	// A write into the live shadow must miss the idle fingerprint.
	if err := os.WriteFile(shadow, []byte("user-write"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner.body = []byte("second-bake")
	runner.afterConvertShadow = []byte("post-second")
	third, err := BuildDMG(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("changed shadow skipped convert: calls = %q", runner.calls)
	}
	if third.ID == first.ID {
		t.Fatal("changed shadow reused the idle id")
	}
	assertBytes(t, filepath.Join(repo.Dir, ImageDMG), []byte("second-bake"))
	assertBytes(t, shadow, []byte("post-second"))
}

func TestBuildDMGCreateIdleSkipsNonDeterministicPacker(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	src := sourceDir(t)
	runner := &dmgRunner{body: []byte("first-create")}
	first, err := BuildDMG(DMGRequest{
		Config: cfg, Name: "alice/app", SrcDir: src, Builder: "test", Run: runner.run,
	})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if meta.ContentSHA256 == "" {
		t.Fatal("create left content_sha256 empty")
	}
	before, err := os.ReadFile(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	runner.body = []byte("different-bytes-each-time")
	second, err := BuildDMG(DMGRequest{
		Config: cfg, Name: "alice/app", SrcDir: src, Builder: "test", Run: runner.run,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("idle create ran hdiutil again: calls = %q", runner.calls)
	}
	if second.ID != first.ID {
		t.Fatalf("idle id = %s, want %s", second.ID, first.ID)
	}
	after, err := os.ReadFile(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("idle create rewrote meta")
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, "archives")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("idle create archived: %v", err)
	}
	assertBytes(t, filepath.Join(repo.Dir, ImageDMG), []byte("first-create"))
}

func TestBakeDMGFailureLeavesImageAndShadow(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	oldBody := []byte("still-active")
	old := seedDMGActive(t, repo, oldBody)
	shadowBody := []byte("still-shadow")
	shadow := writeShadow(t, cfg.SessionsDir, "dev", 1, shadowBody)
	runner := &dmgRunner{body: []byte("partial"), err: errors.New("hdiutil: convert failed")}
	_, err = BuildDMG(DMGRequest{
		Config: cfg, Name: "alice/app", Shadow: shadow, Builder: "test", Run: runner.run,
	})
	if err == nil || !strings.Contains(err.Error(), "convert failed") {
		t.Fatalf("err = %v", err)
	}
	assertBytes(t, filepath.Join(repo.Dir, ImageDMG), oldBody)
	assertBytes(t, shadow, shadowBody)
	meta, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != old.ID {
		t.Fatalf("meta id = %s, want %s", meta.ID, old.ID)
	}
	assertNoDMGTemp(t, repo.Dir)
}

func TestBuildDMGCreateFailureLeavesNoImage(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	runner := &dmgRunner{body: []byte("partial"), err: errors.New("hdiutil: create failed")}
	_, err = BuildDMG(DMGRequest{
		Config: cfg, Name: "alice/app", SrcDir: sourceDir(t), Builder: "test", Run: runner.run,
	})
	if err == nil || !strings.Contains(err.Error(), "create failed") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(repo.Dir, ImageDMG)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed create published image.dmg: %v", statErr)
	}
	assertNoDMGTemp(t, repo.Dir)
}

func TestBuildDMGConstructorErrorIsNotExecuted(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	old := seedDMGActive(t, repo, []byte("untouched"))
	runner := &dmgRunner{body: []byte("nope")}
	_, err = BuildDMG(DMGRequest{
		Config: cfg, Name: "alice/app", Shadow: "/tmp/upper.shadow",
		Builder: "test", Run: runner.run,
	})
	if err == nil || len(runner.calls) != 0 {
		t.Fatalf("err = %v calls = %q", err, runner.calls)
	}
	assertBytes(t, filepath.Join(repo.Dir, ImageDMG), []byte("untouched"))
	meta, loadErr := Load(repo.MetaPath())
	if loadErr != nil || meta.ID != old.ID {
		t.Fatalf("meta = %+v %v", meta, loadErr)
	}
}

func TestBakeThenNewMountUsesEmptyShadow(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	seedDMGActive(t, repo, []byte("old-base"))
	oldShadow := writeShadow(t, cfg.SessionsDir, "dev", 1, []byte("live-shadow"))
	runner := &dmgRunner{body: []byte("new-base")}
	res, err := BuildDMG(DMGRequest{
		Config: cfg, Name: "alice/app", Shadow: oldShadow, Builder: "test", Run: runner.run,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(repo.Dir, ImageDMG)
	if got.ID != res.ID || len(got.Members) != 1 || got.Members[0].Path != imagePath {
		t.Fatalf("mount target = %+v", got)
	}
	next := writeShadow(t, cfg.SessionsDir, "reader", 2, nil)
	if err := os.Remove(next); err != nil {
		t.Fatal(err)
	}
	point := filepath.Join(t.TempDir(), "view")
	if err := os.Mkdir(point, 0o755); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	if _, err := workspace.Mount(workspace.Request{
		Full:       imagePath,
		Format:     FormatDMG,
		RuntimeDir: filepath.Dir(next),
		MergedDir:  point,
		Hdiutil: func(argv []string, consErr error) ([]byte, error) {
			if consErr != nil {
				return nil, consErr
			}
			calls = append(calls, append([]string(nil), argv...))
			return nil, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	want, err := container.HdiutilAttachWritableArgv(imagePath, point, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !slices.Equal(calls[0], want) {
		t.Fatalf("attach = %q, want %q", calls, want)
	}
	if _, statErr := os.Stat(next); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("new shadow was created by the bake: %v", statErr)
	}
	assertBytes(t, oldShadow, []byte("live-shadow"))
}

func TestGCDoesNotDeleteSessionShadow(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	retired := makeArchive(t, repo, []byte("old-archive"), time.Now().Add(-time.Hour))
	cfg.MaxAge = time.Minute
	shadow := writeShadow(t, cfg.SessionsDir, "dev", 1, []byte("keep-shadow"))
	events, err := GC(cfg, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != GCActionRemoved || events[0].FullID != retired.ID {
		t.Fatalf("events = %+v", events)
	}
	assertBytes(t, shadow, []byte("keep-shadow"))
}

func TestRemoveRefusesAttachedDMG(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	body := []byte("live-dmg")
	seedDMGActive(t, repo, body)
	calls := 0
	attached := func(dir string) (bool, error) {
		calls++
		if dir != repo.Dir {
			t.Fatalf("attached dir = %s, want %s", dir, repo.Dir)
		}
		return true, nil
	}
	for i := 0; i < 2; i++ {
		outcome, err := RemoveChecked(cfg, "alice/app", nil, attached)
		if err == nil || !strings.Contains(err.Error(), "still attached") {
			t.Fatalf("call %d: outcome = %s err = %v", i, outcome, err)
		}
	}
	if calls != 2 {
		t.Fatalf("attach checks = %d", calls)
	}
	meta, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if meta.Deprecated {
		t.Fatal("attached target was deprecated")
	}
	assertBytes(t, filepath.Join(repo.Dir, ImageDMG), body)
}

func TestRemoveSquashfsDoesNotAskAttach(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	seedActive(t, repo, []byte("sqsh"), false)
	_, err := RemoveChecked(cfg, "alice/app", nil, func(string) (bool, error) {
		t.Fatal("squashfs remove consulted hdiutil info")
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertNoDMGTemp(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".image.dmg.") {
			t.Fatalf("temp left behind: %s", entry.Name())
		}
	}
}
