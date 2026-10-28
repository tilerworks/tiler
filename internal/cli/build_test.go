package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
)

// libFlags points the library directories at a temporary tree under
// home, outside the working directory, so the reserved-tree precheck
// and the tests never interfere.
func libFlags(home string) (imagesDir string, args []string) {
	imagesDir = filepath.Join(home, "lib", "images")
	args = []string{
		"--images-dir", imagesDir,
		"--sessions-dir", filepath.Join(home, "lib", "sessions"),
	}
	return imagesDir, args
}

func sessionsDir(home string) string {
	return filepath.Join(home, "lib", "sessions")
}

// withBuildSeams installs the injected mount table, a fake packer
// that needs no mksquashfs, and a passing mount smoke gate.
func withBuildSeams(t *testing.T, table []mountinfo.Mount) {
	t.Helper()
	origTable, origPack, origSmoke := loadMountTable, packTool, incrSmoke
	origLegacy := legacyBuild
	t.Cleanup(func() {
		loadMountTable, packTool, incrSmoke = origTable, origPack, origSmoke
		legacyBuild = origLegacy
	})
	loadMountTable = func() ([]mountinfo.Mount, error) { return table, nil }
	packTool = fakePacker(t)
	incrSmoke = func(string) error { return nil }
	versionFixture := true
	for skip := 1; skip < 8; skip++ {
		_, file, _, ok := runtime.Caller(skip)
		base := filepath.Base(file)
		if ok && strings.HasSuffix(base, "_test.go") && base != "build_test.go" {
			versionFixture = false
			break
		}
	}
	legacyBuild = !versionFixture
	installFoundTools(t)
}

// installFoundTools reports every external binary as present. Command
// tests that need a missing stack replace toolLookPath afterwards.
// The host default is pinned empty so an omitted --format still
// follows toolchain inference on a Mac. Darwin policy tests set the
// host default after this helper.
func installFoundTools(t *testing.T) {
	t.Helper()
	orig := toolLookPath
	toolLookPath = func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	}
	origHelp := erofsHelp
	erofsHelp = func() (string, error) {
		return "Available compressors are: lz4, lz4hc, lzma, deflate, zstd\n", nil
	}
	origHost := hostDefaultType
	hostDefaultType = func() string { return "" }
	t.Cleanup(func() {
		toolLookPath = orig
		erofsHelp = origHelp
		hostDefaultType = origHost
	})
}

// fakePacker writes deterministic bytes derived from the source tree
// digest, so equal trees pack to equal ids and different trees do not.
func fakePacker(t *testing.T) image.ToolRunner {
	t.Helper()
	return func(name string, _ []string, args ...string) error {
		if name != "mksquashfs" {
			return fmt.Errorf("unexpected tool %q", name)
		}
		if len(args) < 2 {
			return fmt.Errorf("mksquashfs needs src and dst: %v", args)
		}
		sum := sha256.New()
		err := filepath.WalkDir(args[0], func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, relErr := filepath.Rel(args[0], p)
			if relErr != nil {
				return relErr
			}
			_, _ = fmt.Fprintf(sum, "%s %s\n", rel, d.Type())
			if d.Type().IsRegular() {
				body, readErr := os.ReadFile(p)
				if readErr != nil {
					return readErr
				}
				_, _ = sum.Write(body)
			}
			return nil
		})
		if err != nil {
			return err
		}
		body := "hsqs-fake " + hex.EncodeToString(sum.Sum(nil))
		return os.WriteFile(args[1], []byte(body), 0o644)
	}
}

// writeSquashFile writes a regular file whose leading bytes are the
// squashfs magic, so a file-path mount detects squashfs.
func writeSquashFile(t *testing.T, path string) {
	t.Helper()
	writeTestFile(t, path, "hsqs-fake")
}

// writeErofsFile writes a regular file with the erofs superblock
// magic at offset 1024, so a file-path mount detects erofs.
func writeErofsFile(t *testing.T, path string) {
	t.Helper()
	body := make([]byte, 1028)
	copy(body[1024:], []byte{0xE2, 0xE1, 0xF5, 0xE0})
	writeTestFile(t, path, string(body))
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildVersionImage runs the activated build command and returns its id.
func buildVersionImage(t *testing.T, lib []string, name, src string) string {
	t.Helper()
	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", name, src)...)
	if err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, stderr)
	}
	return buildOutput(t, stdout)
}

func buildImage(t *testing.T, lib []string, name, src string) string {
	t.Helper()
	legacyBuild = true
	cfg := config.Defaults(t.TempDir())
	cfg.ImagesDir, cfg.SessionsDir = lib[1], lib[3]
	cfg.CacheDir = filepath.Join(filepath.Dir(cfg.ImagesDir), "cache")
	res, err := image.BuildFull(image.FullRequest{
		Config: cfg, Name: name, SrcDir: src, Builder: "test", Run: packTool,
		Refs: session.Refs{Dir: cfg.SessionsDir},
	})
	if err != nil {
		t.Fatalf("legacy fixture %s: %v", name, err)
	}
	return res.ID
}

// buildOutput checks one build result line and returns the id.
func buildOutput(t *testing.T, stdout string) string {
	t.Helper()
	fields := strings.Fields(stdout)
	if len(fields) != 3 || !isPayloadID(fields[2]) {
		t.Fatalf("build stdout = %q", stdout)
	}
	return fields[2]
}

