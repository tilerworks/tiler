package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/container"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
)

func TestImagesShowDMGFromMeta(t *testing.T) {
	t.Skip("isolated legacy DMG parser fixture")
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	for _, compression := range []string{"ulfo", "ulmo", "udro"} {
		t.Run(compression, func(t *testing.T) {
			writeDMGFixture(t, imagesDir, compression)
			out := mustShow(t, lib, "alice/app")
			for _, want := range []string{
				"  format      dmg/" + compression + "\n",
				"  form        single\n",
				"  chain       1\n",
			} {
				if !strings.Contains(out, want) {
					t.Fatalf("show missing %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestImagesShowDMGSurvivesPublishRepair(t *testing.T) {
	t.Skip("isolated legacy DMG parser fixture")
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	body := []byte("dmg!")
	meta := writeHashedDMGFixture(t, imagesDir, body)
	repo, err := image.OpenRepo(imagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Publish(body, meta, nil); err != nil {
		t.Fatal(err)
	}
	sqsh := filepath.Join(repo.Dir, image.ImageFile)
	if _, err := os.Stat(sqsh); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("image.sqsh after repair: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, image.ImageDMG)); err != nil {
		t.Fatal(err)
	}
	out := mustShow(t, lib, "alice/app")
	for _, want := range []string{
		"  format      dmg/ulfo\n",
		"  form        single\n",
		"  chain       1\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("show missing %q:\n%s", want, out)
		}
	}
}

func TestMountAndBuildRejectUnregisteredDMG(t *testing.T) {
	if _, err := container.Lookup(image.FormatDMG); err == nil {
		t.Skip("dmg is registered on this OS")
	}
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	writeDMGFixture(t, imagesDir, "ulfo")
	withMountSeams(t, nil)
	view := filepath.Join(work, "view")
	_, stderr, _, err := runCmd(t, append(lib, "mount", "alice/app", view)...)
	if err == nil || !strings.Contains(err.Error()+"\n"+stderr, "the format mounts only on macOS") {
		t.Fatalf("mount err = %v\n%s", err, stderr)
	}

	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "f.txt"), "x")
	_, stderr, _, err = runCmd(t, append(lib, "build", "--format", "dmg", "--image", "bob/app", src)...)
	if err == nil || !strings.Contains(err.Error()+"\n"+stderr, "the format mounts only on macOS") {
		t.Fatalf("build err = %v\n%s", err, stderr)
	}
}

// useDarwinHost installs the macOS host default. Call it after
// withBuildSeams or withMountSeams, which pin the default empty so
// other command tests keep toolchain inference.
func useDarwinHost(t *testing.T) {
	t.Helper()
	prev := hostDefaultType
	hostDefaultType = func() string { return image.FormatDMG }
	t.Cleanup(func() { hostDefaultType = prev })
}

func TestNativeHostDefaultType(t *testing.T) {
	got := nativeHostDefaultType()
	if runtime.GOOS == "darwin" {
		if got != image.FormatDMG {
			t.Fatalf("nativeHostDefaultType = %q, want dmg", got)
		}
		return
	}
	if got != "" {
		t.Fatalf("nativeHostDefaultType = %q, want empty", got)
	}
}

func TestDarwinHostDefaultIsDMGULFO(t *testing.T) {
	useDarwinHost(t)
	cmd := newBuildCmd()
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatal(err)
	}
	typ, comp, err := selectContainer(cmd, config.Config{}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if typ != image.FormatDMG || comp != "ulfo" {
		t.Fatalf("selected %s/%s, want dmg/ulfo", typ, comp)
	}
	for _, name := range []string{"ulfo", "ulmo", "udro"} {
		cmd := newBuildCmd()
		if err := cmd.ParseFlags([]string{"--compression", name}); err != nil {
			t.Fatal(err)
		}
		typ, got, err := selectContainer(cmd, config.Config{}, "", name)
		if err != nil {
			t.Fatalf("compression %s: %v", name, err)
		}
		if typ != image.FormatDMG || got != name {
			t.Fatalf("compression %s selected %s/%s", name, typ, got)
		}
	}
	cmd = newBuildCmd()
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatal(err)
	}
	typ, comp, err = selectContainer(cmd, config.Config{
		ImageFormat: image.FormatSquashfs, ImageCompression: "gzip",
	}, "", "")
	if err != nil || typ != image.FormatSquashfs || comp != "gzip" {
		t.Fatalf("config did not override the host default: %s/%s %v", typ, comp, err)
	}
}

func TestEmptyHostDefaultStillInfersSquashfs(t *testing.T) {
	prev := hostDefaultType
	hostDefaultType = func() string { return "" }
	t.Cleanup(func() { hostDefaultType = prev })
	cmd := newBuildCmd()
	if err := cmd.ParseFlags([]string{"--format", "squashfs"}); err != nil {
		t.Fatal(err)
	}
	typ, comp, err := selectContainer(cmd, config.Config{}, "squashfs", "")
	if err != nil {
		t.Fatal(err)
	}
	if typ != image.FormatSquashfs || comp != image.CompressionZstd {
		t.Fatalf("selected %s/%s, want squashfs/zstd", typ, comp)
	}
}

func TestDarwinDirectoryBuildPublishesImageDMG(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	useDarwinHost(t)
	forbidRebase(t)
	calls := installHdiutil(t, []byte("created-dmg"))
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")
	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "alice/app", src)...)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, stderr)
	}
	if !strings.HasPrefix(stdout, "full alice/app ") {
		t.Fatalf("stdout = %q", stdout)
	}
	// A plain directory build carries no mount point: it neither
	// rebases nor warns.
	if stderr != "" {
		t.Fatalf("directory build warned: %s", stderr)
	}
	if len(*calls) != 1 || !strings.Contains(strings.Join((*calls)[0], " "), "-srcfolder "+src) {
		t.Fatalf("calls = %q", *calls)
	}
	if strings.Contains(strings.Join((*calls)[0], " "), "-shadow") {
		t.Fatalf("directory build passed a shadow: %q", (*calls)[0])
	}
	assertFileText(t, filepath.Join(imagesDir, "alice", "app", image.ImageDMG), "created-dmg")
	if _, statErr := os.Stat(filepath.Join(imagesDir, "alice", "app", image.ImageFile)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("darwin default left %s: %v", image.ImageFile, statErr)
	}
}

func TestDarwinDirectoryBuildPublishesImmutableDMGVersion(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	legacyBuild = false
	useDarwinHost(t)
	installHdiutil(t, []byte("created-version-dmg"))
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")

	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "alice/app", src)...)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, stderr)
	}
	id := buildOutput(t, stdout)
	assertFileText(t, filepath.Join(imagesDir, "alice", "app", "_objects", id, image.ImageDMG),
		"created-version-dmg")
	if _, err := os.Stat(filepath.Join(imagesDir, "alice", "app", image.ImageDMG)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mutable dmg path exists: %v", err)
	}
	repo, err := image.OpenRepo(imagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := image.LoadVersionManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Images) != 1 || manifest.Images[0].ID != id ||
		manifest.Images[0].Format.Type != image.FormatDMG {
		t.Fatalf("manifest images = %+v", manifest.Images)
	}
}

