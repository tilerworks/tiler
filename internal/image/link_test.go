package image

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
)

func withLinuxLinkPolicy(t *testing.T) {
	t.Helper()
	prev := linkDarwinHost
	linkDarwinHost = func() bool { return false }
	t.Cleanup(func() { linkDarwinHost = prev })
}

func linkImagesRoot(repo Repo) string {
	dir := repo.Dir
	for range repo.Segments {
		dir = filepath.Dir(dir)
	}
	return dir
}

func TestLinkExternalSuccess(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	src := writeFakeSquashfs(t, nas, []byte("vendor-payload"))
	if err := LinkExternal(images, logsFor(images), "alice/app", src, "test-builder"); err != nil {
		t.Fatal(err)
	}
	repo, err := OpenRepo(images, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadVersionManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	root := manifest.Images[0]
	payload, err := repo.ObjectPayloadPath(root)
	if err != nil {
		t.Fatal(err)
	}
	target, ok, err := readExternalLinkTarget(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || target != src {
		t.Fatalf("symlink target = %q, want %q", target, src)
	}
	sum := sha256.Sum256(append([]byte("hsqs"), []byte("vendor-payload")...))
	want := hex.EncodeToString(sum[:])
	if root.SHA256 != want || root.ID != want {
		t.Fatalf("meta digest = %s id = %s", root.SHA256, root.ID)
	}
	if root.Format.Type != FormatSquashfs || root.Builder != "test-builder" {
		t.Fatalf("meta = %+v", manifest)
	}
	if len(manifest.Images) != 1 {
		t.Fatalf("chain = %+v", manifest.Images)
	}
}

func TestLinkExternalDuplicateContentKeepsOriginalSource(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	src := writeFakeSquashfs(t, nas, []byte("indexed-payload"))
	if err := LinkExternal(images, logsFor(images), "alice/app", src, "test-builder"); err != nil {
		t.Fatal(err)
	}
	repo, err := OpenRepo(images, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	duplicate := filepath.Join(t.TempDir(), "duplicate.sqsh")
	body, _ := os.ReadFile(src)
	if err := os.WriteFile(duplicate, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := LinkExternal(images, logsFor(images), "alice/app", duplicate, "other-builder"); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadVersionManifest(repo)
	if err != nil || len(manifest.Images) != 1 {
		t.Fatalf("manifest = %#v, %v", manifest, err)
	}
	payload, _ := repo.ObjectPayloadPath(manifest.Images[0])
	if target, ok, err := readExternalLinkTarget(payload); err != nil || !ok || target != src {
		t.Fatalf("target = %q, %v", target, err)
	}
}

func TestLinkExternalIdempotent(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	src := writeFakeSquashfs(t, nas, []byte("same-bytes"))
	if err := LinkExternal(images, logsFor(images), "alice/app", src, "test-builder"); err != nil {
		t.Fatal(err)
	}
	repo, err := OpenRepo(images, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := LinkExternal(images, logsFor(images), "alice/app", src, "other-builder"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("idempotent link rewrote meta")
	}
}

func TestLinkExternalAddsIndependentFullVersions(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	src := writeFakeSquashfs(t, nas, []byte("chained-bytes"))
	if err := LinkExternal(images, logsFor(images), "alice/app", src, "test-builder"); err != nil {
		t.Fatal(err)
	}
	repo, err := OpenRepo(images, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	other := writeFakeSquashfs(t, nas, []byte("other-bytes"))
	if err := LinkExternal(images, logsFor(images), "alice/app", other, "test"); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadVersionManifest(repo)
	if err != nil || len(manifest.Images) != 2 {
		t.Fatalf("versions = %#v, %v", manifest.Images, err)
	}
}

func TestLinkExternalConflictDifferentTarget(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	first := writeFakeSquashfs(t, nas, []byte("first"))
	second := writeFakeSquashfs(t, nas, []byte("second"))
	if err := LinkExternal(images, logsFor(images), "alice/app", first, "test"); err != nil {
		t.Fatal(err)
	}
	if err := LinkExternal(images, logsFor(images), "alice/app", second, "test"); err != nil {
		t.Fatal(err)
	}
	repo, _ := OpenRepo(images, "alice/app")
	manifest, err := LoadVersionManifest(repo)
	if err != nil || len(manifest.Images) != 2 {
		t.Fatalf("versions = %#v, %v", manifest.Images, err)
	}
}

func TestLinkExternalRefusesReservedPath(t *testing.T) {
	withLinuxLinkPolicy(t)
	data := t.TempDir()
	images := filepath.Join(data, "images")
	if err := os.MkdirAll(images, 0o755); err != nil {
		t.Fatal(err)
	}
	inside := writeFakeSquashfs(t, images, []byte("inside-library"))
	err := LinkExternal(images, logsFor(images), "alice/app", inside, "test")
	if err == nil || !errors.Is(err, config.ErrReservedPath) {
		t.Fatalf("error = %v", err)
	}
}

func TestLinkExternalRefusesForeignFormat(t *testing.T) {
	withLinuxLinkPolicy(t)
	nas := t.TempDir()
	src := writeFakeDMG(t, nas, []byte("disk-image"))
	err := LinkExternal(t.TempDir(), logsFor(t.TempDir()), "alice/app", src, "test")
	if err == nil || !strings.Contains(err.Error(), "dmg is not available") {
		t.Fatalf("error = %v", err)
	}
}

func TestLinkExternalRefusesSquashfsOnDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("use host override test")
	}
	prev := linkDarwinHost
	linkDarwinHost = func() bool { return true }
	t.Cleanup(func() { linkDarwinHost = prev })

	nas := t.TempDir()
	src := writeFakeSquashfs(t, nas, []byte("linux-image"))
	err := LinkExternal(t.TempDir(), logsFor(t.TempDir()), "alice/app", src, "test")
	if err == nil || !strings.Contains(err.Error(), "not available on macOS") {
		t.Fatalf("error = %v", err)
	}
}

func writeFakeSquashfs(t *testing.T, dir string, payload []byte) string {
	t.Helper()
	sum := sha256.Sum256(payload)
	path := filepath.Join(dir, hex.EncodeToString(sum[:8])+".sqsh")
	body := append([]byte{'h', 's', 'q', 's'}, payload...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFakeDMG(t *testing.T, dir string, payload []byte) string {
	t.Helper()
	path := filepath.Join(dir, "external.dmg")
	const total = 1024
	body := make([]byte, total)
	copy(body, payload)
	body[512] = 'k'
	body[513] = 'o'
	body[514] = 'l'
	body[515] = 'y'
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLinkExternalFixedNow(t *testing.T) {
	withLinuxLinkPolicy(t)
	prev := linkNow
	linkNow = func() time.Time {
		return time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	}
	t.Cleanup(func() { linkNow = prev })

	images := t.TempDir()
	src := writeFakeSquashfs(t, t.TempDir(), []byte("x"))
	if err := LinkExternal(images, logsFor(images), "bob/tool", src, "dev"); err != nil {
		t.Fatal(err)
	}
	repo, err := OpenRepo(images, "bob/tool")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadVersionManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Images[0].CreatedAt.Equal(linkNow()) {
		t.Fatalf("created_at = %v", manifest.Images[0].CreatedAt)
	}
	if manifest.Images[0].Format.Compression != CompressionZstd {
		t.Fatalf("compression = %q", manifest.Images[0].Format.Compression)
	}
}