func isPayloadID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// viewTable is one writable merged view over the library image, with
// its upper under the runtime tree.
func viewTable(imagesDir, name, merged, upper string) []mountinfo.Mount {
	repo, err := image.OpenRepo(imagesDir, name)
	if err != nil {
		panic(err)
	}
	return []mountinfo.Mount{{
		Point:  merged,
		FSType: "fuse.fuse-overlayfs",
		Source: filepath.Join(repo.Dir, image.ImageFile),
		Upper:  upper,
	}}
}

func TestBuildFullPrintsNameAndID(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")

	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "alice/app", src)...)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, stderr)
	}
	fields := strings.Fields(stdout)
	if len(fields) != 3 || fields[0] != "full" || fields[1] != "alice/app" || !isPayloadID(fields[2]) {
		t.Fatalf("stdout = %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(imagesDir, "alice", "app", "_objects", fields[2], "image.sqsh")); err != nil {
		t.Fatal(err)
	}
	repo, err := image.OpenRepo(imagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := image.LoadVersionManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Images[0].ID != fields[2] || manifest.Images[0].Builder != Version {
		t.Fatalf("node = %+v, stdout id = %s", manifest.Images[0], fields[2])
	}
	if manifest.Images[0].Format != (image.Format{Type: image.FormatSquashfs, Compression: image.CompressionZstd}) {
		t.Fatalf("format = %+v, want squashfs/zstd", manifest.Images[0].Format)
	}
}

func TestBuildStoresDescriptionsFromMessagesFilesAndStdin(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")
	descriptionFile := filepath.Join(work, "description.txt")
	writeTestFile(t, descriptionFile, "From file\r\n\r\nDetails\n")

	cases := []struct {
		name  string
		args  []string
		stdin string
		want  string
	}{
		{name: "messages", args: []string{"-m", "Subject", "-m", "Body\nDetails"}, want: "Subject\n\nBody\nDetails"},
		{name: "file", args: []string{"-F", descriptionFile}, want: "From file\n\nDetails"},
		{name: "stdin", args: []string{"-F", "-"}, stdin: "From stdin\n\nDetails\n", want: "From stdin\n\nDetails"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := "alice/" + tc.name
			args := append(append(append([]string{}, lib...), "build", "--image", name), tc.args...)
			args = append(args, src)
			_, stderr, err := runCmdInput(t, tc.stdin, args...)
			if err != nil {
				t.Fatalf("build: %v\n%s", err, stderr)
			}
			repo, err := image.OpenRepo(imagesDir, name)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := image.LoadVersionManifest(repo)
			if err != nil {
				t.Fatal(err)
			}
			if got := manifest.Images[0].Description; got != tc.want {
				t.Fatalf("description = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildDescriptionFlagsRejectInvalidCombinationsAndConflicts(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "empty values are mutually exclusive", args: []string{"-m=", "-F="}, want: "mutually exclusive"},
		{name: "duplicate file", args: []string{"-F", "one", "-F", "two"}, want: "only once"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append(append([]string{}, lib...), "build", "--image", "alice/app"), tc.args...)
			args = append(args, src)
			_, _, _, err := runCmd(t, args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}

	buildArgs := append(append([]string{}, lib...), "build", "--image", "alice/app", "-m", "Original", src)
	if _, stderr, _, err := runCmd(t, buildArgs...); err != nil {
		t.Fatalf("initial build: %v\n%s", err, stderr)
	}
	conflictArgs := append(append([]string{}, lib...), "build", "--image", "alice/app", "-m", "Changed", src)
	_, _, _, err := runCmd(t, conflictArgs...)
	if err == nil || !strings.Contains(err.Error(), "use images describe") {
		t.Fatalf("duplicate description conflict = %v", err)
	}
}

func runCmdInput(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := NewRootCmd()
	var out, errBuf bytes.Buffer
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errBuf.String(), err
}

func TestBuildHelpShowsSplit(t *testing.T) {
	_, _ = withWorkspace(t)
	stdout, stderr, _, err := runCmd(t, "build", "--help")
	if err != nil {
		t.Fatalf("build --help: %v\n%s", err, stderr)
	}
	help := stdout + stderr
	for _, want := range []string{
		"-s, --split", "-f, --full", "-i, --incremental", "-I, --image",
		"--format", "--compression",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("build help missing %s:\n%s", want, help)
		}
	}
}

func TestBuildSplitFlagProducesBlocks(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")

	stdout, stderr, _, err := runCmd(t, append(lib,
		"build", "--split", "16", "--image", "alice/app", src)...)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, stderr)
	}
	id := buildOutput(t, stdout)
	blocksDir := filepath.Join(imagesDir, "alice", "app", "_objects", id)
	names, err := filepath.Glob(filepath.Join(blocksDir, "blk.*"))
	if err != nil || len(names) < 2 {
		t.Fatalf("blocks = %v (%v), want a multi-block payload", names, err)
	}
	if _, err := os.Stat(filepath.Join(blocksDir, "image.sqsh")); !os.IsNotExist(err) {
		t.Fatalf("split build left a single-file image: %v", err)
	}
}

func TestBuildSplitZeroYieldsSingleFile(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	writeProjectConfig(t, work, "split_size: 16\n")
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")

	stdout, stderr, _, err := runCmd(t, append(lib,
		"build", "--split", "0", "--image", "alice/app", src)...)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, stderr)
	}
	id := buildOutput(t, stdout)
	if _, err := os.Stat(filepath.Join(imagesDir, "alice", "app", "_objects", id, "image.sqsh")); err != nil {
		t.Fatalf("explicit --split 0 left no image.sqsh: %v", err)
	}
	if _, err := os.Stat(filepath.Join(imagesDir, "alice", "app", "_blocks")); !os.IsNotExist(err) {
		t.Fatalf("--split 0 published blocks: %v", err)
	}
}