func TestDarwinForeignFormatIsUsage(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	useDarwinHost(t)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")
	for _, format := range []string{"squashfs", "erofs"} {
		t.Run(format, func(t *testing.T) {
			_, stderr, _, err := runCmd(t, append(lib,
				"build", "--format", format, "--image", "alice/app", src)...)
			if ExitCode(err) != 2 || !strings.Contains(err.Error()+"\n"+stderr, "not available on macOS") {
				t.Fatalf("ExitCode(%v) = %d\n%s", err, ExitCode(err), stderr)
			}
		})
	}
}

func TestDMGCompressionAndSplitAreUsage(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")
	for _, comp := range []string{"zstd", "xz", "gzip", "lz4"} {
		t.Run(comp, func(t *testing.T) {
			_, stderr, _, err := runCmd(t, append(lib,
				"build", "--format", "dmg", "--compression", comp, "--image", "alice/app", src)...)
			if ExitCode(err) != 2 || !strings.Contains(err.Error()+"\n"+stderr, "not allowed") {
				t.Fatalf("ExitCode(%v) = %d\n%s", err, ExitCode(err), stderr)
			}
		})
	}
	_, stderr, _, err := runCmd(t, append(lib,
		"build", "--format", "dmg", "--split", "16", "--image", "alice/app", src)...)
	if ExitCode(err) != 2 || !strings.Contains(err.Error()+"\n"+stderr, "--split") {
		t.Fatalf("split ExitCode(%v) = %d\n%s", err, ExitCode(err), stderr)
	}
	writeProjectConfig(t, work, "split_size: 16\n")
	_, stderr, _, err = runCmd(t, append(lib,
		"build", "--format", "dmg", "--image", "alice/cfg", src)...)
	if ExitCode(err) != 2 || !strings.Contains(err.Error()+"\n"+stderr, "--split") {
		t.Fatalf("config split ExitCode(%v) = %d\n%s", err, ExitCode(err), stderr)
	}
}

func TestDMGSubdirIsUsage(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	writeDMGFixture(t, imagesDir, "ulfo")
	withMountSeams(t, nil)
	view := filepath.Join(work, "view")
	_, stderr, _, err := runCmd(t, append(lib, "mount", "--subdir", "src", "alice/app", view)...)
	if ExitCode(err) != 2 || !strings.Contains(err.Error()+"\n"+stderr, "--subdir") {
		t.Fatalf("ExitCode(%v) = %d\n%s", err, ExitCode(err), stderr)
	}
}

func TestDMGFileSubdirIsUsage(t *testing.T) {
	if _, err := container.Lookup(image.FormatDMG); err != nil {
		t.Skip("dmg is not registered on this OS")
	}
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	img := filepath.Join(work, "disk.img")
	writeKolyFile(t, img)
	view := filepath.Join(work, "view")
	_, stderr, _, err := runCmd(t, append(lib, "mount", "--subdir", "src", img, view)...)
	if ExitCode(err) != 2 || !strings.Contains(err.Error()+"\n"+stderr, "--subdir") {
		t.Fatalf("ExitCode(%v) = %d\n%s", err, ExitCode(err), stderr)
	}
}

func TestReadOnlyDMGViewRefusesPublish(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	writeDMGFixture(t, imagesDir, "ulfo")
	merged := filepath.Join(work, "merged")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	withBuildSeams(t, []mountinfo.Mount{{
		Point:  merged,
		FSType: "fuse",
		Source: filepath.Join(imagesDir, "alice", "app", image.ImageDMG),
	}})
	t.Chdir(merged)
	_, stderr, _, err := runCmd(t, append(lib, "build", "--full", "--image", "alice/app")...)
	if ExitCode(err) != 2 || !strings.Contains(err.Error()+"\n"+stderr, "read-only dmg") {
		t.Fatalf("ExitCode(%v) = %d\n%s", err, ExitCode(err), stderr)
	}
}

func TestReadOnlyDMGViewDivertsDifferentImage(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	writeDMGFixture(t, imagesDir, "ulfo")
	merged := filepath.Join(work, "merged")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(merged, "f.txt"), "x")
	withBuildSeams(t, []mountinfo.Mount{{
		Point:  merged,
		FSType: "fuse",
		Source: filepath.Join(imagesDir, "alice", "app", image.ImageDMG),
	}})
	useDarwinHost(t)
	calls := installHdiutil(t, []byte("diverted-dmg"))
	t.Chdir(merged)
	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--full", "--image", "bob/other")...)
	if err != nil {
		t.Fatalf("divert: %v\n%s", err, stderr)
	}
	if !strings.HasPrefix(stdout, "full bob/other ") {
		t.Fatalf("stdout = %q", stdout)
	}
	// An --image diversion carries no mount point: it neither
	// rebases nor warns.
	if stderr != "" {
		t.Fatalf("diversion warned: %s", stderr)
	}
	if len(*calls) != 1 || !strings.Contains(strings.Join((*calls)[0], " "), "-srcfolder") {
		t.Fatalf("calls = %q", *calls)
	}
	assertFileText(t, filepath.Join(imagesDir, "bob", "other", image.ImageDMG), "diverted-dmg")
}

func TestReadOnlyKolySourceRefusesDifferentImage(t *testing.T) {
	// A koly file path is not a library identity, so --image cannot
	// divert: the read-only dmg refusal still owns the path.
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(work, "disk.img")
	writeKolyFile(t, payload)
	withBuildSeams(t, []mountinfo.Mount{{
		Point:  merged,
		FSType: "fuse",
		Source: payload,
	}})
	t.Chdir(merged)
	_, stderr, _, err := runCmd(t, append(lib, "build", "--full", "--image", "bob/other")...)
	if ExitCode(err) != 2 || !strings.Contains(err.Error()+"\n"+stderr, "read-only dmg") {
		t.Fatalf("ExitCode(%v) = %d\n%s", err, ExitCode(err), stderr)
	}
	published := filepath.Join(imagesDir, "bob", "other", image.ImageFile)
	if _, statErr := os.Stat(published); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("koly view was published: %v", statErr)
	}
}

