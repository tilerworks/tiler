package image

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
)

func fixedNow() time.Time {
	return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
}

func buildConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Defaults(t.TempDir())
}

type fakeRunner struct {
	calls [][]string
	envs  [][]string
	body  []byte
	err   error
}

func (f *fakeRunner) run(name string, env []string, args ...string) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	f.envs = append(f.envs, env)
	if f.err != nil {
		return f.err
	}
	dst, err := fakePackDst(name, args)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, f.body, 0o644)
}

// fakePackDst is the image file the packer would create. mksquashfs
// takes source then destination. mkfs.erofs takes -U and the nil
// UUID, the compression flag, the destination, then the source.
func fakePackDst(name string, args []string) (string, error) {
	idx := 1
	if name == "mkfs.erofs" {
		idx = 3
	}
	if len(args) <= idx {
		return "", errors.New("fake runner: missing destination")
	}
	return args[idx], nil
}

func sourceDir(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

func TestBuildFullPublishesPackedImage(t *testing.T) {
	cfg := buildConfig(t)
	src := sourceDir(t)
	body := []byte("packed-full-image")
	runner := &fakeRunner{body: body}
	req := FullRequest{
		Config:  cfg,
		Name:    "alice/app",
		SrcDir:  src,
		Version: "v1.2.3",
		Source: Source{
			Repo:   "https://example.com/alice/app.git",
			Ref:    "main",
			Commit: "0123abc",
		},
		Builder: "test",
		Now:     fixedNow,
		Run:     runner.run,
	}
	res, err := BuildFull(req)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), body)
	if res.Path != repo.ImagePath() {
		t.Fatalf("result path = %s", res.Path)
	}
	sum := sha256.Sum256(body)
	if res.ID != ID(sum) || res.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("result digest = %s %s", res.ID, res.SHA256)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %v", runner.calls)
	}
	call := runner.calls[0]
	want := []string{"mksquashfs", src, call[2], "-comp", "zstd", "-noappend"}
	if !equalArgs(call, want) {
		t.Fatalf("runner call = %q", call)
	}
	if filepath.Dir(call[2]) != repo.Dir || !strings.HasPrefix(filepath.Base(call[2]), ".") {
		t.Fatalf("temp output %s is outside the repository dir", call[2])
	}
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(repo.ImagePath())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "alice/app" || loaded.Version != "v1.2.3" {
		t.Fatalf("meta identity = %+v", loaded)
	}
	if loaded.Kind != KindFull || loaded.Format.Type != FormatSquashfs ||
		loaded.Format.Compression != CompressionZstd {
		t.Fatalf("meta format = %+v", loaded)
	}
	if loaded.Size != int64(len(body)) || loaded.SHA256 != hex.EncodeToString(sum[:]) ||
		loaded.ID != ID(sum) || loaded.MtimeUnix != info.ModTime().Unix() {
		t.Fatalf("meta payload = %+v", loaded)
	}
	if loaded.Source != req.Source || loaded.Builder != "test" ||
		!loaded.CreatedAt.Equal(fixedNow()) || len(loaded.Chain) != 0 {
		t.Fatalf("meta provenance = %+v", loaded)
	}
	assertNoTemps(t, repo.Dir)
}

func equalArgs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestBuildFullRejectsInvalidName(t *testing.T) {
	cfg := buildConfig(t)
	_, err := BuildFull(FullRequest{
		Config: cfg, Name: "_foo", SrcDir: sourceDir(t), Builder: "test",
	})
	if err == nil {
		t.Fatal("expected name error")
	}
}

func TestBuildFullRefusesReservedSource(t *testing.T) {
	cfg := buildConfig(t)
	src := filepath.Join(cfg.ImagesDir, "inside")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{body: []byte("never-packed")}
	_, err := BuildFull(FullRequest{
		Config: cfg, Name: "alice/app", SrcDir: src, Builder: "test", Run: runner.run,
	})
	if err == nil || !errors.Is(err, config.ErrReservedPath) {
		t.Fatalf("err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran on a reserved source: %v", runner.calls)
	}
}

