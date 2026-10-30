package image

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePayloadBytesPathSymlink(t *testing.T) {
	dir := t.TempDir()
	src := writeFakeSquashfs(t, dir, []byte("via-symlink"))
	leaf := filepath.Join(dir, "image.sqsh")
	if err := os.Symlink(src, leaf); err != nil {
		t.Fatal(err)
	}
	got, err := ResolvePayloadBytesPath(leaf)
	if err != nil {
		t.Fatal(err)
	}
	if got != src {
		t.Fatalf("bytes path = %q, want %q", got, src)
	}
}

func TestResolvePayloadBytesPathPathRef(t *testing.T) {
	dir := t.TempDir()
	src := writeFakeSquashfs(t, dir, []byte("via-pathref"))
	leaf := filepath.Join(dir, "image.sqsh")
	if err := writePathRef(src, leaf); err != nil {
		t.Fatal(err)
	}
	got, err := ResolvePayloadBytesPath(leaf)
	if err != nil {
		t.Fatal(err)
	}
	if got != src {
		t.Fatalf("bytes path = %q, want %q", got, src)
	}
}

func TestResolvePayloadBytesPathLocalPayload(t *testing.T) {
	dir := t.TempDir()
	leaf := writeFakeSquashfs(t, dir, []byte("local-bytes"))
	got, err := ResolvePayloadBytesPath(leaf)
	if err != nil {
		t.Fatal(err)
	}
	if got != leaf {
		t.Fatalf("bytes path = %q, want leaf %q", got, leaf)
	}
}

func TestResolvePayloadBytesPathRefQuickCheck(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	src := writeFakeSquashfs(t, nas, []byte("check-me"))
	prev := linkSymlink
	linkSymlink = func(string, string) error {
		return os.ErrPermission
	}
	t.Cleanup(func() { linkSymlink = prev })
	if err := LinkExternal(images, logsFor(images), "alice/app", src, "test"); err != nil {
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
	payload, err := repo.ObjectPayloadPath(manifest.Images[0])
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(payload)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("expected path-ref regular file")
	}
	if err := QuickCheck(manifest.Images[0], payload); err != nil {
		t.Fatal(err)
	}
	if err := VerifySHA256(manifest.Images[0], payload); err != nil {
		t.Fatal(err)
	}
}

func TestLinkExternalPathRefFallback(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	src := writeFakeSquashfs(t, nas, []byte("fallback"))
	prev := linkSymlink
	linkSymlink = func(string, string) error {
		return os.ErrPermission
	}
	t.Cleanup(func() { linkSymlink = prev })
	if err := LinkExternal(images, logsFor(images), "alice/app", src, "test"); err != nil {
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
	payload, err := repo.ObjectPayloadPath(manifest.Images[0])
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(payload)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != src+"\n" {
		t.Fatalf("path-ref body = %q", body)
	}
	if err := LinkExternal(images, logsFor(images), "alice/app", src, "other"); err != nil {
		t.Fatal(err)
	}
}

func TestLinkExternalIdlePathRef(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	src := writeFakeSquashfs(t, nas, []byte("idle-pathref"))
	prev := linkSymlink
	linkSymlink = func(string, string) error {
		return os.ErrPermission
	}
	t.Cleanup(func() { linkSymlink = prev })
	if err := LinkExternal(images, logsFor(images), "alice/app", src, "test"); err != nil {
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
	if err := LinkExternal(images, logsFor(images), "alice/app", src, "again"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("idle path-ref rewrote meta")
	}
}

func TestEnsureCachedPathRef(t *testing.T) {
	dir := t.TempDir()
	src := writeFakeSquashfs(t, dir, []byte("cached-pathref"))
	leaf := filepath.Join(dir, "image.sqsh")
	if err := writePathRef(src, leaf); err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA256(src)
	if err != nil {
		t.Fatal(err)
	}
	want := hexDigest(sum)
	cache := t.TempDir()
	path, got, err := EnsureCached(cache, leaf, want)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("digest = %s", got)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	srcBody, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(srcBody) {
		t.Fatal("cache copy mismatched source bytes")
	}
}