func TestReadOnlySquashfsViewStillPublishes(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(work, "base.sqsh")
	body := make([]byte, 600)
	copy(body, []byte("hsqs-fake"))
	writeTestFile(t, payload, string(body))
	withBuildSeams(t, []mountinfo.Mount{{
		Point:  merged,
		FSType: "fuse",
		Source: payload,
	}})
	writeTestFile(t, filepath.Join(merged, "f.txt"), "x")
	t.Chdir(merged)
	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--full", "--image", "alice/app")...)
	if err != nil {
		t.Fatalf("squashfs view: %v\n%s", err, stderr)
	}
	if !strings.HasPrefix(stdout, "full alice/app ") {
		t.Fatalf("stdout = %q", stdout)
	}
	if _, statErr := os.Stat(filepath.Join(imagesDir, "alice", "app", image.ImageFile)); statErr != nil {
		t.Fatal(statErr)
	}
}

func TestReadOnlySourceReadErrorIsNotNonDMG(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(work, "disk.img")
	body := make([]byte, 512)
	for i := range body {
		body[i] = 'x'
	}
	writeTestFile(t, payload, string(body))
	if err := os.Chmod(payload, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(payload, 0o644) })
	if file, openErr := os.Open(payload); openErr == nil {
		_ = file.Close()
		t.Skip("this user can read a mode 000 file")
	}
	withBuildSeams(t, []mountinfo.Mount{{
		Point:  merged,
		FSType: "fuse",
		Source: payload,
	}})
	t.Chdir(merged)
	_, stderr, _, err := runCmd(t, append(lib, "build", "--full", "--image", "bob/other")...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error()+"\n"+stderr, payload) {
		t.Fatalf("ExitCode(%v) = %d\n%s", err, ExitCode(err), stderr)
	}
	published := filepath.Join(imagesDir, "bob", "other", image.ImageFile)
	if _, statErr := os.Stat(published); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unreadable source was published: %v", statErr)
	}
}

func TestBuildFormatFlagNamesDMG(t *testing.T) {
	usage := newBuildCmd().Flags().Lookup("format").Usage
	for _, name := range []string{"dmg", "squashfs", "erofs"} {
		if !strings.Contains(usage, name) {
			t.Fatalf("format usage %q missing %s", usage, name)
		}
	}
	if strings.Contains(usage, "image_format") {
		t.Fatalf("format usage mentions image_format: %q", usage)
	}
}

func writeKolyFile(t *testing.T, path string) {
	t.Helper()
	body := make([]byte, 512)
	copy(body, []byte("koly"))
	writeTestFile(t, path, string(body))
}

func writeDMGFixture(t *testing.T, imagesDir, compression string) {
	t.Helper()
	dir := filepath.Join(imagesDir, "alice", "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("dmg!")
	path := filepath.Join(dir, image.ImageDMG)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	const sum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	manifest := dmgFixtureManifest(sum, sum, info.Size(),
		info.ModTime().Unix(), compression)
	if err := image.StoreManifest(filepath.Join(dir, "meta.yaml"), manifest); err != nil {
		t.Fatal(err)
	}
}

// dmgFixtureManifest assembles the single-node dmg manifest the
// fixtures store: the full image is the whole DAG and the head.
func dmgFixtureManifest(id, sha string, size, mtime int64, compression string) image.Manifest {
	return image.Manifest{
		Version: image.ManifestVersion,
		Name:    "alice/app",
		Format:  image.Format{Type: image.FormatDMG, Compression: compression},
		Head:    id,
		Images: []image.ImageMeta{{
			ID:        id,
			Kind:      image.KindFull,
			Size:      size,
			SHA256:    sha,
			MtimeUnix: mtime,
			CreatedAt: time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC),
			Builder:   "test",
		}},
	}
}

// manifestRootID is the full-root id of a fixture manifest. Test
// fixtures always hold a valid root, so the graph error cannot
// occur; a broken helper input panics instead of passing silently.
func manifestRootID(m image.Manifest) string {
	return manifestRoot(m).ID
}

// manifestRoot is the full-root node of a fixture manifest.
func manifestRoot(m image.Manifest) image.ImageMeta {
	root, err := m.Full()
	if err != nil {
		panic(err)
	}
	return root
}

func writeHashedDMGFixture(t *testing.T, imagesDir string, body []byte) image.Manifest {
	t.Helper()
	dir := filepath.Join(imagesDir, "alice", "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, image.ImageDMG)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	manifest := dmgFixtureManifest(image.ID(sum), hex.EncodeToString(sum[:]),
		info.Size(), info.ModTime().Unix(), "ulfo")
	if err := image.StoreManifest(filepath.Join(dir, "meta.yaml"), manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, image.ImageFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fixture must not include image.sqsh")
	}
	return manifest
}

func installHdiutil(t *testing.T, body []byte) *[][]string {
	t.Helper()
	calls := [][]string{}
	prev := hdiutilTool
	hdiutilTool = func(name string, _ []string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return os.WriteFile(hdiutilDest(args), body, 0o644)
	}
	t.Cleanup(func() { hdiutilTool = prev })
	return &calls
}

// installSeqHdiutil writes a different body on each hdiutil call, so
// two consecutive bakes publish two distinct images instead of the
// second one idling against the first one's content fingerprint.
func installSeqHdiutil(t *testing.T, bodies ...[]byte) *[][]string {
	t.Helper()
	calls := [][]string{}
	prev := hdiutilTool
	hdiutilTool = func(name string, _ []string, args ...string) error {
		i := len(calls)
		calls = append(calls, append([]string{name}, args...))
		if i >= len(bodies) {
			i = len(bodies) - 1
		}
		return os.WriteFile(hdiutilDest(args), bodies[i], 0o644)
	}
	t.Cleanup(func() { hdiutilTool = prev })
	return &calls
}

// forbidRebase fails the test if a dmg build rewrites the session
// record. The dmg paths never call the rebase seam: the shadow is
// bound to the attached base, so the record keeps its old id.
func forbidRebase(t *testing.T) {
	t.Helper()
	prev := rebaseMountedStack
	rebaseMountedStack = func(_ config.Config, point, name, fullID, tipID string) error {
		t.Fatalf("dmg build rebased %s (%s) to (%s, %s)", point, name, fullID, tipID)
		return nil
	}
	t.Cleanup(func() { rebaseMountedStack = prev })
}

// dmgID is the image id the fake hdiutil produces for a fixed body:
// finishDMG derives the id from the sha256 of the published bytes.
func dmgID(body string) string {
	return image.ID(sha256.Sum256([]byte(body)))
}

func assertFileText(t *testing.T, path, want string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil || string(body) != want {
		t.Fatalf("%s = %q, %v", path, body, err)
	}
}