func TestBuildSplitFlagOverridesConfig(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	writeProjectConfig(t, work, "split_size: 0\n")
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")

	stdout, stderr, _, err := runCmd(t, append(lib,
		"build", "--split", "16", "--image", "alice/app", src)...)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, stderr)
	}
	id := buildOutput(t, stdout)
	blocksDir := filepath.Join(imagesDir, "alice", "app", "_objects", id)
	if _, err := os.Stat(blocksDir); err != nil {
		t.Fatalf("CLI --split did not override config: %v", err)
	}
}

func TestBuildUsageErrorsExitTwo(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "f.txt"), "x")

	tests := []struct {
		name string
		args []string
	}{
		{name: "both mode flags", args: []string{"build", "--full", "--incremental", "--image", "alice/app", src}},
		{name: "missing image name", args: []string{"build", src}},
		{name: "bad image name", args: []string{"build", "--image", "_foo", src}},
		{name: "colon image name", args: []string{"build", "--image", "alice/app:abcd", src}},
		{name: "colon before path cleaning", args: []string{"build", "--image", "alice/bad:name/../app", src}},
		{name: "incr outside a view", args: []string{"build", "--incremental", "--image", "alice/app", src}},
		{name: "short incr outside a view", args: []string{"build", "-i", "-I", "alice/app", src}},
		{name: "two source arguments", args: []string{"build", "--image", "alice/app", src, src}},
		{name: "bad split size", args: []string{"build", "--split", "1.5G", "--image", "alice/app", src}},
		{name: "erofs xz", args: []string{"build", "--format", "erofs", "--compression", "xz", "--image", "alice/app", src}},
		{name: "unknown format", args: []string{"build", "--format", "tar", "--image", "alice/app", src}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, _, err := runCmd(t, append(lib, tt.args...)...)
			if ExitCode(err) != 2 {
				t.Fatalf("ExitCode(%v) = %d, want 2", err, ExitCode(err))
			}
			if strings.HasPrefix(stdout, "full ") || strings.HasPrefix(stdout, "incr ") {
				t.Fatalf("usage failure printed a result: %q", stdout)
			}
		})
	}
}

func TestBuildFullIdempotentOnSameSource(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")
	first := buildVersionImage(t, lib, "alice/app", src)
	metaPath := filepath.Join(imagesDir, "alice", "app", "meta.yaml")
	before, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "alice/app", src)...)
	if err != nil {
		t.Fatalf("second build: %v\n%s", err, stderr)
	}
	second := buildOutput(t, stdout)
	if second != first {
		t.Fatalf("second id = %s, want %s", second, first)
	}
	after, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("idle rebuild rewrote meta:\n%s\n%s", before, after)
	}
	if _, err := os.Stat(filepath.Join(imagesDir, "alice", "app", "_archives")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("idle rebuild archived the image: %v", err)
	}
}

func TestBuildIncrIdempotentOnSameUpper(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	upper := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper")
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "base.txt"), "base")
	withBuildSeams(t, viewTable(imagesDir, "alice/app", merged, upper))
	buildVersionImage(t, lib, "alice/app", src)
	writeTestFile(t, filepath.Join(upper, "new.txt"), "layer")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(merged)
	// --incremental keeps the layer even when the upper is large
	// enough that the default would promote to a full rebuild.
	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--incremental")...)
	if err != nil {
		t.Fatalf("first incr: %v\n%s", err, stderr)
	}
	fields := strings.Fields(stdout)
	if len(fields) != 3 || fields[0] != "incr" || !isPayloadID(fields[2]) {
		t.Fatalf("stdout = %q", stdout)
	}
	layer := fields[2]
	stdout, stderr, _, err = runCmd(t, append(lib, "build", "--incremental")...)
	if err != nil {
		t.Fatalf("second incr: %v\n%s", err, stderr)
	}
	fields = strings.Fields(stdout)
	if len(fields) != 3 || fields[0] != "incr" || fields[2] != layer {
		t.Fatalf("second stdout = %q, want incr id %s", stdout, layer)
	}
	repo, err := image.OpenRepo(imagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := image.LoadVersionManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Images) != 2 || manifest.Images[1].ID != layer {
		t.Fatalf("chain = %+v", manifest.Images[1:])
	}
}

func TestBuildIncrementalForwardsDescriptionAndRejectsDuplicateChange(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	upper := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper")
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "base.txt"), "base")
	withBuildSeams(t, viewTable(imagesDir, "alice/app", merged, upper))
	buildVersionImage(t, lib, "alice/app", src)
	writeTestFile(t, filepath.Join(upper, "new.txt"), "layer")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(merged)

	_, stderr, _, err := runCmd(t, append(lib,
		"build", "--incremental", "-m", "Incremental details")...)
	if err != nil {
		t.Fatalf("incremental build: %v\n%s", err, stderr)
	}
	repo, err := image.OpenRepo(imagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := image.LoadVersionManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.Images[1].Description; got != "Incremental details" {
		t.Fatalf("incremental description = %q", got)
	}

	_, _, _, err = runCmd(t, append(lib,
		"build", "--incremental", "-m", "Changed")...)
	if err == nil || !strings.Contains(err.Error(), "use images describe") {
		t.Fatalf("duplicate incremental description conflict = %v", err)
	}
}

