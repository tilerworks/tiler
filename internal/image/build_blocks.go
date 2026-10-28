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

// ensureBlocksSpace requires room for the staging file and for the
// block copy. An empty cache dir is left to makeStagingDir, which
// already reports that blocks builds need cache_dir.
func ensureBlocksSpace(cacheDir, repoDir, src string) error {
	if strings.TrimSpace(cacheDir) == "" {
		return nil
	}
	need, err := estimateTree(src)
	if err != nil {
		return err
	}
	return ensureSpace(
		spaceWrite{path: cacheDir, bytes: need},
		spaceWrite{path: repoDir, bytes: need},
	)
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
func packStagedImage(run ToolRunner, src, dst, typ, compression string) error {
	if err := packFullImage(run, src, dst, typ, compression); err != nil {
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

// blocksManifest assembles the single-node manifest of a blocks
// payload. The whole digest and size come from the streaming pass,
// and the logical image mtime is the staged pack time.
func blocksManifest(
	repo Repo,
	req FullRequest,
	staged string,
	split blockSplit,
	now func() time.Time,
) (Manifest, error) {
	info, err := os.Stat(staged)
	if err != nil {
		return Manifest{}, fmt.Errorf("build: %w", err)
	}
	node := ImageMeta{
		ID:        ID(split.whole),
		Kind:      KindFull,
		Size:      split.size,
		SHA256:    hexDigest(split.whole),
		MtimeUnix: info.ModTime().Unix(),
		CreatedAt: now().UTC(),
		Builder:   req.Builder,
		Blocks:    split.blocks,
	}
	manifest := fullManifest(
		repo.Name(),
		Format{Type: req.Format, Compression: req.Compression},
		node,
	)
	if err := manifest.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("build: %w", err)
	}
	return manifest, nil
}

// verifyPublishedBlocks re-hashes every published block against the
// manifest, extending the discipline of hashing the image again
// after the publish rename.
func verifyPublishedBlocks(repo Repo, node ImageMeta) error {
	dir, err := repo.BlocksDirPath(node.ID)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	ok, err := dirMatchesBlocks(dir, node.Blocks)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	if !ok {
		return fmt.Errorf("build: %s does not match the block manifest", dir)
	}
	return nil
}