func hdiutilDest(args []string) string {
	for i, arg := range args {
		if arg == "-o" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return args[len(args)-1]
}

func TestDMGViewBakeWarnsAndKeepsShadow(t *testing.T) {
	for _, flag := range []string{"", "-i", "--full"} {
		name := flag
		if name == "" {
			name = "default"
		}
		t.Run(name, func(t *testing.T) {
			home, work := withWorkspace(t)
			imagesDir, lib := libFlags(home)
			old := []byte("old-dmg")
			meta := writeHashedDMGFixture(t, imagesDir, old)
			shadow := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper.shadow")
			if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(shadow, []byte("live-shadow"), 0o644); err != nil {
				t.Fatal(err)
			}
			withBuildSeams(t, []mountinfo.Mount{{
				Point:  work,
				FSType: "fuse.dmg",
				Source: filepath.Join(imagesDir, "alice", "app", image.ImageDMG),
				Upper:  shadow,
			}})
			useDarwinHost(t)
			forbidRebase(t)
			calls := installHdiutil(t, []byte("baked-dmg"))
			action := libraryAction("alice/app", work, session.ModeRW)
			action.FullID = manifestRootID(meta)
			writeSessionRecord(t, sessionsDir(home), "dev", action)
			args := append(lib, "build")
			if flag != "" {
				args = append(args, flag)
			}
			args = append(args, "--image", "alice/app")
			stdout, stderr, _, err := runCmd(t, args...)
			if err != nil {
				t.Fatalf("build: %v\n%s", err, stderr)
			}
			if !strings.HasPrefix(stdout, "full alice/app ") || strings.Contains(stdout, "incr ") {
				t.Fatalf("stdout = %q", stdout)
			}
			if !strings.Contains(stderr, "dmg has no incremental layer") {
				t.Fatalf("stderr = %q", stderr)
			}
			// The keeps-old-base warning is a user contract: the
			// golden pins its exact text for every bake flag.
			assertGolden(t, "dmg_bake_warning", []byte(stderr))
			joined := strings.Join((*calls)[0], " ")
			if len(*calls) != 1 || !strings.Contains(joined, "-shadow "+shadow) {
				t.Fatalf("calls = %q", *calls)
			}
			if strings.Contains(joined, "mv ") || strings.Contains(joined, "rm ") {
				t.Fatalf("bake renamed or removed the shadow: %q", (*calls)[0])
			}
			if body, readErr := os.ReadFile(shadow); readErr != nil || string(body) != "live-shadow" {
				t.Fatalf("shadow = %q %v", body, readErr)
			}
			assertFileText(t, filepath.Join(imagesDir, "alice", "app", image.ImageDMG), "baked-dmg")
			assertFileText(t, filepath.Join(imagesDir, "alice", "app", "_hold", manifestRootID(meta), image.ImageDMG), "old-dmg")
			if _, statErr := os.Stat(filepath.Join(imagesDir, "alice", "app", "_archives")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("active session archived the pinned image: %v", statErr)
			}
			// The bake never rebases: the record keeps the pinned
			// base id and gains no served stack.
			assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"),
				action.FullID, "", "", "")
		})
	}
}

func TestDMGViewBakePublishesImmutableDMGVersion(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	legacyBuild = false
	useDarwinHost(t)
	installSeqHdiutil(t, []byte("base-version-dmg"), []byte("baked-version-dmg"))
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")
	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "alice/app", src)...)
	if err != nil {
		t.Fatalf("initial build: %v\n%s", err, stderr)
	}
	baseID := buildOutput(t, stdout)
	shadow := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper.shadow")
	writeTestFile(t, shadow, "live-shadow")
	basePath := filepath.Join(imagesDir, "alice", "app", "_objects", baseID, image.ImageDMG)
	loadMountTable = func() ([]mountinfo.Mount, error) {
		return []mountinfo.Mount{{Point: work, FSType: "fuse.dmg", Source: basePath, Upper: shadow}}, nil
	}
	action := libraryAction("alice/app", work, session.ModeRW)
	action.FullID, action.TipID = baseID, baseID
	writeSessionRecord(t, sessionsDir(home), "dev", action)
	t.Chdir(work)

	stdout, stderr, _, err = runCmd(t, append(lib, "build", "--image", "alice/app")...)
	if err != nil {
		t.Fatalf("bake: %v\n%s", err, stderr)
	}
	bakedID := buildOutput(t, stdout)
	if bakedID == baseID {
		t.Fatalf("bake reused base id %s", baseID)
	}
	assertFileText(t, basePath, "base-version-dmg")
	assertFileText(t, filepath.Join(imagesDir, "alice", "app", "_objects", bakedID, image.ImageDMG),
		"baked-version-dmg")
	if _, err := os.Stat(filepath.Join(imagesDir, "alice", "app", image.ImageDMG)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mutable dmg path exists: %v", err)
	}
	repo, err := image.OpenRepo(imagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := image.LoadVersionManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Images) != 2 || manifest.Images[0].ID != baseID || manifest.Images[1].ID != bakedID {
		t.Fatalf("manifest images = %+v", manifest.Images)
	}
}

func TestDMGCreateForwardsDescriptionAndRejectsDuplicateChange(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	legacyBuild = false
	useDarwinHost(t)
	installHdiutil(t, []byte("described-dmg"))
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")

	_, stderr, _, err := runCmd(t, append(lib,
		"build", "--image", "alice/app", "-m", "DMG create details", src)...)
	if err != nil {
		t.Fatalf("dmg create: %v\n%s", err, stderr)
	}
	assertVersionDescription(t, imagesDir, "alice/app", "DMG create details")

	_, _, _, err = runCmd(t, append(lib,
		"build", "--image", "alice/app", "-m=", src)...)
	if err == nil || !strings.Contains(err.Error(), "use images describe") {
		t.Fatalf("duplicate dmg create description conflict = %v", err)
	}
}

func TestDMGBakeForwardsDescriptionAndRejectsDuplicateChange(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	legacyBuild = false
	useDarwinHost(t)
	installHdiutil(t, []byte("base-dmg"))
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")
	stdout, stderr, _, err := runCmd(t, append(lib,
		"build", "--image", "alice/app", src)...)
	if err != nil {
		t.Fatalf("initial dmg: %v\n%s", err, stderr)
	}
	baseID := buildOutput(t, stdout)
	shadow := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper.shadow")
	writeTestFile(t, shadow, "live-shadow")
	basePath := filepath.Join(imagesDir, "alice", "app", "_objects", baseID, image.ImageDMG)
	loadMountTable = func() ([]mountinfo.Mount, error) {
		return []mountinfo.Mount{{Point: work, FSType: "fuse.dmg", Source: basePath, Upper: shadow}}, nil
	}
	action := libraryAction("alice/app", work, session.ModeRW)
	action.FullID, action.TipID = baseID, baseID
	writeSessionRecord(t, sessionsDir(home), "dev", action)
	t.Chdir(work)
	installHdiutil(t, []byte("baked-dmg"))

	_, stderr, err = runCmdInput(t, "", append(lib,
		"build", "-F", "-", "--image", "alice/app")...)
	if err != nil {
		t.Fatalf("dmg bake: %v\n%s", err, stderr)
	}
	assertVersionDescription(t, imagesDir, "alice/app", "")

	_, _, _, err = runCmd(t, append(lib,
		"build", "-m", "Changed", "--image", "alice/app")...)
	if err == nil || !strings.Contains(err.Error(), "use images describe") {
		t.Fatalf("duplicate dmg bake description conflict = %v", err)
	}
}