func TestBuildDivertedSubdirIdempotent(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	upper := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper")
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "README.txt"), "base")
	writeTestFile(t, filepath.Join(src, "docs", "guide.md"), "guide")
	withBuildSeams(t, viewTable(imagesDir, "alice/app", merged, upper))
	buildVersionImage(t, lib, "alice/app", src)
	if err := os.MkdirAll(filepath.Join(merged, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(merged, "docs", "guide.md"), "guide")
	docs := filepath.Join(merged, "docs")
	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "bob/docs", docs)...)
	if err != nil {
		t.Fatalf("first divert: %v\n%s", err, stderr)
	}
	first := buildOutput(t, stdout)
	metaPath := filepath.Join(imagesDir, "bob", "docs", "meta.yaml")
	before, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, _, err = runCmd(t, append(lib, "build", "--image", "bob/docs", docs)...)
	if err != nil {
		t.Fatalf("second divert: %v\n%s", err, stderr)
	}
	second := buildOutput(t, stdout)
	if second != first {
		t.Fatalf("second id = %s, want %s", second, first)
	}
	after, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("idle diverted rebuild rewrote meta")
	}
}

func TestBuildCoveringSourceIsUsage(t *testing.T) {
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	packTool = func(string, []string, ...string) error {
		t.Fatal("packer ran on a covering source")
		return nil
	}
	cover := filepath.Dir(imagesDir)
	if err := os.MkdirAll(cover, 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "alice/app", cover)...)
	if ExitCode(err) != 2 || !strings.Contains(err.Error()+"\n"+stderr, "would cover reserved") {
		t.Fatalf("ExitCode(%v) = %d\n%s", err, ExitCode(err), stderr)
	}
	if stdout != "" {
		t.Fatalf("usage failure printed %q", stdout)
	}
}

func TestBuildSymlinkIntoReservedIsUsage(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	packTool = func(string, []string, ...string) error {
		t.Fatal("packer ran on a symlink into reserved")
		return nil
	}
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(work, "via-images")
	if err := os.Symlink(imagesDir, link); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "alice/app", link)...)
	if ExitCode(err) != 2 || !strings.Contains(err.Error()+"\n"+stderr, "inside reserved") {
		t.Fatalf("ExitCode(%v) = %d\n%s", err, ExitCode(err), stderr)
	}
	if stdout != "" {
		t.Fatalf("usage failure printed %q", stdout)
	}
}

func TestBuildPackerFailureExitsOne(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	packTool = func(string, []string, ...string) error { return errors.New("mksquashfs: boom") }
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "f.txt"), "x")

	stdout, _, _, err := runCmd(t, append(lib, "build", "--image", "alice/app", src)...)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
	if stdout != "" {
		t.Fatalf("failed build printed %q", stdout)
	}
	if _, statErr := os.Stat(filepath.Join(imagesDir, "alice", "app", "image.sqsh")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed build left an active image: %v", statErr)
	}
}

func TestBuildIncrementalInsideView(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	runtimeDir := filepath.Join(sessionsDir(home), "dev", "runtime", "m1")
	merged := filepath.Join(work, "merged")
	upper := filepath.Join(runtimeDir, "upper")
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "base.txt"), "base")
	withBuildSeams(t, viewTable(imagesDir, "alice/app", merged, upper))
	fullID := buildVersionImage(t, lib, "alice/app", src)

	writeTestFile(t, filepath.Join(upper, "new.txt"), "layer change")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(merged)
	stdout, stderr, _, err := runCmd(t, append(lib, "build")...)
	if err != nil {
		t.Fatalf("incr build: %v\n%s", err, stderr)
	}
	fields := strings.Fields(stdout)
	if len(fields) != 3 || fields[0] != "incr" || fields[1] != "alice/app" || !isPayloadID(fields[2]) {
		t.Fatalf("stdout = %q", stdout)
	}
	repo, err := image.OpenRepo(imagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := image.LoadVersionManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Images[0].ID != fullID || len(manifest.Images) != 2 || manifest.Images[1].ID != fields[2] {
		t.Fatalf("meta = %+v, stdout id = %s", manifest, fields[2])
	}
	if _, err := os.Stat(filepath.Join(imagesDir, "alice", "app", "_objects", fields[2], "image.sqsh")); err != nil {
		t.Fatal(err)
	}
}

