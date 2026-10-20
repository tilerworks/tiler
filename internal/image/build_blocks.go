package image

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// buildBlocks is the byte-block full build pipeline: pack the whole
// image into a local staging directory, stream it into blocks inside
// the repository, and publish the directory payload atomically. The
// staging directory and the scratch payload directory never survive
// the call. Callers must set a positive BlockSize (BuildFull copies
// SplitSize when needed).
func buildBlocks(req FullRequest) (FullResult, error) {
	repo, err := validateFull(req)
	if err != nil {
		return FullResult{}, err
	}
	if err := prepareScene(req); err != nil {
		return FullResult{}, err
	}
	blockSize := req.BlockSize
	if blockSize <= 0 {
		return FullResult{}, errors.New("build: positive block size is required")
	}
	stageDir, err := makeStagingDir(req.Config.CacheDir)
	if err != nil {
		return FullResult{}, err
	}
	defer func() { _ = os.RemoveAll(stageDir) }()
	staged := filepath.Join(stageDir, fileImage)
	if err := packStagedImage(toolRunner(req), req.SrcDir, staged); err != nil {
		return FullResult{}, err
	}
	payloadTmp, err := makePayloadTmpDir(repo, tempBlocksDirName)
	if err != nil {
		return FullResult{}, err
	}
	defer func() { _ = os.RemoveAll(payloadTmp) }()
	split, err := splitStagedImage(staged, payloadTmp, blockSize)
	if err != nil {
		return FullResult{}, err
	}
	return publishBlocks(repo, req, staged, payloadTmp, split)
}