func assertVersionDescription(t *testing.T, imagesDir, name, want string) {
	t.Helper()
	repo, err := image.OpenRepo(imagesDir, name)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := image.LoadVersionManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.Images[len(manifest.Images)-1].Description; got != want {
		t.Fatalf("description = %q, want %q", got, want)
	}
}

func TestDMGViewBakeAcceptsEquivalentDirs(t *testing.T) {
	forms := []struct {
		name   string
		arg    string
		parent bool
	}{
		{name: "omitted"},
		{name: "dot", arg: "."},
		{name: "absolute", arg: "abs"},
		{name: "absolute from parent", arg: "abs", parent: true},
	}
	for _, form := range forms {
		t.Run(form.name, func(t *testing.T) {
			home, work := withWorkspace(t)
			imagesDir, lib := libFlags(home)
			old := []byte("old-dmg")
			meta := writeHashedDMGFixture(t, imagesDir, old)
			shadow := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper.shadow")
			if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(shadow, []byte("live-shadow"), 0o644); err != nil {
				t.Fatal(err)
			}
			withBuildSeams(t, []mountinfo.Mount{{
				Point:  work,
				FSType: "fuse.dmg",
				Source: filepath.Join(imagesDir, "alice", "app", image.ImageDMG),
				Upper:  shadow,
			}})
			useDarwinHost(t)
			forbidRebase(t)
			calls := installHdiutil(t, []byte("baked-dmg"))
			action := libraryAction("alice/app", work, session.ModeRW)
			action.FullID = manifestRootID(meta)
			writeSessionRecord(t, sessionsDir(home), "dev", action)
			if form.parent {
				t.Chdir(home)
			}
			args := append(lib, "build", "--image", "alice/app")
			switch form.arg {
			case ".":
				args = append(args, ".")
			case "abs":
				args = append(args, work)
			}
			stdout, stderr, _, err := runCmd(t, args...)
			if err != nil {
				t.Fatalf("build: %v\n%s", err, stderr)
			}
			if !strings.HasPrefix(stdout, "full alice/app ") || strings.Contains(stdout, "incr ") {
				t.Fatalf("stdout = %q", stdout)
			}
			if !strings.Contains(stderr, "dmg has no incremental layer") {
				t.Fatalf("stderr = %q", stderr)
			}
			joined := strings.Join((*calls)[0], " ")
			if len(*calls) != 1 || !strings.Contains(joined, "-shadow "+shadow) {
				t.Fatalf("calls = %q", *calls)
			}
			assertFileText(t, shadow, "live-shadow")
			assertFileText(t, filepath.Join(imagesDir, "alice", "app", image.ImageDMG), "baked-dmg")
		})
	}
}

func TestDMGViewBakeArchivesUnreferencedImage(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	old := []byte("old-dmg")
	meta := writeHashedDMGFixture(t, imagesDir, old)
	shadow := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper.shadow")
	if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("live-shadow"), 0o644); err != nil {
		t.Fatal(err)
	}
	withBuildSeams(t, []mountinfo.Mount{{
		Point: work, FSType: "fuse.dmg", Upper: shadow,
		Source: filepath.Join(imagesDir, "alice", "app", image.ImageDMG),
	}})
	useDarwinHost(t)
	forbidRebase(t)
	calls := installHdiutil(t, []byte("baked-dmg"))
	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "alice/app")...)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, stderr)
	}
	if !strings.HasPrefix(stdout, "full alice/app ") {
		t.Fatalf("stdout = %q", stdout)
	}
	// No session record pins a base here, so the keeps-old-base
	// warning prints its id-less variant; the golden pins the exact
	// text of that branch.
	assertGolden(t, "dmg_bake_warning_unpinned", []byte(stderr))
	if len(*calls) != 1 || !strings.Contains(strings.Join((*calls)[0], " "), "-shadow "+shadow) {
		t.Fatalf("calls = %q", *calls)
	}
	assertFileText(t, shadow, "live-shadow")
	assertFileText(t, filepath.Join(imagesDir, "alice", "app", image.ImageDMG), "baked-dmg")
	assertFileText(t, filepath.Join(imagesDir, "alice", "app", "_archives", manifestRootID(meta), image.ImageDMG), "old-dmg")
	if _, statErr := os.Stat(filepath.Join(imagesDir, "alice", "app", "_hold")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unreferenced bake held the image: %v", statErr)
	}
}

func TestDMGBakeFailureLeavesImageAndShadow(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	writeHashedDMGFixture(t, imagesDir, []byte("old-dmg"))
	shadow := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper.shadow")
	if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("live-shadow"), 0o644); err != nil {
		t.Fatal(err)
	}
	withBuildSeams(t, []mountinfo.Mount{{
		Point: work, FSType: "fuse.dmg", Upper: shadow,
		Source: filepath.Join(imagesDir, "alice", "app", image.ImageDMG),
	}})
	useDarwinHost(t)
	prev := hdiutilTool
	hdiutilTool = func(string, []string, ...string) error {
		return errors.New("hdiutil: convert failed")
	}
	t.Cleanup(func() { hdiutilTool = prev })
	_, stderr, _, err := runCmd(t, append(lib, "build", "-i", "--image", "alice/app")...)
	if err == nil || !strings.Contains(err.Error()+"\n"+stderr, "convert failed") {
		t.Fatalf("err = %v\n%s", err, stderr)
	}
	assertFileText(t, filepath.Join(imagesDir, "alice", "app", image.ImageDMG), "old-dmg")
	assertFileText(t, shadow, "live-shadow")
	if _, statErr := os.Stat(filepath.Join(imagesDir, "alice", "app", "_archives")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed bake archived the image: %v", statErr)
	}
}

