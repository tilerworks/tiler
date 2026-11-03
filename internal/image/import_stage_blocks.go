package image

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// StagedBlocks is one unpublished split payload inside the target
// library. Blocks keep manifest names, sizes, and source payload
// mtimes. SHA256 is the logical image digest, updated in the same
// pass as each block. Dir also holds a concat view rebuilt from that
// order; the source concat file is not copied. Remove drops the
// unpublished directory.
type StagedBlocks struct {
	Dir    string
	Blocks []BlockEntry
	Size   int64
	SHA256 string
	Format Format
	root   string
}

// StageImportBlocks copies one split node from a directory source.
// Blocks are read in manifest order. Each read updates that block's
// digest and the logical image digest. A missing, reordered, or
// corrupt block, a logical digest mismatch, a changed source, or
// cancellation removes the staging directory. Nothing is added to
// the committed manifest.
func StageImportBlocks(ctx context.Context, req ImportRequest, id string) (StagedBlocks, error) {
	return stageImportBlocks(ctx, req, id, stageCopyHooks{})
}

func stageImportBlocks(ctx context.Context, req ImportRequest, id string, seam stageCopyHooks) (StagedBlocks, error) {
	if req.Mode != ImportCopy {
		return StagedBlocks{}, importFail(req, errors.New("copy staging requires copy mode"))
	}
	if err := rejectNonDirectorySplit(req); err != nil {
		return StagedBlocks{}, err
	}
	read, err := readImportSource(req)
	if err != nil {
		return StagedBlocks{}, err
	}
	if read.plan.Kind != SourceDir {
		return StagedBlocks{}, importFail(req, errors.New("split copy requires an image directory"))
	}
	node, err := findSplitNode(read.manifest, id)
	if err != nil {
		return StagedBlocks{}, importFail(req, err)
	}
	staged, err := copyImportBlocks(bindStageCtx(ctx), req, read, node, seam)
	if err != nil {
		return StagedBlocks{}, importFail(req, err)
	}
	return staged, nil
}

// rejectNonDirectorySplit fails a regular file before format policy.
// Split copy never accepts a file. On macOS the squashfs refusal
// would otherwise hide that requirement. A missing path stays with
// readImportSource so the stat error is unchanged.
func rejectNonDirectorySplit(req ImportRequest) error {
	source := strings.TrimSpace(req.Source)
	if source == "" {
		return nil
	}
	given, err := filepath.Abs(source)
	if err != nil {
		return nil
	}
	info, err := os.Stat(given)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		return nil
	}
	return importFail(req, errors.New("split copy requires an image directory"))
}

func bindStageCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func findSplitNode(manifest VersionManifest, id string) (ImageMeta, error) {
	if err := checkID(id); err != nil {
		return ImageMeta{}, err
	}
	for _, node := range manifest.Images {
		if node.ID != id {
			continue
		}
		if err := orderedBlocks(node); err != nil {
			return ImageMeta{}, err
		}
		return node, nil
	}
	return ImageMeta{}, fmt.Errorf("version %s not in source", id)
}

func orderedBlocks(node ImageMeta) error {
	if len(node.Blocks) == 0 {
		return errors.New("split copy requires blocks")
	}
	if IsUnknownSHA256(node.SHA256) {
		return fmt.Errorf("unknown digest is not valid for %s", node.ID)
	}
	var size int64
	for i, block := range node.Blocks {
		if block.Name != fmt.Sprintf(blockNameFmt, i) {
			return fmt.Errorf("block %s is out of order", block.Name)
		}
		if IsUnknownSHA256(block.SHA256) {
			return fmt.Errorf("unknown digest is not valid for block %s", block.Name)
		}
		size += block.Size
	}
	if size != node.Size {
		return fmt.Errorf("logical image %s size mismatch: metadata declares %d bytes, blocks total %d bytes", node.ID, node.Size, size)
	}
	return nil
}

func copyImportBlocks(ctx context.Context, req ImportRequest, read importRead, node ImageMeta, seam stageCopyHooks) (StagedBlocks, error) {
	dir, err := prepareStageDir(read.plan.Repo)
	if err != nil {
		return failBlocks(dir, req.ImagesDir, err)
	}
	src := Repo{Dir: read.plan.Location}
	whole := sha256.New()
	blocks := make([]BlockEntry, 0, len(node.Blocks))
	var size int64
	for _, want := range node.Blocks {
		entry, copyErr := copyOneBlock(ctx, src, dir, node.ID, want, whole, seam)
		if copyErr != nil {
			return failBlocks(dir, req.ImagesDir, copyErr)
		}
		blocks = append(blocks, entry)
		size += entry.Size
	}
	staged, err := finishStagedBlocks(dir, node, blocks, size, whole.Sum(nil), req.ImagesDir)
	if err != nil {
		return failBlocks(dir, req.ImagesDir, err)
	}
	if err = metaUnchanged(read); err != nil {
		return failBlocks(dir, req.ImagesDir, err)
	}
	return staged, nil
}