// publishBlocks assembles the metadata, publishes the staged payload
// directory, and re-hashes every published block against the
// manifest.
func publishBlocks(
	repo Repo,
	req FullRequest,
	staged, payloadTmp string,
	split blockSplit,
) (FullResult, error) {
	meta, err := blocksMeta(repo, req, staged, split, buildNow(req))
	if err != nil {
		return FullResult{}, err
	}
	body, err := Encode(meta)
	if err != nil {
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	// Resolve the final payload place before the meta temp exists, so
	// no early return can leave a staged temp behind.
	finalDir, err := repo.BlocksDirPath(meta.ID)
	if err != nil {
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	metaTmp, err := writeTemp(repo.Dir, fileMeta, body, hooks{})
	if err != nil {
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	if err := publishStagedPayload(repo, meta, req.Refs,
		payloadTmp, finalDir, metaTmp, hooks{}); err != nil {
		return FullResult{}, err
	}
	if err := verifyPublishedBlocks(repo, meta); err != nil {
		return FullResult{}, err
	}
	return FullResult{ID: meta.ID, Path: finalDir, SHA256: meta.SHA256}, nil
}

// makeStagingDir creates the scratch directory under cacheDir that
// holds the whole packed image before it is split. The packer needs
// a writable full-size output file, so staging rests on local cache
// storage even when the repository caps single-file sizes.
func makeStagingDir(cacheDir string) (string, error) {
	if strings.TrimSpace(cacheDir) == "" {
		return "", errors.New("build: cache_dir is required for blocks builds")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	dir := filepath.Join(cacheDir,
		fmt.Sprintf("build-%d-%s", os.Getpid(), hex.EncodeToString(buf[:])))
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	return dir, nil
}

// packStagedImage runs the packer into the staging file and syncs
// it, so the streaming split reads bytes that survived a crash. The
// failures route through the single-file limit classifier.
func packStagedImage(run ToolRunner, src, dst string) error {
	if err := packFullImage(run, src, dst); err != nil {
		return failTool(dst, err)
	}
	if err := syncFile(dst); err != nil {
		return failWrite(dst, err)
	}
	return nil
}

// makePayloadTmpDir creates the scratch payload directory inside the
// repository directory, so the publish rename stays on the target
// filesystem. Both split forms stage their payload here.
func makePayloadTmpDir(repo Repo, tempName func() (string, error)) (string, error) {
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	name, err := tempName()
	if err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	dir := filepath.Join(repo.Dir, name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	return dir, nil
}

// blockSplit is the outcome of one streaming pass over the staged
// image: the manifest in write order, the whole-image digest, and
// the whole size.
type blockSplit struct {
	blocks []BlockEntry
	whole  [32]byte
	size   int64
}

// splitStagedImage streams the staged image through io.CopyN blocks
// in one pass. Every block file feeds its own sha256 and the shared
// whole-image sha256, so the manifest and the logical digest come
// from the same read. An image that ends on a block boundary leaves
// no trailing empty block, and outgrowing the manifest capacity is
// refused.
func splitStagedImage(staged, tmpDir string, blockSize int64) (blockSplit, error) {
	src, err := os.Open(staged)
	if err != nil {
		return blockSplit{}, fmt.Errorf("build: %w", err)
	}
	defer func() { _ = src.Close() }()
	whole := sha256.New()
	var split blockSplit
	var names []string
	for i := 0; ; i++ {
		name := fmt.Sprintf(blockNameFmt, i)
		entry, n, err := writeBlock(src, tmpDir, name, blockSize, whole)
		if err != nil {
			return blockSplit{}, err
		}
		if n == 0 {
			// The image ended on a block boundary; no trailing
			// empty block is kept.
			removeFile(filepath.Join(tmpDir, name))
			break
		}
		if i >= maxBlocks {
			return blockSplit{}, fmt.Errorf(
				"build: image exceeds the %d-block capacity; raise the block size",
				maxBlocks)
		}
		split.blocks = append(split.blocks, entry)
		split.size += n
		names = append(names, name)
		if n < blockSize {
			break
		}
	}
	if len(split.blocks) == 0 {
		return blockSplit{}, errors.New("build: packed image is empty")
	}
	copy(split.whole[:], whole.Sum(nil))
	if err := writeConcatView(tmpDir, names); err != nil {
		return blockSplit{}, err
	}
	if err := syncDir(tmpDir, hooks{}); err != nil {
		return blockSplit{}, fmt.Errorf("build: %w", err)
	}
	return split, nil
}

// writeBlock copies at most blockSize bytes into tmpDir/name while
// feeding the block digest and the shared whole digest. It returns
// the manifest entry and the copied byte count; a count of zero
// means the source ended before this block.
func writeBlock(
	src io.Reader,
	tmpDir, name string,
	blockSize int64,
	whole io.Writer,
) (BlockEntry, int64, error) {
	path := filepath.Join(tmpDir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return BlockEntry{}, 0, failWrite(path, err)
	}
	block := sha256.New()
	n, copyErr := io.CopyN(io.MultiWriter(file, block, whole), src, blockSize)
	serr := file.Sync()
	cerr := file.Close()
	if copyErr != nil && !errors.Is(copyErr, io.EOF) {
		return BlockEntry{}, n, failWrite(path, copyErr)
	}
	if err := joinWriteErr(serr, cerr); err != nil {
		return BlockEntry{}, n, failWrite(path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return BlockEntry{}, n, fmt.Errorf("build: %w", err)
	}
	var sum [32]byte
	copy(sum[:], block.Sum(nil))
	return BlockEntry{
		Name:      name,
		Size:      n,
		SHA256:    hexDigest(sum),
		MtimeUnix: info.ModTime().Unix(),
	}, n, nil
}

// writeConcatView lays the concat description file beside the
// blocks. It lists the block names in manifest order, one per line;
// the optional concat route mounts through it without reassembly.
func writeConcatView(tmpDir string, names []string) error {
	path := filepath.Join(tmpDir, fileConcatView)
	body := []byte(strings.Join(names, "\n") + "\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return failWrite(path, err)
	}
	if err := syncFile(path); err != nil {
		return failWrite(path, err)
	}
	return nil
}

// blocksMeta assembles the full metadata of a blocks payload. The
// whole digest and size come from the streaming pass, and the
// logical image mtime is the staged pack time.
func blocksMeta(
	repo Repo,
	req FullRequest,
	staged string,
	split blockSplit,
	now func() time.Time,
) (Meta, error) {
	info, err := os.Stat(staged)
	if err != nil {
		return Meta{}, fmt.Errorf("build: %w", err)
	}
	meta := newFullMeta(repo, req, now)
	meta.ID = ID(split.whole)
	meta.Size = split.size
	meta.SHA256 = hexDigest(split.whole)
	meta.MtimeUnix = info.ModTime().Unix()
	meta.Blocks = split.blocks
	if err := meta.Validate(); err != nil {
		return Meta{}, fmt.Errorf("build: %w", err)
	}
	return meta, nil
}

// verifyPublishedBlocks re-hashes every published block against the
// manifest, extending the discipline of hashing the image again
// after the publish rename.
func verifyPublishedBlocks(repo Repo, meta Meta) error {
	dir, err := repo.BlocksDirPath(meta.ID)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	ok, err := dirMatchesBlocks(dir, meta.Blocks)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	if !ok {
		return fmt.Errorf("build: %s does not match the block manifest", dir)
	}
	return nil
}