func TestImagesGCLeavesSessionShadow(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	shadow := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper.shadow")
	if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("keep-shadow"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSessionRecord(t, sessionsDir(home), "dev",
		libraryAction("alice/app", filepath.Join(work, "view"), session.ModeRW))
	if _, stderr, _, err := runCmd(t, append(lib, "images", "gc")...); err != nil {
		t.Fatalf("gc: %v\n%s", err, stderr)
	}
	assertFileText(t, shadow, "keep-shadow")
}

func TestImagesRemoveRefusesAttachedDMG(t *testing.T) {
	t.Skip("isolated legacy DMG parser fixture")
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	writeHashedDMGFixture(t, imagesDir, []byte("live-dmg"))
	imagePath := filepath.Join(imagesDir, "alice", "app", image.ImageDMG)
	restoreDiskSeams(t)
	readHdiutilInfo = func() ([]byte, error) {
		return hdiutilStatusPlist([]statusVolume{{
			image:  imagePath,
			point:  filepath.Join(home, "view"),
			shadow: filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper.shadow"),
		}}), nil
	}
	for i := 0; i < 2; i++ {
		_, stderr, _, err := runCmd(t, append(lib, "images", "rm", "alice/app")...)
		if err == nil || !strings.Contains(err.Error()+"\n"+stderr, "still attached") {
			t.Fatalf("call %d: %v\n%s", i, err, stderr)
		}
	}
	assertFileText(t, imagePath, "live-dmg")
}

func TestBuildFromDiskInfoInfersUnresolvedSession(t *testing.T) {
	t.Run("omitted image", func(t *testing.T) {
		imagesDir, lib, shadow, resolved := diskInfoSession(t)
		forbidRebase(t)
		calls := installHdiutil(t, []byte("from-session"))
		t.Chdir(resolved)
		stdout, stderr, _, err := runCmd(t, append(lib, "build")...)
		if err != nil {
			t.Fatalf("build: %v\n%s", err, stderr)
		}
		if !strings.HasPrefix(stdout, "full alice/app ") {
			t.Fatalf("stdout = %q", stdout)
		}
		if len(*calls) != 1 {
			t.Fatalf("calls = %q", *calls)
		}
		joined := strings.Join((*calls)[0], " ")
		wantImage := filepath.Join(imagesDir, "alice", "app", image.ImageDMG)
		if !strings.Contains(joined, "-shadow "+shadow) || !strings.Contains(joined, wantImage) {
			t.Fatalf("calls = %q", *calls)
		}
		if strings.Contains(joined, "mv ") || strings.Contains(joined, "rm ") {
			t.Fatalf("bake renamed or removed the shadow: %q", (*calls)[0])
		}
		assertFileText(t, shadow, "info-shadow")
	})
	t.Run("different image packs the path", func(t *testing.T) {
		imagesDir, lib, shadow, resolved := diskInfoSession(t)
		forbidRebase(t)
		calls := installHdiutil(t, []byte("diverted-from-view"))
		t.Chdir(resolved)
		stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "bob/other")...)
		if err != nil {
			t.Fatalf("divert: %v\n%s", err, stderr)
		}
		if !strings.HasPrefix(stdout, "full bob/other ") {
			t.Fatalf("stdout = %q", stdout)
		}
		if len(*calls) != 1 || !strings.Contains(strings.Join((*calls)[0], " "), "-srcfolder") {
			t.Fatalf("calls = %q", *calls)
		}
		assertFileText(t, filepath.Join(imagesDir, "alice", "app", image.ImageDMG), "info-base")
		assertFileText(t, shadow, "info-shadow")
		assertFileText(t, filepath.Join(imagesDir, "bob", "other", image.ImageDMG), "diverted-from-view")
	})
}

// diskInfoSession is a writable dmg view whose hdiutil mount point is
// symlink-resolved and whose session record keeps filepath.Abs.
func diskInfoSession(t *testing.T) (imagesDir string, lib []string, shadow, resolved string) {
	t.Helper()
	home, _ := withWorkspace(t)
	imagesDir, lib = libFlags(home)
	meta := writeHashedDMGFixture(t, imagesDir, []byte("info-base"))
	shadow = filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper.shadow")
	if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("info-shadow"), 0o644); err != nil {
		t.Fatal(err)
	}
	recorded, resolved := symlinkView(t, home)
	action := libraryAction("alice/app", recorded, session.ModeRW)
	action.FullID = manifestRootID(meta)
	writeSessionRecord(t, sessionsDir(home), "dev", action)
	withBuildSeams(t, nil)
	useDarwinHost(t)
	restoreDiskSeams(t)
	loadMountTable = func() ([]mountinfo.Mount, error) {
		return nil, mountinfo.ErrUnavailable
	}
	imagePath := filepath.Join(imagesDir, "alice", "app", image.ImageDMG)
	readHdiutilInfo = func() ([]byte, error) {
		return hdiutilStatusPlist([]statusVolume{{
			image:  imagePath,
			shadow: shadow,
			point:  resolved,
		}}), nil
	}
	return imagesDir, lib, shadow, resolved
}

func symlinkView(t *testing.T, home string) (recorded, resolved string) {
	t.Helper()
	concrete := filepath.Join(home, "private", "view")
	if err := os.MkdirAll(concrete, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "tmp", "view")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(concrete, link); err != nil {
		t.Fatal(err)
	}
	var err error
	recorded, err = filepath.Abs(link)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err = filepath.EvalSymlinks(recorded)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == recorded {
		t.Fatal("symlink fixture resolved to the recorded mount point")
	}
	return recorded, resolved
}

func TestMountedFullIDUsesSessionShadow(t *testing.T) {
	home, _ := withWorkspace(t)
	sessions := sessionsDir(home)
	action := libraryAction("alice/app", "/view", session.ModeRW)
	action.FullID = "abcdef0123456789"
	writeSessionRecord(t, sessions, "dev", action)
	shadow := filepath.Join(sessions, "dev", "runtime", "m1", "upper.shadow")
	id, err := mountedFullID(sessions, shadow)
	if err != nil || id != action.FullID {
		t.Fatalf("id = %q %v", id, err)
	}
	id, err = mountedFullID(sessions, filepath.Join(sessions, "dev", "upper.shadow"))
	if err != nil || id != "" {
		t.Fatalf("unrelated shadow = %q %v", id, err)
	}
}

func TestBuildUsesHdiutilInfoWhenMountTableIsUnavailable(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	writeHashedDMGFixture(t, imagesDir, []byte("info-base"))
	shadow := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper.shadow")
	if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("info-shadow"), 0o644); err != nil {
		t.Fatal(err)
	}
	withBuildSeams(t, nil)
	useDarwinHost(t)
	forbidRebase(t)
	restoreDiskSeams(t)
	loadMountTable = func() ([]mountinfo.Mount, error) {
		return nil, mountinfo.ErrUnavailable
	}
	readHdiutilInfo = func() ([]byte, error) {
		return hdiutilStatusPlist([]statusVolume{{
			image:  filepath.Join(imagesDir, "alice", "app", image.ImageDMG),
			shadow: shadow,
			point:  work,
		}}), nil
	}
	calls := installHdiutil(t, []byte("from-info"))
	stdout, stderr, _, err := runCmd(t, append(lib, "build", "-i", "--image", "alice/app")...)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, stderr)
	}
	if !strings.HasPrefix(stdout, "full alice/app ") {
		t.Fatalf("stdout = %q", stdout)
	}
	if len(*calls) != 1 || !strings.Contains(strings.Join((*calls)[0], " "), "-shadow "+shadow) {
		t.Fatalf("calls = %q", *calls)
	}
	assertFileText(t, shadow, "info-shadow")
}

