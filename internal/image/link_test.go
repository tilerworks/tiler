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
	if err := LinkExternal(images, "alice/app", src, "test-builder"); err != nil {
		t.Fatal(err)
	}
	repo, err := OpenRepo(images, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	payload := repo.ImagePath()
	target, err := os.Readlink(payload)
	if err != nil {
		t.Fatal(err)
	}
	if target != src {
		t.Fatalf("symlink target = %q, want %q", target, src)
	}
	meta, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(append([]byte("hsqs"), []byte("vendor-payload")...))
	want := hex.EncodeToString(sum[:])
	if meta.SHA256 != want || meta.ID != want {
		t.Fatalf("meta digest = %s id = %s", meta.SHA256, meta.ID)
	}
	if meta.Format.Type != FormatSquashfs || meta.Builder != "test-builder" {
		t.Fatalf("meta = %+v", meta)
	}
	if len(meta.Chain) != 0 {
		t.Fatalf("chain = %+v", meta.Chain)
	}
	rel, err := lookupSHA256(repo, meta.SHA256)
	if err != nil || rel != indexRelActive {
		t.Fatalf("index lookup = %q, %v", rel, err)
	}
}

func TestLinkExternalIdleRebuildsMissingIndexes(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	src := writeFakeSquashfs(t, nas, []byte("indexed-payload"))
	if err := LinkExternal(images, "alice/app", src, "test-builder"); err != nil {
		t.Fatal(err)
	}
	repo, err := OpenRepo(images, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{fileSHA256Index, fileContentSHA256Index} {
		if err := os.Remove(filepath.Join(repo.Dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := LinkExternal(images, "alice/app", src, "other-builder"); err != nil {
		t.Fatal(err)
	}
	if !indexesPresent(repo) {
		t.Fatal("idle link did not rebuild missing indexes")
	}
	meta, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	rel, err := lookupSHA256(repo, meta.SHA256)
	if err != nil || rel != indexRelActive {
		t.Fatalf("index lookup = %q, %v", rel, err)
	}
}

func TestLinkExternalIdempotent(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	src := writeFakeSquashfs(t, nas, []byte("same-bytes"))
	if err := LinkExternal(images, "alice/app", src, "test-builder"); err != nil {
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
	if err := LinkExternal(images, "alice/app", src, "other-builder"); err != nil {
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

func TestLinkExternalIdempotentWithChain(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	src := writeFakeSquashfs(t, nas, []byte("chained-bytes"))
	if err := LinkExternal(images, "alice/app", src, "test-builder"); err != nil {
		t.Fatal(err)
	}
	repo, err := OpenRepo(images, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	// Grow the active image with one incremental layer, the way a
	// build --incremental inside the linked view does.
	meta, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	layer := testID("abc1234567890def")
	meta.Chain = []ChainEntry{validEntry(meta.ID, layer)}
	if err := Store(repo.MetaPath(), meta); err != nil {
		t.Fatal(err)
	}
	incr, err := repo.IncrPath(layer)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(incr), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(incr, []byte("layer"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}

	// Re-registering the same external file is still a no-op: the
	// chain guard must not fire for a link that replaces nothing.
	if err := LinkExternal(images, "alice/app", src, "other-builder"); err != nil {
		t.Fatalf("re-link with chain = %v", err)
	}
	after, err := os.ReadFile(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("idempotent link rewrote meta")
	}
	if got, err := Load(repo.MetaPath()); err != nil || len(got.Chain) != 1 {
		t.Fatalf("chain after re-link = %+v (%v)", got.Chain, err)
	}
	// A different external file still hits the chain guard.
	other := writeFakeSquashfs(t, nas, []byte("other-bytes"))
	err = LinkExternal(images, "alice/app", other, "test")
	if err == nil || !strings.Contains(err.Error(), "incremental chain") {
		t.Fatalf("conflicting re-link = %v, want chain refusal", err)
	}
}

func TestLinkExternalConflictChain(t *testing.T) {
	withLinuxLinkPolicy(t)
	repo := testRepo(t)
	seedActive(t, repo, []byte("local-full"), true)
	nas := t.TempDir()
	src := writeFakeSquashfs(t, nas, []byte("external"))
	err := LinkExternal(linkImagesRoot(repo), repo.Name(), src, "test")
	if err == nil || !strings.Contains(err.Error(), "incremental chain") {
		t.Fatalf("error = %v", err)
	}
}

func TestLinkExternalConflictPayload(t *testing.T) {
	withLinuxLinkPolicy(t)
	repo := testRepo(t)
	seedActive(t, repo, []byte("local-full"), false)
	nas := t.TempDir()
	src := writeFakeSquashfs(t, nas, []byte("external"))
	err := LinkExternal(linkImagesRoot(repo), repo.Name(), src, "test")
	if err == nil || !strings.Contains(err.Error(), "external link") {
		t.Fatalf("error = %v", err)
	}
}

func TestLinkExternalConflictDifferentTarget(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	first := writeFakeSquashfs(t, nas, []byte("first"))
	second := writeFakeSquashfs(t, nas, []byte("second"))
	if err := LinkExternal(images, "alice/app", first, "test"); err != nil {
		t.Fatal(err)
	}
	err := LinkExternal(images, "alice/app", second, "test")
	if err == nil {
		t.Fatal("expected conflict")
	}
	if !strings.Contains(err.Error(), "does not match") &&
		!strings.Contains(err.Error(), "points elsewhere") {
		t.Fatalf("error = %v", err)
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
	err := LinkExternal(images, "alice/app", inside, "test")
	if err == nil || !errors.Is(err, config.ErrReservedPath) {
		t.Fatalf("error = %v", err)
	}
}

func TestLinkExternalRefusesForeignFormat(t *testing.T) {
	withLinuxLinkPolicy(t)
	nas := t.TempDir()
	src := writeFakeDMG(t, nas, []byte("disk-image"))
	err := LinkExternal(t.TempDir(), "alice/app", src, "test")
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
	err := LinkExternal(t.TempDir(), "alice/app", src, "test")
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
	if err := LinkExternal(images, "bob/tool", src, "dev"); err != nil {
		t.Fatal(err)
	}
	repo, err := OpenRepo(images, "bob/tool")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if !meta.CreatedAt.Equal(linkNow()) {
		t.Fatalf("created_at = %v", meta.CreatedAt)
	}
	if meta.Format.Compression != CompressionZstd {
		t.Fatalf("compression = %q", meta.Format.Compression)
	}
}
