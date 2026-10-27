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
	if err := repo.Publish(body, publishManifest(meta), nil); err != nil {
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
	if strings.Contains(stderr, "incremental") {
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
	meta := image.Meta{
		ID:        sum,
		Name:      "alice/app",
		Version:   image.MetaVersion,
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

// publishManifest converts a v1 fixture meta into the single-node
// v2 manifest Repo.Publish accepts.
func publishManifest(meta image.Meta) image.Manifest {
	return image.Manifest{
		Version: image.ManifestVersion,
		Name:    meta.Name,
		Format:  meta.Format,
		Head:    meta.ID,
		Images: []image.ImageMeta{{
			ID:            meta.ID,
			Kind:          image.KindFull,
			Size:          meta.Size,
			SHA256:        meta.SHA256,
			MtimeUnix:     meta.MtimeUnix,
			CreatedAt:     meta.CreatedAt,
			Builder:       meta.Builder,
			ContentSHA256: meta.ContentSHA256,
		}},
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
		Name:      "alice/app",
		Version:   image.MetaVersion,
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
			calls := installHdiutil(t, []byte("baked-dmg"))
			action := libraryAction("alice/app", work, session.ModeRW)
			action.FullID = meta.ID
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
			assertFileText(t, filepath.Join(imagesDir, "alice", "app", "_hold", meta.ID, image.ImageDMG), "old-dmg")
			if _, statErr := os.Stat(filepath.Join(imagesDir, "alice", "app", "_archives")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("active session archived the pinned image: %v", statErr)
			}
			rec, err := (session.Store{Dir: sessionsDir(home)}).Load("dev")
			if err != nil {
				t.Fatal(err)
			}
			if rec.Actions[0].FullID != action.FullID {
				t.Fatalf("session full id = %s, want %s", rec.Actions[0].FullID, action.FullID)
			}
		})
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
			calls := installHdiutil(t, []byte("baked-dmg"))
			action := libraryAction("alice/app", work, session.ModeRW)
			action.FullID = meta.ID
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
	calls := installHdiutil(t, []byte("baked-dmg"))
	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "alice/app")...)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, stderr)
	}
	if !strings.HasPrefix(stdout, "full alice/app ") {
		t.Fatalf("stdout = %q", stdout)
	}
	if len(*calls) != 1 || !strings.Contains(strings.Join((*calls)[0], " "), "-shadow "+shadow) {
		t.Fatalf("calls = %q", *calls)
	}
	assertFileText(t, shadow, "live-shadow")
	assertFileText(t, filepath.Join(imagesDir, "alice", "app", image.ImageDMG), "baked-dmg")
	assertFileText(t, filepath.Join(imagesDir, "alice", "app", "_archives", meta.ID, image.ImageDMG), "old-dmg")
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
	action.FullID = meta.ID
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