func TestDirectoryBuildWhenDiskInfoIsEmpty(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	useDarwinHost(t)
	restoreDiskSeams(t)
	loadMountTable = func() ([]mountinfo.Mount, error) {
		return nil, mountinfo.ErrUnavailable
	}
	readHdiutilInfo = func() ([]byte, error) {
		return []byte(`<plist version="1.0"><dict><key>images</key><array></array></dict></plist>`), nil
	}
	calls := installHdiutil(t, []byte("plain-dmg"))
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")
	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "alice/app", src)...)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, stderr)
	}
	if !strings.HasPrefix(stdout, "full alice/app ") {
		t.Fatalf("stdout = %q", stdout)
	}
	got := []string{}
	if calls != nil && len(*calls) == 1 {
		got = (*calls)[0]
	}
	srcArg := ""
	for i, arg := range got {
		if arg == "-srcfolder" && i+1 < len(got) {
			srcArg = got[i+1]
		}
	}
	if srcArg != src && srcArg != resolveBuildPath(src) {
		t.Fatalf("srcfolder = %q, calls = %q", srcArg, got)
	}
	assertFileText(t, filepath.Join(imagesDir, "alice", "app", image.ImageDMG), "plain-dmg")
}

func TestReadOnlyDiskInfoRefusesPublish(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	writeHashedDMGFixture(t, imagesDir, []byte("ro-dmg"))
	withBuildSeams(t, nil)
	restoreDiskSeams(t)
	loadMountTable = func() ([]mountinfo.Mount, error) {
		return nil, mountinfo.ErrUnavailable
	}
	readHdiutilInfo = func() ([]byte, error) {
		return hdiutilStatusPlist([]statusVolume{{
			image: filepath.Join(imagesDir, "alice", "app", image.ImageDMG),
			point: work,
		}}), nil
	}
	prev := hdiutilTool
	hdiutilTool = func(string, []string, ...string) error {
		t.Fatal("read-only view ran hdiutil")
		return nil
	}
	t.Cleanup(func() { hdiutilTool = prev })
	_, stderr, _, err := runCmd(t, append(lib, "build", "--full", "--image", "alice/app")...)
	if ExitCode(err) != 2 || !strings.Contains(err.Error()+"\n"+stderr, "read-only dmg") {
		t.Fatalf("ExitCode(%v) = %d\n%s", err, ExitCode(err), stderr)
	}
}

// TestDMGViewBakeTwiceKeepsRecordedBase proves the repeated-bake
// semantics survive the warning: the record keeps the old base id,
// so mountedFullID keeps answering with it and the second bake still
// converts the held copy of that base instead of the newer active.
// A third round idling against the last convert publishes nothing
// and keeps the keeps-old-base warning silent.
func TestDMGViewBakeTwiceKeepsRecordedBase(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	meta := writeHashedDMGFixture(t, imagesDir, []byte("old-dmg"))
	oldID := manifestRootID(meta)
	shadow := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper.shadow")
	if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("shadow-one"), 0o644); err != nil {
		t.Fatal(err)
	}
	withBuildSeams(t, []mountinfo.Mount{{
		Point: work, FSType: "fuse.dmg", Upper: shadow,
		Source: filepath.Join(imagesDir, "alice", "app", image.ImageDMG),
	}})
	useDarwinHost(t)
	forbidRebase(t)
	calls := installSeqHdiutil(t, []byte("baked-one"), []byte("baked-two"))
	action := libraryAction("alice/app", work, session.ModeRW)
	action.FullID = oldID
	writeSessionRecord(t, sessionsDir(home), "dev", action)

	holdBase := filepath.Join(imagesDir, "alice", "app", "_hold", oldID, image.ImageDMG)
	wants := []string{dmgID("baked-one"), dmgID("baked-two")}
	for i, want := range wants {
		stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "alice/app")...)
		if err != nil {
			t.Fatalf("bake %d: %v\n%s", i+1, err, stderr)
		}
		if got := "full alice/app " + want + "\n"; stdout != got {
			t.Fatalf("bake %d stdout = %q, want %q", i+1, stdout, got)
		}
		if !strings.Contains(stderr, "the session record keeps "+oldID) {
			t.Fatalf("bake %d stderr = %q", i+1, stderr)
		}
		// The record never moves, so mountedFullID keeps answering
		// with the recorded old base for the next bake.
		assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"), oldID, "", "", "")
		if id, idErr := mountedFullID(sessionsDir(home), shadow); idErr != nil || id != oldID {
			t.Fatalf("bake %d: mountedFullID = %q, %v", i+1, id, idErr)
		}
		// A fresh shadow delta keeps the next bake off the idle path.
		if err := os.WriteFile(shadow, []byte("shadow-two"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if len(*calls) != 2 {
		t.Fatalf("calls = %q", *calls)
	}
	// The first bake converts the active image; the second one must
	// read the held copy of the recorded old base, not the newer
	// active image the first bake published.
	if got := strings.Join((*calls)[1], " "); !strings.Contains(got, holdBase) {
		t.Fatalf("second bake base = %q, want %q", (*calls)[1], holdBase)
	}
	assertFileText(t, holdBase, "old-dmg")
	assertFileText(t, filepath.Join(imagesDir, "alice", "app", image.ImageDMG), "baked-two")
	assertFileText(t, shadow, "shadow-two")

	// A third round with the untouched shadow idles: the content
	// fingerprint matches the last convert, so BuildDMG answers
	// with the active image itself and nothing was published. The
	// keeps-old-base warning would claim a publish that never
	// happened, so it stays silent and hdiutil never runs.
	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "alice/app")...)
	if err != nil {
		t.Fatalf("idle bake: %v\n%s", err, stderr)
	}
	if want := "full alice/app " + dmgID("baked-two") + "\n"; stdout != want {
		t.Fatalf("idle stdout = %q, want %q", stdout, want)
	}
	if !strings.Contains(stderr, "dmg has no incremental layer") ||
		strings.Contains(stderr, "published new image") {
		t.Fatalf("idle stderr = %q", stderr)
	}
	if len(*calls) != 2 {
		t.Fatalf("idle bake ran hdiutil: %q", *calls)
	}
	assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"), oldID, "", "", "")
	assertFileText(t, filepath.Join(imagesDir, "alice", "app", image.ImageDMG), "baked-two")
}

