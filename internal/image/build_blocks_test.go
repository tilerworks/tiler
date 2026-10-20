package image

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
)

func blocksConfig(t *testing.T) config.Config {
	t.Helper()
	return buildConfig(t)
}

func blocksRequest(cfg config.Config, src string, run ToolRunner, blockSize int64) FullRequest {
	return FullRequest{
		Config:    cfg,
		Name:      "alice/app",
		SrcDir:    src,
		Builder:   "test",
		Now:       fixedNow,
		Run:       run,
		BlockSize: blockSize,
	}
}

func TestBuildFullSplitSizeSelectsBlocks(t *testing.T) {
	cfg := blocksConfig(t)
	cfg.SplitSize = 24
	src := sourceDir(t)
	body := []byte(strings.Repeat("0123456789abcdef", 4))
	res, err := BuildFull(FullRequest{
		Config:  cfg,
		Name:    "alice/app",
		SrcDir:  src,
		Builder: "test",
		Now:     fixedNow,
		Run:     (&fakeRunner{body: body}).run,
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
	if len(loaded.Blocks) != 3 {
		t.Fatalf("SplitSize blocks = %+v, want 3", loaded.Blocks)
	}
	blocksDir, err := repo.BlocksDirPath(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != blocksDir {
		t.Fatalf("path = %s, want %s", res.Path, blocksDir)
	}
	if _, err := os.Stat(repo.ImagePath()); !os.IsNotExist(err) {
		t.Fatalf("SplitSize build left image.sqsh: %v", err)
	}
}

func TestBuildFullZeroSplitSizeSingleFile(t *testing.T) {
	cfg := blocksConfig(t)
	if cfg.SplitSize != 0 {
		t.Fatalf("split size = %d, want 0", cfg.SplitSize)
	}
	src := sourceDir(t)
	body := []byte("single-file-payload")
	res, err := BuildFull(FullRequest{
		Config:  cfg,
		Name:    "alice/app",
		SrcDir:  src,
		Builder: "test",
		Now:     fixedNow,
		Run:     (&fakeRunner{body: body}).run,
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != repo.ImagePath() {
		t.Fatalf("path = %s, want %s", res.Path, repo.ImagePath())
	}
	if _, err := os.Stat(repo.ImagePath()); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Blocks) != 0 {
		t.Fatalf("single-file meta has blocks: %+v", loaded.Blocks)
	}
}

func TestBuildBlocksPublishesSplitPayload(t *testing.T) {
	cfg := blocksConfig(t)
	src := sourceDir(t)
	body := []byte(strings.Repeat("0123456789abcdef", 4))
	runner := &fakeRunner{body: body}
	res, err := BuildFull(blocksRequest(cfg, src, runner.run, 24))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	blocksDir, err := repo.BlocksDirPath(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != blocksDir {
		t.Fatalf("result path = %s, want %s", res.Path, blocksDir)
	}
	whole := sha256.Sum256(body)
	if res.ID != ID(whole) || res.SHA256 != hex.EncodeToString(whole[:]) {
		t.Fatalf("result digest = %s %s", res.ID, res.SHA256)
	}
	parts := [][]byte{body[0:24], body[24:48], body[48:64]}
	for i, part := range parts {
		assertBytes(t, filepath.Join(blocksDir, fmt.Sprintf(blockNameFmt, i)), part)
	}
	view, err := os.ReadFile(filepath.Join(blocksDir, fileConcatView))
	if err != nil {
		t.Fatal(err)
	}
	if string(view) != "blk.000\nblk.001\nblk.002\n" {
		t.Fatalf("concat view = %q", view)
	}
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Blocks) != len(parts) {
		t.Fatalf("manifest = %+v", loaded.Blocks)
	}
	for i, entry := range loaded.Blocks {
		sum := sha256.Sum256(parts[i])
		if entry.Name != fmt.Sprintf(blockNameFmt, i) ||
			entry.Size != int64(len(parts[i])) ||
			entry.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("blocks[%d] = %+v", i, entry)
		}
		info, err := os.Stat(filepath.Join(blocksDir, entry.Name))
		if err != nil {
			t.Fatal(err)
		}
		if entry.MtimeUnix != info.ModTime().Unix() {
			t.Fatalf("blocks[%d].mtime = %d", i, entry.MtimeUnix)
		}
	}
	if loaded.Size != int64(len(body)) || loaded.SHA256 != res.SHA256 ||
		loaded.ID != res.ID {
		t.Fatalf("meta payload = %+v", loaded)
	}
	if loaded.Kind != KindFull || len(loaded.Chain) != 0 ||
		!loaded.CreatedAt.Equal(fixedNow()) || loaded.Builder != "test" {
		t.Fatalf("meta provenance = %+v", loaded)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %v", runner.calls)
	}
	call := runner.calls[0]
	want := []string{"mksquashfs", src, call[2], "-comp", "zstd", "-noappend"}
	if !equalArgs(call, want) {
		t.Fatalf("runner call = %q", call)
	}
	staging := filepath.Dir(call[2])
	if filepath.Base(call[2]) != fileImage ||
		filepath.Dir(staging) != cfg.CacheDir ||
		!strings.HasPrefix(filepath.Base(staging), "build-") {
		t.Fatalf("staging output %s is outside the cache staging dir", call[2])
	}
	assertNoStaging(t, cfg.CacheDir)
	assertNoTemps(t, repo.Dir)
}

func TestBuildBlocksBoundaryShapes(t *testing.T) {
	cases := []struct {
		name      string
		size      int
		blockSize int64
		wantSizes []int64
	}{
		{"exact multiple", 32, 16, []int64{16, 16}},
		{"remainder", 34, 16, []int64{16, 16, 2}},
		{"single block", 10, 16, []int64{10}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := blocksConfig(t)
			body := make([]byte, tc.size)
			for i := range body {
				body[i] = byte('a' + i%26)
			}
			res, err := BuildFull(blocksRequest(cfg, sourceDir(t),
				(&fakeRunner{body: body}).run, tc.blockSize))
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
			if len(loaded.Blocks) != len(tc.wantSizes) {
				t.Fatalf("manifest = %+v", loaded.Blocks)
			}
			for i, want := range tc.wantSizes {
				if loaded.Blocks[i].Size != want {
					t.Fatalf("blocks[%d].size = %d, want %d",
						i, loaded.Blocks[i].Size, want)
				}
			}
			whole := sha256.Sum256(body)
			if res.ID != ID(whole) || loaded.Size != int64(tc.size) {
				t.Fatalf("whole = %s size = %d", res.ID, loaded.Size)
			}
			assertNoTemps(t, repo.Dir)
			assertNoStaging(t, cfg.CacheDir)
		})
	}
}

func TestBuildBlocksRefusesCapacityExceeded(t *testing.T) {
	cfg := blocksConfig(t)
	body := make([]byte, maxBlocks+1)
	copy(body, strings.Repeat("x", maxBlocks+1))
	_, err := BuildFull(blocksRequest(cfg, sourceDir(t),
		(&fakeRunner{body: body}).run, 1))
	if err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("err = %v", err)
	}
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(repo.MetaPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("active meta err = %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(repo.Dir, dirBlocks)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("blocks dir err = %v", statErr)
	}
	assertNoTemps(t, repo.Dir)
	assertNoStaging(t, cfg.CacheDir)
}

func TestBuildBlocksCleansUpOnPackFailure(t *testing.T) {
	cfg := blocksConfig(t)
	runner := &fakeRunner{err: errors.New("mksquashfs: exit status 1: boom")}
	_, err := BuildFull(blocksRequest(cfg, sourceDir(t), runner.run, 16))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	assertNoStaging(t, cfg.CacheDir)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(repo.MetaPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("active meta err = %v", statErr)
	}
	if _, statErr := os.Stat(repo.Dir); statErr == nil {
		assertNoTemps(t, repo.Dir)
	}
}

func TestBuildBlocksCleansUpOnPublishFailure(t *testing.T) {
	cfg := blocksConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	// A hold whose meta cannot be parsed makes the publish
	// preparation fail after the payload was already split.
	hold, err := repo.HoldPath("0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hold, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hold, fileMeta), []byte("not: [yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo.Dir, dirIncr), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("publish-failure-body")
	_, err = BuildFull(blocksRequest(cfg, sourceDir(t),
		(&fakeRunner{body: body}).run, 16))
	if err == nil {
		t.Fatal("expected publish failure")
	}
	if _, statErr := os.Stat(repo.MetaPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("active meta err = %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(repo.Dir, dirBlocks)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("blocks dir err = %v", statErr)
	}
	assertNoTemps(t, repo.Dir)
	assertNoStaging(t, cfg.CacheDir)
}

func TestBuildBlocksGuidesOnToolSizeLimit(t *testing.T) {
	cfg := blocksConfig(t)
	runner := &fakeRunner{err: errors.New(
		"mksquashfs: exit status 1: FATAL ERROR: File too large for filesystem")}
	_, err := BuildFull(blocksRequest(cfg, sourceDir(t), runner.run, 16))
	if err == nil {
		t.Fatal("expected failure")
	}
	assertSplitGuidance(t, err)
	assertNoStaging(t, cfg.CacheDir)
}

func TestBuildFullSingleFormGuidesOnToolSizeLimit(t *testing.T) {
	cfg := buildConfig(t)
	runner := &fakeRunner{err: errors.New("mksquashfs: exit status 1: File too large")}
	_, err := BuildFull(FullRequest{
		Config: cfg, Name: "alice/app", SrcDir: sourceDir(t),
		Builder: "test", Run: runner.run,
	})
	if err == nil {
		t.Fatal("expected failure")
	}
	assertSplitGuidance(t, err)
}

func TestBuildFullPlainToolFailureKeepsMeaning(t *testing.T) {
	for _, out := range []string{
		"mksquashfs: exit status 1: No space left on device",
		"mksquashfs: exit status 1: Invalid argument",
	} {
		cfg := buildConfig(t)
		runner := &fakeRunner{err: errors.New(out)}
		_, err := BuildFull(FullRequest{
			Config: cfg, Name: "alice/app", SrcDir: sourceDir(t),
			Builder: "test", Run: runner.run,
		})
		if err == nil {
			t.Fatalf("expected failure for %q", out)
		}
		if strings.Contains(err.Error(), "split_size") {
			t.Fatalf("%q gained split guidance: %v", out, err)
		}
	}
}

func TestToolOutputHitsSizeLimit(t *testing.T) {
	hits := []string{
		"File too large",
		"file too large",
		"FATAL ERROR: Value too large for defined data type",
		"write: Result too large",
	}
	misses := []string{
		"",
		"No space left on device",
		"Invalid argument",
		"mksquashfs: exit status 1: boom",
	}
	for _, out := range hits {
		if !toolOutputHitsSizeLimit(out) {
			t.Errorf("missed %q", out)
		}
	}
	for _, out := range misses {
		if toolOutputHitsSizeLimit(out) {
			t.Errorf("false hit %q", out)
		}
	}
}

func TestSizeLimitWriteClassification(t *testing.T) {
	if isSizeLimitWrite(errors.New("plain failure")) {
		t.Fatal("plain error classified as size limit")
	}
	err := failWrite("/store/alice/app/blk.000", syscall.ENOSPC)
	if strings.Contains(err.Error(), "split_size") {
		t.Fatalf("ENOSPC gained split guidance: %v", err)
	}
	if !strings.Contains(err.Error(), "/store/alice/app/blk.000") {
		t.Fatalf("write not named: %v", err)
	}
	err = failWrite("/store/alice/app/blk.000", syscall.EINVAL)
	if strings.Contains(err.Error(), "split_size") {
		t.Fatalf("EINVAL gained split guidance: %v", err)
	}
	if runtime.GOOS != "linux" {
		return
	}
	err = failWrite("/store/alice/app/blk.000",
		fmt.Errorf("write block: %w", syscall.EFBIG))
	for _, want := range []string{
		"split_size", "--split", "doctor", "/store/alice/app/blk.000",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

func assertSplitGuidance(t *testing.T, err error) {
	t.Helper()
	text := err.Error()
	for _, want := range []string{
		"refused a file this large",
		"split_size",
		"--split",
		"doctor",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
	for _, banned := range []string{
		"chapter", "page ", "R6",
		"split_enabled", "split_mode", "split_route",
	} {
		if strings.Contains(text, banned) {
			t.Errorf("guidance cites removed or external text (%q): %s", banned, text)
		}
	}
}

func assertNoStaging(t *testing.T, runtimeDir string) {
	t.Helper()
	entries, err := os.ReadDir(runtimeDir)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "build-") {
			t.Errorf("staging dir left behind: %s", entry.Name())
		}
	}
}

func TestBuildBlocksPacksRequestedFormat(t *testing.T) {
	cfg := blocksConfig(t)
	src := sourceDir(t)
	body := []byte(strings.Repeat("0123456789abcdef", 2))
	runner := &fakeRunner{body: body}
	req := blocksRequest(cfg, src, runner.run, 16)
	req.Format = FormatErofs
	req.Compression = "gzip"
	res, err := BuildFull(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %v", runner.calls)
	}
	call := runner.calls[0]
	if call[0] != "mkfs.erofs" || call[3] != "-zdeflate" || call[len(call)-1] != src {
		t.Fatalf("runner call = %q", call)
	}
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Format.Type != FormatErofs || loaded.Format.Compression != "gzip" ||
		loaded.ID != res.ID || len(loaded.Blocks) == 0 {
		t.Fatalf("meta = %+v", loaded)
	}
}
