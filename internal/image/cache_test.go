package image

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func digestHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeSource creates one source image file and returns its path.
func writeSource(t *testing.T, dir string, data []byte) string {
	t.Helper()
	src := filepath.Join(dir, "source.sqsh")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

// assertDirEmpty proves a failed copy left neither a temp file nor a
// half-finished cache entry behind.
func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("dir %s holds leftovers: %v", dir, names)
	}
}

func TestEnsureCachedMissCopiesAndVerifies(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	body := []byte("nas image bytes")
	src := writeSource(t, dir, body)
	want := digestHex(body)

	path, sum, err := EnsureCached(cache, src, want)
	if err != nil {
		t.Fatal(err)
	}
	if sum != want {
		t.Fatalf("sum = %s, want %s", sum, want)
	}
	if path != filepath.Join(cache, want+SquashExt) {
		t.Fatalf("path = %s", path)
	}
	assertBytes(t, path, body)
}

func TestEnsureCachedSelfKeyedFileComputesDigest(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	body := []byte("file-path image bytes")
	src := writeSource(t, dir, body)
	want := digestHex(body)

	path, sum, err := EnsureCached(cache, src, "")
	if err != nil {
		t.Fatal(err)
	}
	if sum != want || path != filepath.Join(cache, want+SquashExt) {
		t.Fatalf("got %s %s, want %s", path, sum, want)
	}
	assertBytes(t, path, body)
}

func TestEnsureCachedHitReusesWithoutCopy(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	body := []byte("original bytes")
	src := writeSource(t, dir, body)
	want := digestHex(body)
	path, _, err := EnsureCached(cache, src, want)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the source with garbage after the copy landed. A hit
	// must return the verified cached bytes without reading the
	// source again; a re-copy would fail the digest check instead.
	if err := os.WriteFile(src, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}

	gotPath, sum, err := EnsureCached(cache, src, want)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != path || sum != want {
		t.Fatalf("got %s %s, want %s %s", gotPath, sum, path, want)
	}
	assertBytes(t, path, body)
}

func TestEnsureCachedRepairsCorruptCopy(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	body := []byte("cacheable bytes")
	src := writeSource(t, dir, body)
	want := digestHex(body)
	path, _, err := EnsureCached(cache, src, want)
	if err != nil {
		t.Fatal(err)
	}
	// Flip one byte of the cached copy; the hit verification must
	// notice and replace the corrupted file from the source.
	corrupt := append([]byte{}, body...)
	corrupt[0] ^= 0xff
	if err := os.WriteFile(path, corrupt, 0o644); err != nil {
		t.Fatal(err)
	}

	gotPath, sum, err := EnsureCached(cache, src, want)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != path || sum != want {
		t.Fatalf("got %s %s, want %s %s", gotPath, sum, path, want)
	}
	assertBytes(t, path, body)
}

func TestEnsureCachedSourceDigestMismatch(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	src := writeSource(t, dir, []byte("honest bytes"))
	wrong := digestHex([]byte("different bytes"))

	_, _, err := EnsureCached(cache, src, wrong)
	if err == nil {
		t.Fatal("want mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "does not match sha256") {
		t.Fatalf("err = %v", err)
	}
	assertDirEmpty(t, cache)
}

func TestEnsureCachedMissingSourceLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	src := filepath.Join(dir, "absent.sqsh")

	_, _, err := EnsureCached(cache, src, "")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("self-keyed err = %v, want not-exist", err)
	}
	assertDirEmpty(t, cache)

	wrong := digestHex([]byte("recorded elsewhere"))
	_, _, err = EnsureCached(cache, src, wrong)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recorded-digest err = %v, want not-exist", err)
	}
	assertDirEmpty(t, cache)
}

func TestEnsureCachedRejectsMalformedDigest(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	src := writeSource(t, dir, []byte("bytes"))

	for _, want := range []string{"../evil", "ZZZ", strings.Repeat("a", 63)} {
		_, _, err := EnsureCached(cache, src, want)
		if err == nil || !strings.Contains(err.Error(), "invalid sha256") {
			t.Fatalf("want invalid sha256 for %q, got %v", want, err)
		}
	}
	assertDirEmpty(t, cache)
	if _, err := os.Stat(filepath.Join(dir, "evil")); !os.IsNotExist(err) {
		t.Fatalf("a cache entry escaped the cache directory: %v", err)
	}
}
