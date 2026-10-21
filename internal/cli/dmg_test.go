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
)

func TestImagesShowDMGFromMeta(t *testing.T) {
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

func TestDarwinBuildDefaultDoesNotPackSquashfs(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	useDarwinHost(t)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")
	_, stderr, _, err := runCmd(t, append(lib, "build", "--image", "alice/app", src)...)
	if err == nil {
		t.Fatal("dmg packing is not implemented; build succeeded")
	}
	msg := err.Error() + "\n" + stderr
	if !strings.Contains(msg, "dmg") && !strings.Contains(msg, "macOS") {
		t.Fatalf("build err = %v\n%s", err, stderr)
	}
	if _, statErr := os.Stat(filepath.Join(imagesDir, "alice", "app", image.ImageFile)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("darwin default left %s: %v", image.ImageFile, statErr)
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

func TestReadOnlyDMGSourceRefusesDifferentImage(t *testing.T) {
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
	_, stderr, _, err := runCmd(t, append(lib, "build", "--full", "--image", "bob/other")...)
	if ExitCode(err) != 2 || !strings.Contains(err.Error()+"\n"+stderr, "read-only dmg") {
		t.Fatalf("ExitCode(%v) = %d\n%s", err, ExitCode(err), stderr)
	}
	published := filepath.Join(imagesDir, "bob", "other", image.ImageFile)
	if _, statErr := os.Stat(published); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("different image was published: %v", statErr)
	}
}

func TestReadOnlyKolySourceRefusesDifferentImage(t *testing.T) {
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
	meta := image.Meta{
		ID:        sum[:16],
		Owner:     "alice",
		Repo:      "app",
		Kind:      image.KindFull,
		Format:    image.Format{Type: image.FormatDMG, Compression: compression},
		Size:      info.Size(),
		SHA256:    sum,
		MtimeUnix: info.ModTime().Unix(),
		CreatedAt: time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC),
		Builder:   "test",
	}
	if len(meta.Chain) != 0 {
		t.Fatal("fixture chain is not empty")
	}
	if err := image.Store(filepath.Join(dir, "meta.yaml"), meta); err != nil {
		t.Fatal(err)
	}
}

func writeHashedDMGFixture(t *testing.T, imagesDir string, body []byte) image.Meta {
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
	meta := image.Meta{
		ID:        image.ID(sum),
		Owner:     "alice",
		Repo:      "app",
		Kind:      image.KindFull,
		Format:    image.Format{Type: image.FormatDMG, Compression: "ulfo"},
		Size:      info.Size(),
		SHA256:    hex.EncodeToString(sum[:]),
		MtimeUnix: info.ModTime().Unix(),
		CreatedAt: time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC),
		Builder:   "test",
	}
	if len(meta.Chain) != 0 {
		t.Fatal("fixture chain is not empty")
	}
	if err := image.Store(filepath.Join(dir, "meta.yaml"), meta); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, image.ImageFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fixture must not include image.sqsh")
	}
	return meta
}