func TestBuildDirectoryFormsMatchInsideView(t *testing.T) {
	// Each form starts from an empty chain. A second build of the
	// same upper can cross the half-base rule and publish a full
	// image, which is a size decision, not a path decision.
	forms := []struct {
		name   string
		dot    bool
		abs    bool
		parent bool
		child  bool
	}{
		{name: "omitted"},
		{name: "dot", dot: true},
		{name: "absolute", abs: true},
		{name: "absolute from parent", abs: true, parent: true},
		{name: "child path", child: true},
	}
	for _, form := range forms {
		t.Run(form.name, func(t *testing.T) {
			home, work := withWorkspace(t)
			imagesDir, lib := libFlags(home)
			merged := filepath.Join(work, "merged")
			upper := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper")
			src := filepath.Join(work, "src")
			writeTestFile(t, filepath.Join(src, "base.txt"), "base")
			withBuildSeams(t, viewTable(imagesDir, "alice/app", merged, upper))
			fullID := buildVersionImage(t, lib, "alice/app", src)
			writeTestFile(t, filepath.Join(upper, "new.txt"), "layer change")
			if err := os.MkdirAll(merged, 0o755); err != nil {
				t.Fatal(err)
			}
			dir := merged
			if form.parent {
				dir = home
			}
			t.Chdir(dir)
			args := append(append([]string{}, lib...), "build")
			switch {
			case form.dot:
				args = append(args, ".")
			case form.abs:
				args = append(args, merged)
			case form.child:
				args = append(args, "sub")
			}
			stdout, stderr, _, err := runCmd(t, args...)
			if err != nil {
				t.Fatalf("build: %v\n%s", err, stderr)
			}
			fields := strings.Fields(stdout)
			if len(fields) != 3 || fields[0] != "incr" || fields[1] != "alice/app" || !isPayloadID(fields[2]) {
				t.Fatalf("stdout = %q", stdout)
			}
			repo, err := image.OpenRepo(imagesDir, "alice/app")
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := image.LoadVersionManifest(repo)
			if err != nil {
				t.Fatal(err)
			}
			if manifest.Images[0].ID != fullID || len(manifest.Images) != 2 || manifest.Images[1].ID != fields[2] {
				t.Fatalf("meta = %+v, stdout id = %s", manifest, fields[2])
			}
		})
	}
}

func TestBuildIdentityFallsBackToSessionRecord(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	// A killed daemon left an anonymous table entry: no source
	// resolves through the library, and only the session record
	// still names the image behind the view.
	table := []mountinfo.Mount{{
		Point:  merged,
		FSType: "fuse.fuse-overlayfs",
		Source: "fuse-overlayfs",
	}}
	withBuildSeams(t, table)
	writeTestFile(t, filepath.Join(merged, "base.txt"), "base")
	writeSessionRecord(t, sessionsDir(home), "dev",
		libraryAction("alice/app", merged, session.ModeRW))
	t.Chdir(merged)

	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--full")...)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, stderr)
	}
	fields := strings.Fields(stdout)
	if len(fields) != 3 || fields[0] != "full" || fields[1] != "alice/app" || !isPayloadID(fields[2]) {
		t.Fatalf("stdout = %q", stdout)
	}

	// A different --image packs this path as that image's full
	// source instead of conflicting with the session record.
	stdout, stderr, _, err = runCmd(t, append(lib, "build", "--full", "--image", "bob/other")...)
	if err != nil {
		t.Fatalf("diverted build: %v\n%s", err, stderr)
	}
	fields = strings.Fields(stdout)
	if len(fields) != 3 || fields[0] != "full" || fields[1] != "bob/other" || !isPayloadID(fields[2]) {
		t.Fatalf("diverted stdout = %q", stdout)
	}
	if _, stderr, _, err := runCmd(t, append(lib, "build", "--full", "--image", "alice/app")...); err != nil {
		t.Fatalf("consistent build: %v\n%s", err, stderr)
	}
}

func TestBuildReadOnlyViewNeedsExplicitFull(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	table := []mountinfo.Mount{{
		Point:  merged,
		FSType: "overlay",
		Source: "overlay",
	}}
	withBuildSeams(t, table)
	writeTestFile(t, filepath.Join(merged, "f.txt"), "x")
	t.Chdir(merged)

	_, _, _, err := runCmd(t, append(lib, "build")...)
	if ExitCode(err) != 2 {
		t.Fatalf("ExitCode(%v) = %d, want 2", err, ExitCode(err))
	}

	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--full", "--image", "alice/app")...)
	if err != nil {
		t.Fatalf("full build of the view: %v\n%s", err, stderr)
	}
	fields := strings.Fields(stdout)
	if len(fields) != 3 || fields[0] != "full" || fields[1] != "alice/app" {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestExceedsHalfBase(t *testing.T) {
	if exceedsHalfBase(100, 40, 10) {
		t.Fatal("exactly half must stay incremental")
	}
	if !exceedsHalfBase(100, 40, 11) {
		t.Fatal("just over half must promote")
	}
	if exceedsHalfBase(0, 10, 10) || exceedsHalfBase(-1, 10, 10) {
		t.Fatal("non-positive base must not promote")
	}
}

func TestTreeBytesSumsFilesNotDirectories(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.txt"), "abcd")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "sub", "b.txt"), "xy")
	got, err := treeBytes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != 6 {
		t.Fatalf("treeBytes = %d, want 6", got)
	}
}

func TestBuildResolvesFormatFlagConfigThenTools(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")

	t.Run("config selects erofs", func(t *testing.T) {
		writeProjectConfig(t, work, "image_format: erofs\nimage_compression: lz4\n")
		packTool = packAny(t, "mkfs.erofs")
		stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "alice/cfg", src)...)
		if err != nil {
			t.Fatalf("build: %v\n%s", err, stderr)
		}
		assertBuiltFormat(t, imagesDir, "alice/cfg", buildOutput(t, stdout), image.FormatErofs, "lz4")
	})

	t.Run("flag overrides config", func(t *testing.T) {
		writeProjectConfig(t, work, "image_format: erofs\nimage_compression: lz4\n")
		packTool = fakePacker(t)
		stdout, stderr, _, err := runCmd(t, append(lib,
			"build", "--format", "squashfs", "--compression", "gzip", "--image", "alice/flag", src)...)
		if err != nil {
			t.Fatalf("build: %v\n%s", err, stderr)
		}
		assertBuiltFormat(t, imagesDir, "alice/flag", buildOutput(t, stdout), image.FormatSquashfs, "gzip")
	})

	t.Run("erofs toolchain alone infers erofs", func(t *testing.T) {
		if err := os.Remove(filepath.Join(work, ".tiler.yaml")); err != nil {
			t.Fatal(err)
		}
		toolLookPath = lookOnly("mkfs.erofs", "erofsfuse")
		packTool = packAny(t, "mkfs.erofs")
		stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "alice/infer", src)...)
		if err != nil {
			t.Fatalf("build: %v\n%s", err, stderr)
		}
		assertBuiltFormat(t, imagesDir, "alice/infer", buildOutput(t, stdout), image.FormatErofs, image.CompressionZstd)
	})
}