// TestReadOnlyViewDMGCreateWarnsAndKeepsRecord covers the dmg create
// path from inside a mounted view: a read-only view of a non-dmg
// source packs as a dmg on a darwin host with --full (a read-only
// dmg view itself is refused before publishing). The record's pin
// diverges from the active root — a later publish moved the root —
// so the warning must quote the id the session record pins for the
// mounted view, not the active manifest root a fresh mount would
// use. Like a bake, the create never rebases: the record keeps the
// old id and the warning text is pinned by a golden. An idle rerun
// publishes nothing and stays silent.
func TestReadOnlyViewDMGCreateWarnsAndKeepsRecord(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	meta := writeHashedDMGFixture(t, imagesDir, []byte("ro-base"))
	oldID := manifestRootID(meta)
	// A later publish moves the active root past the record's pin:
	// the view is still served by oldID while a fresh mount would
	// use movedID.
	moved := writeHashedDMGFixture(t, imagesDir, []byte("moved-base"))
	movedID := manifestRootID(moved)
	merged := filepath.Join(work, "merged")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(merged, "f.txt"), "x")
	payload := filepath.Join(work, "base.sqsh")
	body := make([]byte, 600)
	copy(body, []byte("hsqs-fake"))
	writeTestFile(t, payload, string(body))
	withBuildSeams(t, []mountinfo.Mount{{
		Point:  merged,
		FSType: "fuse",
		Source: payload,
	}})
	useDarwinHost(t)
	forbidRebase(t)
	calls := installHdiutil(t, []byte("created-dmg"))
	action := libraryAction("alice/app", merged, session.ModeRO)
	action.FullID = oldID
	writeSessionRecord(t, sessionsDir(home), "dev", action)
	t.Chdir(merged)

	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--full", "--image", "alice/app")...)
	if err != nil {
		t.Fatalf("build --full: %v\n%s", err, stderr)
	}
	newID := dmgID("created-dmg")
	if want := "full alice/app " + newID + "\n"; stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	if len(*calls) != 1 || !strings.Contains(strings.Join((*calls)[0], " "), "-srcfolder "+merged) {
		t.Fatalf("calls = %q", *calls)
	}
	// The golden pins the warning to the record's pin: the diverged
	// active root movedID must not appear in it.
	assertGolden(t, "dmg_create_warning", []byte(stderr))
	if strings.Contains(stderr, movedID) {
		t.Fatalf("warning quoted the active root instead of the record: %s", stderr)
	}
	// The create never rebases: the record keeps the pinned id and
	// gains no served stack.
	assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"), oldID, "", "", "")
	assertFileText(t, filepath.Join(imagesDir, "alice", "app", image.ImageDMG), "created-dmg")
	// The diverged active root was pinned by no record, so the
	// publish archived it instead of holding it.
	assertFileText(t, filepath.Join(imagesDir, "alice", "app", "_archives", movedID, image.ImageDMG), "moved-base")
	if _, statErr := os.Stat(filepath.Join(imagesDir, "alice", "app", "_hold")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unreferenced active root was held: %v", statErr)
	}

	// An idle rerun publishes nothing: the source directory already
	// matches the active image, so BuildDMG answers with that image
	// itself and the keeps-old-base warning stays silent.
	stdout, stderr, _, err = runCmd(t, append(lib, "build", "--full", "--image", "alice/app")...)
	if err != nil {
		t.Fatalf("idle build --full: %v\n%s", err, stderr)
	}
	if want := "full alice/app " + newID + "\n"; stdout != want {
		t.Fatalf("idle stdout = %q, want %q", stdout, want)
	}
	if stderr != "" {
		t.Fatalf("idle build warned: %s", stderr)
	}
	if len(*calls) != 1 {
		t.Fatalf("idle build ran hdiutil: %q", *calls)
	}
	assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"), oldID, "", "", "")
}

// TestDMGBakeThenReplayAttachesRecordedBase chains the dmg exception
// end to end where dmg is registered: two in-view bakes publish new
// images while the record keeps the base the live attach serves,
// and a replay of the dead view attaches that recorded base again —
// resolved through the hold leaf the first bake parked it in — with
// no prune request, because a dmg attach has no upper directory.
// The cross-platform bake coverage lives in
// TestDMGViewBakeTwiceKeepsRecordedBase.
func TestDMGBakeThenReplayAttachesRecordedBase(t *testing.T) {
	if _, err := container.Lookup(image.FormatDMG); err != nil {
		t.Skip("dmg is not registered on this OS")
	}
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	meta := writeHashedDMGFixture(t, imagesDir, []byte("old-dmg"))
	oldID := manifestRootID(meta)
	shadow := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper.shadow")
	if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("shadow-one"), 0o644); err != nil {
		t.Fatal(err)
	}
	withBuildSeams(t, []mountinfo.Mount{{
		Point: work, FSType: "fuse.dmg", Upper: shadow,
		Source: filepath.Join(imagesDir, "alice", "app", image.ImageDMG),
	}})
	useDarwinHost(t)
	forbidRebase(t)
	installSeqHdiutil(t, []byte("baked-one"), []byte("baked-two"))
	action := libraryAction("alice/app", work, session.ModeRW)
	action.FullID = oldID
	writeSessionRecord(t, sessionsDir(home), "dev", action)

	// Two bakes from the view: both succeed, both warn that the
	// record keeps the old base, and the record never moves.
	for i := 0; i < 2; i++ {
		stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "alice/app")...)
		if err != nil {
			t.Fatalf("bake %d: %v\n%s", i+1, err, stderr)
		}
		if !strings.HasPrefix(stdout, "full alice/app ") {
			t.Fatalf("bake %d stdout = %q", i+1, stdout)
		}
		if !strings.Contains(stderr, "the session record keeps "+oldID) {
			t.Fatalf("bake %d stderr = %q", i+1, stderr)
		}
		assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"), oldID, "", "", "")
		// A fresh shadow delta keeps the next bake off the idle path.
		if err := os.WriteFile(shadow, []byte("shadow-two"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// The attach dies behind the record's back: the replay resolves
	// the recorded base through the hold leaf and never asks for a
	// prune.
	f := withMountSeams(t, nil)
	pinPortableHealth(t, f)
	if _, stderr, _, err := runCmd(t, append(lib, "session", "open", "dev")...); err != nil {
		t.Fatalf("session open: %v\n%s", err, stderr)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("engine calls = %d, want 1", len(f.mountReqs))
	}
	req := f.mountReqs[0]
	want := filepath.Join(imagesDir, "alice", "app", "_hold", oldID, image.ImageDMG)
	if req.Full != want {
		t.Fatalf("Full = %s, want the held recorded base %s", req.Full, want)
	}
	if req.PruneUpper {
		t.Fatal("PruneUpper = true, want a dmg replay to keep the flag clear")
	}
	// The replay of a dmg view reattaches the recorded base and the
	// record keeps pinning nothing else.
	assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"), oldID, "", "", "")
}