func TestBuildFullRefusesCoveringSource(t *testing.T) {
	home := t.TempDir()
	cfg := config.Defaults(home)
	if err := os.MkdirAll(cfg.ImagesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{body: []byte("never-packed")}
	_, err := BuildFull(FullRequest{
		Config: cfg, Name: "alice/app", SrcDir: home, Builder: "test", Run: runner.run,
	})
	if err == nil || !errors.Is(err, config.ErrReservedPath) ||
		!strings.Contains(err.Error(), "would cover reserved") {
		t.Fatalf("err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran on a covering source: %v", runner.calls)
	}
}

func TestBuildFullRefusesSymlinkIntoReserved(t *testing.T) {
	cfg := buildConfig(t)
	if err := os.MkdirAll(cfg.ImagesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	link := filepath.Join(work, "via-images")
	if err := os.Symlink(cfg.ImagesDir, link); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{body: []byte("never-packed")}
	_, err := BuildFull(FullRequest{
		Config: cfg, Name: "alice/app", SrcDir: link, Builder: "test", Run: runner.run,
	})
	if err == nil || !errors.Is(err, config.ErrReservedPath) {
		t.Fatalf("err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran on a symlink into reserved: %v", runner.calls)
	}
}

func TestBuildFullRefusesSymlinkCoveringReserved(t *testing.T) {
	home := t.TempDir()
	cfg := config.Defaults(home)
	if err := os.MkdirAll(cfg.ImagesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasRoot := t.TempDir()
	link := filepath.Join(aliasRoot, "cover")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{body: []byte("never-packed")}
	_, err := BuildFull(FullRequest{
		Config: cfg, Name: "alice/app", SrcDir: link, Builder: "test", Run: runner.run,
	})
	if err == nil || !errors.Is(err, config.ErrReservedPath) ||
		!strings.Contains(err.Error(), "would cover reserved") {
		t.Fatalf("err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran on a covering symlink: %v", runner.calls)
	}
}

func TestBuildFullRefusesMissingSource(t *testing.T) {
	cfg := buildConfig(t)
	_, err := BuildFull(FullRequest{
		Config:  cfg,
		Name:    "alice/app",
		SrcDir:  filepath.Join(t.TempDir(), "absent"),
		Builder: "test",
	})
	if err == nil {
		t.Fatal("expected missing source error")
	}
}

func TestBuildFullRefusesFileSource(t *testing.T) {
	cfg := buildConfig(t)
	src := filepath.Join(t.TempDir(), "plain-file")
	if err := os.WriteFile(src, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{body: []byte("never-packed")}
	_, err := BuildFull(FullRequest{
		Config: cfg, Name: "alice/app", SrcDir: src, Builder: "test", Run: runner.run,
	})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran on a file source: %v", runner.calls)
	}
}

func TestBuildFullNonGitSourceSkipsScene(t *testing.T) {
	cfg := buildConfig(t)
	src := sourceDir(t)
	scene := func(dir string) error {
		t.Fatalf("scene ran on a non-git source %s", dir)
		return nil
	}
	_, err := BuildFull(FullRequest{
		Config: cfg, Name: "alice/app", SrcDir: src, Builder: "test",
		Run: (&fakeRunner{body: []byte("packed")}).run, PrepareScene: scene,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBuildFullGitSourceRunsScene(t *testing.T) {
	cfg := buildConfig(t)
	src := gitSourceDir(t)
	var seen []string
	scene := func(dir string) error {
		seen = append(seen, dir)
		return nil
	}
	_, err := BuildFull(FullRequest{
		Config: cfg, Name: "alice/app", SrcDir: src, Builder: "test",
		Run: (&fakeRunner{body: []byte("packed")}).run, PrepareScene: scene,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != src {
		t.Fatalf("scene calls = %v", seen)
	}
}

func TestBuildFullSceneFailureAbortsBeforePacking(t *testing.T) {
	cfg := buildConfig(t)
	src := gitSourceDir(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{body: []byte("never-packed")}
	scene := func(string) error { return errors.New("git: fatal: index write failed") }
	_, err = BuildFull(FullRequest{
		Config: cfg, Name: "alice/app", SrcDir: src, Builder: "test",
		Run: runner.run, PrepareScene: scene,
	})
	if err == nil || !strings.Contains(err.Error(), "index write failed") {
		t.Fatalf("err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran despite git failure: %v", runner.calls)
	}
	if _, statErr := os.Stat(repo.ImagePath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("active image err = %v", statErr)
	}
}

func gitSourceDir(t *testing.T) string {
	t.Helper()
	src := sourceDir(t)
	if err := os.MkdirAll(filepath.Join(src, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return src
}

func TestBuildFullPackerFailureLeavesNoActive(t *testing.T) {
	cfg := buildConfig(t)
	src := sourceDir(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{err: errors.New("mksquashfs: exit status 1: boom")}
	_, err = BuildFull(FullRequest{
		Config: cfg, Name: "alice/app", SrcDir: src, Builder: "test", Run: runner.run,
	})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	partial := func(_ string, _ []string, args ...string) error {
		if writeErr := os.WriteFile(args[1], []byte("partial"), 0o644); writeErr != nil {
			return writeErr
		}
		return errors.New("mksquashfs: exit status 1")
	}
	_, err = BuildFull(FullRequest{
		Config: cfg, Name: "alice/app", SrcDir: src, Builder: "test", Run: partial,
	})
	if err == nil {
		t.Fatal("expected packer failure")
	}
	if _, statErr := os.Stat(repo.ImagePath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("active image err = %v", statErr)
	}
	if _, statErr := os.Stat(repo.MetaPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("active meta err = %v", statErr)
	}
	assertNoTemps(t, repo.Dir)
}

func TestBuildFullRejectsEmptyBuilder(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	_, err = BuildFull(FullRequest{
		Config: cfg, Name: "alice/app", SrcDir: sourceDir(t),
		Run: (&fakeRunner{body: []byte("packed")}).run,
	})
	if err == nil || !strings.Contains(err.Error(), "builder") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(repo.ImagePath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("active image err = %v", statErr)
	}
	assertNoTemps(t, repo.Dir)
}

func TestBuildFullSameBytesIsIdle(t *testing.T) {
	cfg := buildConfig(t)
	src := sourceDir(t)
	body := []byte("stable-image-bytes")
	req := FullRequest{
		Config: cfg, Name: "alice/app", SrcDir: src, Builder: "test",
		Now: fixedNow, Run: (&fakeRunner{body: body}).run,
	}
	first, err := BuildFull(req)
	if err != nil {
		t.Fatal(err)
	}
	later := func() time.Time { return fixedNow().Add(time.Hour) }
	req.Now = later
	req.Run = (&fakeRunner{body: body}).run
	second, err := BuildFull(req)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.SHA256 != first.SHA256 {
		t.Fatalf("rebuild digest = %s, want %s", second.ID, first.ID)
	}
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{dirArchives, dirHold} {
		if _, statErr := os.Stat(filepath.Join(repo.Dir, dir)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%s err = %v", dir, statErr)
		}
	}
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.CreatedAt.Equal(fixedNow()) {
		t.Fatalf("idle rebuild rewrote meta: %+v", loaded)
	}
	assertNoTemps(t, repo.Dir)
}

func TestBuildFullSameBytesWithChainRebuilds(t *testing.T) {
	cfg := buildConfig(t)
	src := sourceDir(t)
	body := []byte("chained-image-bytes")
	first, err := BuildFull(FullRequest{
		Config: cfg, Name: "alice/app", SrcDir: src, Builder: "test",
		Now: fixedNow, Run: (&fakeRunner{body: body}).run,
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
	loaded.Chain = []ChainEntry{validEntry(loaded.ID, "fedcba9876543210")}
	if err := Store(repo.MetaPath(), loaded); err != nil {
		t.Fatal(err)
	}
	second, err := BuildFull(FullRequest{
		Config: cfg, Name: "alice/app", SrcDir: src, Builder: "test",
		Now: fixedNow, Run: (&fakeRunner{body: body}).run,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("rebuild id = %s, want %s", second.ID, first.ID)
	}
	arch, err := repo.ArchivePath(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(arch, fileImage), body)
	archived, err := Load(filepath.Join(arch, fileMeta))
	if err != nil {
		t.Fatal(err)
	}
	if len(archived.Chain) != 1 {
		t.Fatalf("archived meta = %+v", archived)
	}
	active, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(active.Chain) != 0 {
		t.Fatalf("active meta = %+v", active)
	}
	if _, statErr := os.Stat(filepath.Join(repo.Dir, dirHold)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("hold left behind: %v", statErr)
	}
	assertNoTemps(t, repo.Dir)
}

func TestBuildFullRealToolsMatchSourceListing(t *testing.T) {
	if _, err := exec.LookPath("mksquashfs"); err != nil {
		t.Skip("mksquashfs is not available")
	}
	if _, err := exec.LookPath("unsquashfs"); err != nil {
		t.Skip("unsquashfs is not available")
	}
	cfg := buildConfig(t)
	src := t.TempDir()
	files := map[string]string{
		"hello.txt":        "hello",
		"dir/nested.bin":   "nested-bytes",
		"dir/deep/one.yml": "key: value\n",
	}
	for rel, body := range files {
		path := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := BuildFull(FullRequest{
		Config: cfg, Name: "alice/app", SrcDir: src, Builder: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("unsquashfs", "-ls", res.Path).CombinedOutput()
	if err != nil {
		t.Fatalf("unsquashfs: %v: %s", err, out)
	}
	listed := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		rel := strings.TrimPrefix(strings.TrimSpace(line), "squashfs-root")
		rel = strings.TrimPrefix(rel, "/")
		if rel != "" {
			listed[rel] = true
		}
	}
	for rel := range files {
		if !listed[rel] {
			t.Errorf("image listing is missing %s", rel)
		}
	}
}

func TestVerifySHA256DetectsTampering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "image.sqsh")
	data := []byte("verified-image")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	meta := metaFor(data)
	if err := VerifySHA256(meta, path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("verifIed-image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifySHA256(meta, path); err == nil {
		t.Fatal("expected digest mismatch")
	}
	if err := VerifySHA256(meta, filepath.Join(dir, "absent")); err == nil {
		t.Fatal("expected missing file error")
	}
}

func TestQuickCheckDetectsChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "image.sqsh")
	data := []byte("quick-check-image")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	meta := metaFor(data)
	meta.MtimeUnix = info.ModTime().Unix()
	if err := QuickCheck(meta, path); err != nil {
		t.Fatal(err)
	}
	wrongSize := meta
	wrongSize.Size++
	if err := QuickCheck(wrongSize, path); err == nil {
		t.Fatal("expected size mismatch")
	}
	wrongMtime := meta
	wrongMtime.MtimeUnix++
	if err := QuickCheck(wrongMtime, path); err == nil {
		t.Fatal("expected mtime mismatch")
	}
}

func TestTailOutputTruncatesLongOutput(t *testing.T) {
	long := bytes.Repeat([]byte("x"), toolOutputLimit+100)
	tail := tailOutput(long)
	if len(tail) > toolOutputLimit+len("... ") {
		t.Fatalf("tail length = %d", len(tail))
	}
	if !strings.HasPrefix(tail, "... ") {
		t.Fatal("truncated output keeps no marker")
	}
	if short := tailOutput([]byte("  short \n")); short != "short" {
		t.Fatalf("short output = %q", short)
	}
}

// epochTree is a source tree whose newest mtime is the given Unix
// second, with any inherited SOURCE_DATE_EPOCH cleared so the
// derivation path runs.
func epochTree(t *testing.T, unix int64) string {
	t.Helper()
	t.Setenv("SOURCE_DATE_EPOCH", "")
	src := t.TempDir()
	file := filepath.Join(src, "a.txt")
	if err := os.WriteFile(file, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(unix, 0)
	if err := os.Chtimes(file, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	// The directory mtime participates in the derivation, so pin it
	// after the write bumped it.
	if err := os.Chtimes(src, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return src
}

func TestPackPathsExportSourceDateEpoch(t *testing.T) {
	src := epochTree(t, 1700000000)
	runner := &fakeRunner{body: []byte("packed")}
	dst := filepath.Join(t.TempDir(), "out.sqsh")
	if err := packFullImage(runner.run, src, dst, FormatSquashfs, CompressionZstd); err != nil {
		t.Fatal(err)
	}
	if err := packIncrImage(runner.run, src, dst+".incr", FormatSquashfs, CompressionZstd); err != nil {
		t.Fatal(err)
	}
	want := "SOURCE_DATE_EPOCH=1700000000"
	if len(runner.envs) != 2 {
		t.Fatalf("envs = %v", runner.envs)
	}
	for i, env := range runner.envs {
		if len(env) != 1 || env[0] != want {
			t.Fatalf("pack %d env = %v, want [%s]", i, env, want)
		}
	}
}

func TestBuildFullPacksRequestedContainer(t *testing.T) {
	src := sourceDir(t)
	cases := []struct {
		name        string
		format      string
		compression string
		wantName    string
		wantComp    string
		metaType    string
		metaComp    string
	}{
		{
			name: "explicit squashfs zstd", format: FormatSquashfs, compression: CompressionZstd,
			wantName: "mksquashfs", wantComp: "zstd",
			metaType: FormatSquashfs, metaComp: CompressionZstd,
		},
		{
			name: "squashfs xz", format: FormatSquashfs, compression: "xz",
			wantName: "mksquashfs", wantComp: "xz",
			metaType: FormatSquashfs, metaComp: "xz",
		},
		{
			name: "default type with gzip", compression: "gzip",
			wantName: "mksquashfs", wantComp: "gzip",
			metaType: FormatSquashfs, metaComp: "gzip",
		},
		{
			name: "erofs gzip", format: FormatErofs, compression: "gzip",
			wantName: "mkfs.erofs", wantComp: "-zdeflate",
			metaType: FormatErofs, metaComp: "gzip",
		},
		{
			name: "erofs default compression", format: FormatErofs,
			wantName: "mkfs.erofs", wantComp: "-zzstd",
			metaType: FormatErofs, metaComp: CompressionZstd,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := buildConfig(t)
			runner := &fakeRunner{body: []byte("packed-" + tc.name)}
			res, err := BuildFull(FullRequest{
				Config: cfg, Name: "alice/app", SrcDir: src, Builder: "test",
				Format: tc.format, Compression: tc.compression,
				Now: fixedNow, Run: runner.run,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(runner.calls) != 1 {
				t.Fatalf("runner calls = %v", runner.calls)
			}
			call := runner.calls[0]
			// The compressor is its own argv element. A substring of
			// the joined command also matches the temp directory,
			// whose name keeps this subtest's compressor word.
			var want []string
			switch tc.wantName {
			case "mksquashfs":
				want = []string{"mksquashfs", src, call[2], "-comp", tc.wantComp, "-noappend"}
			case "mkfs.erofs":
				want = []string{"mkfs.erofs", "-U", "00000000-0000-0000-0000-000000000000", tc.wantComp, call[4], src}
			default:
				t.Fatalf("unexpected packer %s", tc.wantName)
			}
			if !equalArgs(call, want) {
				t.Fatalf("runner call = %q, want %q", call, want)
			}
			repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(repo.MetaPath())
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Format.Type != tc.metaType || loaded.Format.Compression != tc.metaComp ||
				loaded.ID != res.ID {
				t.Fatalf("meta format = %+v id %s", loaded.Format, loaded.ID)
			}
		})
	}
}

func TestBuildFullRejectsBadContainer(t *testing.T) {
	cases := []struct {
		name        string
		format      string
		compression string
		want        string
	}{
		{name: "unknown type", format: "tar", compression: "zstd", want: "unknown container type"},
		{name: "erofs xz", format: FormatErofs, compression: "xz", want: "not allowed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{body: []byte("nope")}
			_, err := BuildFull(FullRequest{
				Config: buildConfig(t), Name: "alice/app", SrcDir: sourceDir(t),
				Builder: "test", Format: tc.format, Compression: tc.compression,
				Run: runner.run,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("packer ran: %v", runner.calls)
			}
		})
	}
}

func TestBuildFullStopsWhenSpaceIsShort(t *testing.T) {
	stubSpace(t, 0, nil)
	runner := &fakeRunner{body: []byte("should-not-pack")}
	_, err := BuildFull(FullRequest{
		Config: buildConfig(t), Name: "alice/app", SrcDir: sourceDir(t),
		Builder: "test", Run: runner.run,
	})
	if err == nil || !strings.Contains(err.Error(), "not enough free space") {
		t.Fatalf("err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran: %v", runner.calls)
	}
}