func TestBuildErofsUsesLz4WhenZstdIsMissing(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	erofsHelp = func() (string, error) {
		return "Available compressors are: lz4, lz4hc, lzma, deflate, libdeflate\n", nil
	}
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")
	packTool = packAny(t, "mkfs.erofs")

	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--format", "erofs", "--image", "alice/app", src)...)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, stderr)
	}
	assertBuiltFormat(t, imagesDir, "alice/app", buildOutput(t, stdout), image.FormatErofs, "lz4")

	_, _, _, err = runCmd(t, append(lib,
		"build", "--format", "erofs", "--compression", "zstd", "--image", "alice/zstd", src)...)
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), `no "zstd" compressor`) {
		t.Fatalf("explicit zstd: ExitCode(%v) = %d", err, ExitCode(err))
	}
}

func TestBuildErofsXZIsUsage(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")
	_, _, _, err := runCmd(t, append(lib,
		"build", "--format", "erofs", "--compression", "xz", "--image", "alice/app", src)...)
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("ExitCode(%v) = %d, want usage", err, ExitCode(err))
	}
	if _, statErr := os.Stat(filepath.Join(imagesDir, "alice", "app", "image.sqsh")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rejected build left an image: %v", statErr)
	}
}

func TestBuildRefusesWhenNoStackOrFormatToolsMissing(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")

	toolLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	_, _, _, err := runCmd(t, append(lib, "build", "--image", "alice/app", src)...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "no complete image toolchain") ||
		!strings.Contains(err.Error(), "tiler doctor") {
		t.Fatalf("no stack: ExitCode(%v) = %d", err, ExitCode(err))
	}

	toolLookPath = lookOnly("mkfs.erofs", "erofsfuse")
	_, _, _, err = runCmd(t, append(lib, "build", "--format", "squashfs", "--image", "alice/app", src)...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "squashfs toolchain is incomplete") {
		t.Fatalf("fine gate: ExitCode(%v) = %d", err, ExitCode(err))
	}
}

func TestBuildIncrementalSelectsIndependentFormat(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	runtimeDir := filepath.Join(sessionsDir(home), "dev", "runtime", "m1")
	merged := filepath.Join(work, "merged")
	upper := filepath.Join(runtimeDir, "upper")
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "base.txt"), "base")
	withBuildSeams(t, viewTable(imagesDir, "alice/app", merged, upper))
	packTool = packAny(t, "mksquashfs", "mkfs.erofs")
	buildVersionImage(t, lib, "alice/app", src)
	writeTestFile(t, filepath.Join(upper, "new.txt"), "layer")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(merged)

	writeProjectConfig(t, work, "image_format: erofs\nimage_compression: lz4\n")
	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--incremental")...)
	if err != nil {
		t.Fatalf("inherit build: %v\n%s", err, stderr)
	}
	repo, err := image.OpenRepo(imagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := image.LoadVersionManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Images) != 2 || manifest.Images[1].ID != buildOutput(t, stdout) {
		t.Fatalf("chain = %+v", manifest.Images[1:])
	}
	if manifest.Images[1].Format != (image.Format{Type: image.FormatErofs, Compression: "lz4"}) {
		t.Fatalf("incremental format = %+v", manifest.Images[1].Format)
	}
}

func assertBuiltFormat(t *testing.T, imagesDir, name, id, format, compression string) {
	t.Helper()
	repo, err := image.OpenRepo(imagesDir, name)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := image.LoadVersionManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	node := manifest.Images[0]
	if node.ID != id || node.Format.Type != format || node.Format.Compression != compression {
		t.Fatalf("meta id=%s format=%s/%s, want %s %s/%s",
			node.ID, node.Format.Type, node.Format.Compression, id, format, compression)
	}
}

func packAny(t *testing.T, names ...string) image.ToolRunner {
	t.Helper()
	allow := map[string]bool{}
	for _, name := range names {
		allow[name] = true
	}
	return func(name string, _ []string, args ...string) error {
		if !allow[name] {
			return fmt.Errorf("unexpected tool %q", name)
		}
		dst, err := packDestination(name, args)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, []byte("packed-by-"+name), 0o644)
	}
}

