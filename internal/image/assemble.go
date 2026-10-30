package image

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/durable"
)

// AssembleBlocks materializes the logical whole image of a blocks
// payload as <cacheDir>/<wantSHA>.sqsh, the content-addressed cache
// entry the default mount route consumes.
//
// A hit re-verifies the cached copy against the whole digest and
// returns it without reading any block; a corrupted copy is rebuilt.
// A miss streams the blocks in manifest order through a temp file
// inside the cache directory: every block is checked against its
// recorded size and sha256 and named in the error when it disagrees,
// the running whole-image digest and size are compared at the end,
// and only a complete verified file is renamed into place, so no
// partial cache entry survives a failure.
func AssembleBlocks(cacheDir string, blocks []BlockFile, wantSHA string, wantSize int64) (string, error) {
	if IsUnknownSHA256(wantSHA) {
		return "", fmt.Errorf("cache: unknown digest is not a content address")
	}
	if !isLowerHex(wantSHA, sumHexLen) {
		// The digest names the cache file, so refuse anything that
		// could escape the cache directory.
		return "", fmt.Errorf("cache: invalid sha256 %q", wantSHA)
	}
	if len(blocks) == 0 {
		return "", errors.New("cache: no blocks to assemble")
	}
	path := filepath.Join(cacheDir, wantSHA+SquashExt)
	if cachedOK(path, wantSHA) {
		return path, nil
	}
	if err := assembleVerified(cacheDir, blocks, wantSHA, wantSize, path); err != nil {
		return "", err
	}
	return path, nil
}

// assembleVerified streams the reassembly into a temp file inside
// cacheDir and renames it onto the final content-addressed path.
func assembleVerified(cacheDir string, blocks []BlockFile, wantSHA string, wantSize int64, path string) error {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	tmp, err := os.CreateTemp(cacheDir, ".tmp-"+wantSHA+"-*")
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	defer removeFile(tmp.Name())
	if err := streamBlocks(tmp, blocks, wantSHA, wantSize); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	return nil
}

// streamBlocks appends every block in manifest order while checking
// each one, then compares the reassembled whole against the recorded
// identity. The temp file is synced and closed here, so the rename
// only ever publishes complete verified bytes.
func streamBlocks(out *os.File, blocks []BlockFile, wantSHA string, wantSize int64) error {
	whole := sha256.New()
	var total int64
	for _, block := range blocks {
		n, err := appendBlock(out, block, whole)
		if err != nil {
			// The temp file is removed anyway; only release the
			// descriptor.
			_ = out.Close()
			return err
		}
		total += n
	}
	if err := joinWriteErr(durable.Sync(out), out.Close()); err != nil {
		return failCacheWrite(out.Name(), err)
	}
	if got := hex.EncodeToString(whole.Sum(nil)); got != wantSHA {
		return fmt.Errorf("cache: assembled image does not match sha256 %s", wantSHA)
	}
	if total != wantSize {
		return fmt.Errorf("cache: assembled size %d does not match meta %d", total, wantSize)
	}
	return nil
}

// appendBlock streams one block file into out while feeding the
// block digest and the shared whole-image digest. The recorded size
// and digest pin the block down; a bad block is named in the error.
func appendBlock(out *os.File, block BlockFile, whole io.Writer) (int64, error) {
	path, err := resolveBlockBytes(block)
	if err != nil {
		return 0, fmt.Errorf("cache: %w", err)
	}
	in, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("cache: block %s: %w", block.Name, err)
	}
	sum := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, sum, whole), in)
	closeErr := in.Close()
	if err := joinWriteErr(copyErr, closeErr); err != nil {
		return n, classifyBlockCopy(out.Name(), block.Name, err)
	}
	if n != block.Size {
		return n, fmt.Errorf("cache: block %s has size %d, meta records %d",
			block.Name, n, block.Size)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != block.SHA256 {
		return n, fmt.Errorf("cache: block %s does not match sha256", block.Name)
	}
	return n, nil
}

// classifyBlockCopy keeps the plain per-block meaning of a copy
// failure and only upgrades to the cache-directory guidance when the
// cache storage refused a large single file.
func classifyBlockCopy(dst, blockName string, err error) error {
	if isSizeLimitWrite(err) {
		return failCacheWrite(dst, err)
	}
	return fmt.Errorf("cache: block %s: %w", blockName, err)
}

// failCacheWrite names the failed cache write and guides to another
// cache directory when the storage refused a file this large.
func failCacheWrite(what string, err error) error {
	if isSizeLimitWrite(err) {
		return fmt.Errorf("cache: %s: %w: the cache storage refused a file this large; "+
			"point cache_dir at storage without a single-file size limit, "+
			"or check the doctor single-file size limit probe", what, err)
	}
	return fmt.Errorf("cache: %s: %w", what, err)
}