func copyOneBlock(ctx context.Context, src Repo, dir, id string, want BlockEntry, whole io.Writer, seam stageCopyHooks) (BlockEntry, error) {
	if err := ctx.Err(); err != nil {
		return BlockEntry{}, err
	}
	payload, err := blockPayload(src, id, want.Name)
	if err != nil {
		return BlockEntry{}, err
	}
	before, err := os.Stat(payload)
	if err != nil {
		return BlockEntry{}, fmt.Errorf("block %s: %w", want.Name, err)
	}
	file, err := importOpen(payload, seam)
	if err != nil {
		return BlockEntry{}, fmt.Errorf("block %s: %w", want.Name, err)
	}
	entry, copyErr := writeStagedBlock(ctx, dir, want, before, file, whole, seam)
	err = joinWriteErr(copyErr, file.Close())
	if copyErr != nil {
		return BlockEntry{}, copyErr
	}
	if err != nil {
		return BlockEntry{}, fmt.Errorf("block %s: %w", want.Name, err)
	}
	return stagedBlockStable(payload, before, entry)
}

func stagedBlockStable(payload string, before os.FileInfo, entry BlockEntry) (BlockEntry, error) {
	after, err := os.Stat(payload)
	if err != nil {
		return BlockEntry{}, fmt.Errorf("block %s: %w", entry.Name, err)
	}
	if err = checkSourceStable(before, after, entry.Size); err != nil {
		return BlockEntry{}, fmt.Errorf("block %s: %w", entry.Name, err)
	}
	return entry, nil
}

func writeStagedBlock(ctx context.Context, dir string, want BlockEntry, before os.FileInfo, src io.Reader, whole io.Writer, seam stageCopyHooks) (BlockEntry, error) {
	path := filepath.Join(dir, want.Name)
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return BlockEntry{}, err
	}
	n, sum, err := streamHashedCopy(dst, cancelReader{ctx, src}, whole, seam)
	if err != nil {
		return BlockEntry{}, fmt.Errorf("block %s: %w", want.Name, err)
	}
	got := hex.EncodeToString(sum)
	if n != want.Size {
		return BlockEntry{}, fmt.Errorf("block %q size mismatch: expected %d bytes, got %d", path, want.Size, n)
	}
	if got != want.SHA256 {
		return BlockEntry{}, fmt.Errorf("block %q digest mismatch: expected sha256 %s, got %s", path, want.SHA256, got)
	}
	if err = acceptCopyDigest(got); err != nil {
		return BlockEntry{}, fmt.Errorf("block %s: %w", want.Name, err)
	}
	if err = sealStagedCopy(path, before); err != nil {
		return BlockEntry{}, err
	}
	return BlockEntry{Name: want.Name, Size: n, SHA256: got, MtimeUnix: before.ModTime().Unix()}, nil
}

func finishStagedBlocks(dir string, node ImageMeta, blocks []BlockEntry, size int64, whole []byte, root string) (StagedBlocks, error) {
	sum := hex.EncodeToString(whole)
	if sum != node.SHA256 {
		return StagedBlocks{}, fmt.Errorf("logical image digest mismatch at %q: expected sha256 %s, got %s", dir, node.SHA256, sum)
	}
	if err := acceptCopyDigest(sum); err != nil {
		return StagedBlocks{}, err
	}
	if size != node.Size {
		return StagedBlocks{}, fmt.Errorf("logical image size mismatch at %q: expected %d bytes, got %d", dir, node.Size, size)
	}
	if err := writeConcatView(dir, blockNames(blocks)); err != nil {
		return StagedBlocks{}, err
	}
	if err := syncDir(dir, hooks{}); err != nil {
		return StagedBlocks{}, err
	}
	return StagedBlocks{
		Dir: dir, Blocks: blocks, Size: size, SHA256: sum,
		Format: node.Format, root: root,
	}, nil
}

func blockNames(blocks []BlockEntry) []string {
	names := make([]string, len(blocks))
	for i, block := range blocks {
		names[i] = block.Name
	}
	return names
}

func blockPayload(src Repo, id, name string) (string, error) {
	leaf, err := src.ObjectBlockPath(id, name)
	if err != nil {
		return "", err
	}
	return resolveBlockBytes(BlockFile{Path: leaf, Name: name})
}

func prepareStageDir(repo Repo) (string, error) {
	parent := importStagePath(repo)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	name, err := tempName("blocks")
	if err != nil {
		return "", err
	}
	dir := filepath.Join(parent, name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func metaUnchanged(read importRead) error {
	meta := filepath.Join(read.plan.Location, fileMeta)
	info, err := os.Stat(meta)
	if err != nil {
		return fmt.Errorf("metadata: %w", err)
	}
	body, err := os.ReadFile(meta)
	if err != nil {
		return fmt.Errorf("metadata: %w", err)
	}
	sameTime := info.ModTime().Unix() == read.plan.MtimeUnix
	if info.Size() != read.plan.Size || !sameTime || !bytes.Equal(body, read.body) {
		return errors.New("metadata changed while reading; retry")
	}
	return nil
}

func failBlocks(dir, stop string, err error) (StagedBlocks, error) {
	discardStage(dir, stop)
	return StagedBlocks{}, err
}

// Remove deletes this unpublished split copy and empty staging directories.
func (s StagedBlocks) Remove() {
	discardStage(s.Dir, s.root)
}

type cancelReader struct {
	ctx context.Context
	r   io.Reader
}

func (c cancelReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
