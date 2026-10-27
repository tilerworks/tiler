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
)

// assembleFixture lays out the block files of a fixture in a scratch
// directory and returns them as resolved BlockFile entries together
// with the concatenated whole-image bytes.
func assembleFixture(t *testing.T, fx splitFixture) ([]BlockFile, []byte) {
	t.Helper()
	dir := t.TempDir()
	writePayloadDir(t, dir, fx)
	var blocks []BlockFile
	var whole []byte
	for i, entry := range fx.root().Blocks {
		blocks = append(blocks, BlockFile{
			Path:      filepath.Join(dir, entry.Name),
			Name:      entry.Name,
			Size:      entry.Size,
			MtimeUnix: entry.MtimeUnix,
			SHA256:    entry.SHA256,
		})
		whole = append(whole, fx.bodies[i]...)
	}
	return blocks, whole
}

func wholeFacts(whole []byte) (string, int64) {
	sum := sha256.Sum256(whole)
	return hex.EncodeToString(sum[:]), int64(len(whole))
}

func TestAssembleBlocksStreamsVerifiedReassembly(t *testing.T) {
	fx := blocksFixture([][]byte{
		[]byte("chunk-one"), []byte("chunk-two"), []byte("tail"),
	})
	blocks, whole := assembleFixture(t, fx)
	wantSHA, wantSize := wholeFacts(whole)
	cacheDir := t.TempDir()
	path, err := AssembleBlocks(cacheDir, blocks, wantSHA, wantSize)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(cacheDir, wantSHA+SquashExt) {
		t.Fatalf("path = %s", path)
	}
	assertBytes(t, path, whole)
	assertNoTemps(t, cacheDir)
}