func packDestination(name string, args []string) (string, error) {
	switch name {
	case "mksquashfs":
		if len(args) < 2 {
			return "", fmt.Errorf("mksquashfs args %v", args)
		}
		return args[1], nil
	case "mkfs.erofs":
		if len(args) < 2 {
			return "", fmt.Errorf("mkfs.erofs args %v", args)
		}
		return args[len(args)-2], nil
	default:
		return "", fmt.Errorf("no destination for %s", name)
	}
}

func lookOnly(names ...string) func(string) (string, error) {
	set := map[string]bool{}
	for _, name := range names {
		set[name] = true
	}
	return func(name string) (string, error) {
		if set[name] {
			return "/usr/bin/" + name, nil
		}
		return "", exec.ErrNotFound
	}
}

// pinnedAction is one recorded library mount action whose desired
// stack pins (fullID, tipID), so an in-view rebase has a distinct
// old stack to replace and record as served.
func pinnedAction(imageRef, mountpoint, mode, fullID, tipID string) session.Action {
	action := libraryAction(imageRef, mountpoint, mode)
	action.FullID = fullID
	action.TipID = tipID
	return action
}

// onlyMountAction returns the single mount action of one record.
func onlyMountAction(t *testing.T, dir, name string) session.Action {
	t.Helper()
	actions := loadSessionRecord(t, dir, name).Actions
	if len(actions) != 1 || actions[0].Type != session.TypeMount {
		t.Fatalf("actions = %+v, want one mount", actions)
	}
	return actions[0]
}

// assertStack checks the desired and served stacks of one recorded
// mount action.
func assertStack(t *testing.T, action session.Action, fullID, tipID, servedID, servedTip string) {
	t.Helper()
	if action.FullID != fullID || action.TipID != tipID {
		t.Fatalf("desired = (%s, %s), want (%s, %s)",
			action.FullID, action.TipID, fullID, tipID)
	}
	if action.ServedID != servedID || action.ServedTip != servedTip {
		t.Fatalf("served = (%s, %s), want (%s, %s)",
			action.ServedID, action.ServedTip, servedID, servedTip)
	}
}

// viewFixture publishes one full image from an external source and
// records a live mount of it at merged, pinning the (full, full)
// stack. It returns the published full id.
func viewFixture(t *testing.T, home, work string, lib []string, merged string) string {
	t.Helper()
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "base.txt"), "base")
	fullID := buildVersionImage(t, lib, "alice/app", src)
	writeSessionRecord(t, sessionsDir(home), "dev",
		pinnedAction("alice/app", merged, session.ModeRW, fullID, fullID))
	return fullID
}

func TestBuildFullInsideViewRebasesSessionRecord(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	upper := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper")
	withBuildSeams(t, viewTable(imagesDir, "alice/app", merged, upper))
	withSessionSeams(t, nil)
	oldID := viewFixture(t, home, work, lib, merged)
	// The view content differs from the packed source, so the
	// rebuild publishes a new id instead of being a no-op.
	writeTestFile(t, filepath.Join(merged, "base.txt"), "changed in view")
	t.Chdir(merged)

	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--full")...)
	if err != nil {
		t.Fatalf("build --full: %v\n%s", err, stderr)
	}
	fields := strings.Fields(stdout)
	if len(fields) != 3 || fields[0] != "full" || fields[1] != "alice/app" || !isPayloadID(fields[2]) {
		t.Fatalf("stdout = %q", stdout)
	}
	newID := fields[2]
	if newID == oldID {
		t.Fatalf("rebuild kept the old id %s", oldID)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr %q", stderr)
	}
	// The record moves to the new stack; the live view keeps serving
	// the old one until the next mount.
	assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"), newID, newID, oldID, oldID)
}

func TestBuildFullNoopInsideViewKeepsSessionRecord(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	upper := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper")
	withBuildSeams(t, viewTable(imagesDir, "alice/app", merged, upper))
	withSessionSeams(t, nil)
	oldID := viewFixture(t, home, work, lib, merged)
	// The view content matches the packed source, so every rebuild
	// is a no-op that reports the existing id.
	writeTestFile(t, filepath.Join(merged, "base.txt"), "base")
	t.Chdir(merged)

	for i := 1; i <= 2; i++ {
		stdout, stderr, _, err := runCmd(t, append(lib, "build", "--full")...)
		if err != nil {
			t.Fatalf("build --full #%d: %v\n%s", i, err, stderr)
		}
		fields := strings.Fields(stdout)
		if len(fields) != 3 || fields[0] != "full" ||
			fields[1] != "alice/app" || fields[2] != oldID {
			t.Fatalf("stdout #%d = %q, want the no-op id %s", i, stdout, oldID)
		}
		if stderr != "" {
			t.Fatalf("build --full #%d warned: %q", i, stderr)
		}
	}
	// The no-op rebuild still rebases the view, but onto the stack
	// the record already pins: the engine's identity guard skips the
	// rewrite, so desired stays put and no served stack is recorded
	// for a view that never differed from it.
	assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"), oldID, oldID, "", "")
}