func TestAssembleBlocksNamesBadBlock(t *testing.T) {
	t.Run("size disagrees", func(t *testing.T) {
		fx := blocksFixture([][]byte{[]byte("chunk-one"), []byte("chunk-two")})
		blocks, whole := assembleFixture(t, fx)
		wantSHA, wantSize := wholeFacts(whole)
		if err := os.WriteFile(blocks[1].Path, []byte("torn"), 0o644); err != nil {
			t.Fatal(err)
		}
		cacheDir := t.TempDir()
		_, err := AssembleBlocks(cacheDir, blocks, wantSHA, wantSize)
		if err == nil || !strings.Contains(err.Error(), "blk.001") {
			t.Fatalf("err = %v", err)
		}
		assertGone(t, filepath.Join(cacheDir, wantSHA+SquashExt))
		assertNoTemps(t, cacheDir)
	})
	t.Run("digest disagrees", func(t *testing.T) {
		fx := blocksFixture([][]byte{[]byte("chunk-one"), []byte("chunk-two")})
		blocks, whole := assembleFixture(t, fx)
		wantSHA, wantSize := wholeFacts(whole)
		if err := os.WriteFile(blocks[0].Path, []byte("chunk-000"), 0o644); err != nil {
			t.Fatal(err)
		}
		cacheDir := t.TempDir()
		_, err := AssembleBlocks(cacheDir, blocks, wantSHA, wantSize)
		if err == nil || !strings.Contains(err.Error(), "blk.000") ||
			!strings.Contains(err.Error(), "sha256") {
			t.Fatalf("err = %v", err)
		}
		assertNoTemps(t, cacheDir)
	})
	t.Run("file missing", func(t *testing.T) {
		fx := blocksFixture([][]byte{[]byte("chunk-one"), []byte("chunk-two")})
		blocks, whole := assembleFixture(t, fx)
		wantSHA, wantSize := wholeFacts(whole)
		if err := os.Remove(blocks[0].Path); err != nil {
			t.Fatal(err)
		}
		_, err := AssembleBlocks(t.TempDir(), blocks, wantSHA, wantSize)
		if err == nil || !strings.Contains(err.Error(), "blk.000") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestAssembleBlocksRefusesWholeMismatch(t *testing.T) {
	fx := blocksFixture([][]byte{[]byte("chunk-one"), []byte("chunk-two")})
	blocks, whole := assembleFixture(t, fx)
	bogus := sha256.Sum256([]byte("another logical image"))
	cacheDir := t.TempDir()
	_, err := AssembleBlocks(cacheDir, blocks, hex.EncodeToString(bogus[:]), int64(len(whole)))
	if err == nil || !strings.Contains(err.Error(), "does not match sha256") {
		t.Fatalf("err = %v", err)
	}
	assertGone(t, filepath.Join(cacheDir, hex.EncodeToString(bogus[:])+SquashExt))
	assertNoTemps(t, cacheDir)
}

func TestAssembleBlocksRefusesSizeMismatch(t *testing.T) {
	fx := blocksFixture([][]byte{[]byte("chunk-one"), []byte("chunk-two")})
	blocks, whole := assembleFixture(t, fx)
	wantSHA, wantSize := wholeFacts(whole)
	cacheDir := t.TempDir()
	_, err := AssembleBlocks(cacheDir, blocks, wantSHA, wantSize+1)
	if err == nil || !strings.Contains(err.Error(), "does not match meta") {
		t.Fatalf("err = %v", err)
	}
	assertNoTemps(t, cacheDir)
}

func TestAssembleBlocksHitSkipsBlocks(t *testing.T) {
	fx := blocksFixture([][]byte{[]byte("chunk-one"), []byte("chunk-two")})
	blocks, whole := assembleFixture(t, fx)
	wantSHA, wantSize := wholeFacts(whole)
	cacheDir := t.TempDir()
	path := filepath.Join(cacheDir, wantSHA+SquashExt)
	if err := os.WriteFile(path, whole, 0o644); err != nil {
		t.Fatal(err)
	}
	// A hit must not read any block, so broken blocks cannot matter.
	for _, block := range blocks {
		if err := os.WriteFile(block.Path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := AssembleBlocks(cacheDir, blocks, wantSHA, wantSize)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("path = %s", got)
	}
	assertBytes(t, got, whole)
}

func TestAssembleBlocksRebuildsCorruptedCache(t *testing.T) {
	fx := blocksFixture([][]byte{[]byte("chunk-one"), []byte("chunk-two")})
	blocks, whole := assembleFixture(t, fx)
	wantSHA, wantSize := wholeFacts(whole)
	cacheDir := t.TempDir()
	path := filepath.Join(cacheDir, wantSHA+SquashExt)
	if err := os.WriteFile(path, []byte("corrupted cache entry"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := AssembleBlocks(cacheDir, blocks, wantSHA, wantSize)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("path = %s", got)
	}
	assertBytes(t, got, whole)
	assertNoTemps(t, cacheDir)
}

func TestAssembleBlocksRefusesInvalidRequests(t *testing.T) {
	fx := blocksFixture([][]byte{[]byte("chunk-one")})
	blocks, whole := assembleFixture(t, fx)
	wantSHA, wantSize := wholeFacts(whole)
	_, err := AssembleBlocks(t.TempDir(), blocks, "../escape", wantSize)
	if err == nil || !strings.Contains(err.Error(), "invalid sha256") {
		t.Fatalf("err = %v", err)
	}
	_, err = AssembleBlocks(t.TempDir(), nil, wantSHA, wantSize)
	if err == nil || !strings.Contains(err.Error(), "no blocks") {
		t.Fatalf("err = %v", err)
	}
}

func TestCacheSizeLimitGuidance(t *testing.T) {
	err := failCacheWrite("/cache/abc.sqsh", syscall.ENOSPC)
	if strings.Contains(err.Error(), "cache_dir") {
		t.Fatalf("ENOSPC gained cache guidance: %v", err)
	}
	if runtime.GOOS != "linux" {
		return
	}
	err = failCacheWrite("/cache/abc.sqsh", fmt.Errorf("write: %w", syscall.EFBIG))
	text := err.Error()
	for _, want := range []string{
		"cache_dir", "doctor", "/cache/abc.sqsh", "refused a file this large",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
	if !errors.Is(err, syscall.EFBIG) {
		t.Errorf("guidance dropped the cause: %v", err)
	}
}