func TestBuildIncrInsideViewRebasesSessionRecord(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	upper := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper")
	withBuildSeams(t, viewTable(imagesDir, "alice/app", merged, upper))
	withSessionSeams(t, nil)
	fullID := viewFixture(t, home, work, lib, merged)
	writeTestFile(t, filepath.Join(upper, "new.txt"), "layer")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(merged)

	stdout, stderr, _, err := runCmd(t, append(lib, "build")...)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, stderr)
	}
	fields := strings.Fields(stdout)
	if len(fields) != 3 || fields[0] != "incr" || fields[1] != "alice/app" || !isPayloadID(fields[2]) {
		t.Fatalf("stdout = %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr %q", stderr)
	}
	// The full root stays; the desired tip moves to the new layer
	// and the served stack keeps the old (full, full) pin.
	assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"), fullID, fields[2], fullID, fullID)
}

func TestBuildPromotedFullInsideViewRebasesSessionRecord(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	upper := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper")
	withBuildSeams(t, viewTable(imagesDir, "alice/app", merged, upper))
	withSessionSeams(t, nil)
	oldID := viewFixture(t, home, work, lib, merged)
	// An upper beyond half the small fake base image promotes the
	// default build to a full rebuild of the view.
	writeTestFile(t, filepath.Join(upper, "big.txt"), strings.Repeat("x", 4096))
	writeTestFile(t, filepath.Join(merged, "base.txt"), "changed in view")
	t.Chdir(merged)

	stdout, stderr, _, err := runCmd(t, append(lib, "build")...)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, stderr)
	}
	fields := strings.Fields(stdout)
	if len(fields) != 3 || fields[0] != "full" || fields[1] != "alice/app" || !isPayloadID(fields[2]) {
		t.Fatalf("stdout = %q", stdout)
	}
	newID := fields[2]
	if newID == oldID {
		t.Fatalf("promoted rebuild kept the old id %s", oldID)
	}
	if !strings.Contains(stderr, "exceed 50%") {
		t.Fatalf("promotion warning missing: %q", stderr)
	}
	// The promoted path rebases exactly like a requested full build.
	assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"), newID, newID, oldID, oldID)
}

func TestBuildOutsideViewKeepsSessionRecord(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	upper := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper")
	withBuildSeams(t, viewTable(imagesDir, "alice/app", merged, upper))
	withSessionSeams(t, nil)
	fullID := viewFixture(t, home, work, lib, merged)

	// A plain directory build replaces the active image but keeps
	// the recorded pin: no rebase and no warning.
	other := filepath.Join(work, "other")
	writeTestFile(t, filepath.Join(other, "f.txt"), "external")
	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "alice/app", other)...)
	if err != nil {
		t.Fatalf("external build: %v\n%s", err, stderr)
	}
	if newID := buildOutput(t, stdout); newID == fullID {
		t.Fatalf("external rebuild kept the old id %s", fullID)
	}
	if stderr != "" {
		t.Fatalf("external build warned: %q", stderr)
	}
	assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"), fullID, fullID, "", "")

	// A --image diversion from inside the view packs another image
	// and leaves the mounted view's record alone as well.
	docs := filepath.Join(merged, "docs")
	writeTestFile(t, filepath.Join(docs, "guide.md"), "guide")
	t.Chdir(merged)
	stdout, stderr, _, err = runCmd(t, append(lib, "build", "--image", "bob/docs", docs)...)
	if err != nil {
		t.Fatalf("diverted build: %v\n%s", err, stderr)
	}
	fields := strings.Fields(stdout)
	if len(fields) != 3 || fields[0] != "full" || fields[1] != "bob/docs" || !isPayloadID(fields[2]) {
		t.Fatalf("diverted stdout = %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("diverted build warned: %q", stderr)
	}
	assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"), fullID, fullID, "", "")
}

func TestBuildRebaseFailureWarnsAndStillSucceeds(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	upper := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper")
	withBuildSeams(t, viewTable(imagesDir, "alice/app", merged, upper))
	withSessionSeams(t, nil)
	oldID := viewFixture(t, home, work, lib, merged)
	writeTestFile(t, filepath.Join(merged, "base.txt"), "changed in view")
	t.Chdir(merged)

	orig := rebaseMountedStack
	t.Cleanup(func() { rebaseMountedStack = orig })
	var gotPoint, gotName, gotFull, gotTip string
	rebaseMountedStack = func(_ config.Config, point, name, fullID, tipID string) error {
		gotPoint, gotName, gotFull, gotTip = point, name, fullID, tipID
		return errors.New("session store: boom")
	}

	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--full")...)
	if err != nil {
		t.Fatalf("build --full: %v\n%s", err, stderr)
	}
	fields := strings.Fields(stdout)
	if len(fields) != 3 || fields[0] != "full" || fields[1] != "alice/app" || !isPayloadID(fields[2]) {
		t.Fatalf("stdout = %q", stdout)
	}
	newID := fields[2]
	if gotPoint != merged || gotName != "alice/app" || gotFull != newID || gotTip != newID {
		t.Fatalf("rebase call = (%q, %q, %q, %q), want (%q, alice/app, %s, %s)",
			gotPoint, gotName, gotFull, gotTip, merged, newID, newID)
	}
	// The published image is not rolled back: the failure degrades
	// to a stderr warning and the build exits 0.
	if !strings.Contains(stderr, "warning") || !strings.Contains(stderr, "boom") {
		t.Fatalf("stderr = %q, want a rebase warning", stderr)
	}
	assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"), oldID, oldID, "", "")
}
