package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/workspace"
)

// maxStackDepth is one full image plus at most seven increments.
const maxStackDepth = 8

// IncrRequest is one incremental layer build. The upper directory of
// a merged view is packed as-is and appended to the active chain.
type IncrRequest struct {
	// Config supplies the image library location and split policy.
	Config config.Config
	// Name is the resolved owner/repo image name.
	Name string
	// Upper is the writable upper directory to pack. It lives under
	// the runtime tree, so no reserved-tree check applies.
	Upper string
	// Subdir is an optional image-relative prefix. The upper is
	// moved under it before packing and moved back afterwards.
	Subdir string
	// Source is optional provenance recorded on the chain entry.
	Source Source
	// Refs is accepted for symmetry with full builds; appending a
	// layer retires no chain, so nothing consults it yet.
	Refs Refs
	// Now returns the creation time. Nil means time.Now.
	Now func() time.Time
	// Run executes external tools. Nil runs mksquashfs through exec.
	Run ToolRunner
	// MountSmoke is the quality gate run on the published layer.
	// Nil runs the default: a read-only mount spot check where
	// mounting works, and a distinguishable ErrSmokeSkipped where it
	// cannot, which still succeeds the build.
	MountSmoke func(image string) error
}

// IncrResult reports one successful incremental build.
type IncrResult struct {
	// ID is the first 16 hex digits of the layer sha256.
	ID string
	// Path is the published layer: incr/<id>.sqsh for a single-file
	// layer, or blocks/<id>/ when SplitSize is positive.
	Path string
	// SHA256 is the 64 hex digit layer digest.
	SHA256 string
}

// BuildIncr packs the upper directory of a merged view and appends
// it to the active chain.
//
// An active full image is required, and one full plus the current
// chain must stay below the stack depth limit. A subdir prefix moves
// the upper under the image layout with rename, never copy, so
// device nodes keep their inode type; the upper is moved back even
// when packing fails. mksquashfs keeps xattrs so whiteout and
// opaque markers survive. An unsplit layer is renamed into incr/
// first. A split layer is packed outside incr/ and published as
// blocks/<id>/, so the blocks layout never gains an incr/ directory.
// Metadata is replaced atomically afterwards; a failed metadata
// replace deletes the fresh layer and keeps the old metadata. A
// digest already in the chain is an idempotent success.
func BuildIncr(req IncrRequest) (IncrResult, error) {
	return buildIncr(req, hooks{})
}

func buildIncr(req IncrRequest, seam hooks) (res IncrResult, err error) {
	repo, active, err := validateIncr(req)
	if err != nil {
		return IncrResult{}, err
	}
	src, restore, err := stageUpper(req.Upper, req.Subdir)
	if err != nil {
		return IncrResult{}, err
	}
	defer func() {
		if restoreErr := restore(); restoreErr != nil && err == nil {
			err = fmt.Errorf("build: %w", restoreErr)
		}
	}()
	run := req.Run
	if run == nil {
		run = runTool
	}
	now := req.Now
	if now == nil {
		now = time.Now
	}
	var tmp string
	if req.Config.SplitSize > 0 {
		var stage string
		stage, tmp, err = packIncrStaged(req.Config.CacheDir, src, run)
		if err != nil {
			return IncrResult{}, err
		}
		defer func() { _ = os.RemoveAll(stage) }()
	} else {
		tmp, err = packIncrTemp(repo, src, run)
		if err != nil {
			return IncrResult{}, err
		}
	}
	return finishIncr(repo, req, active, tmp, now, seam)
}

func validateIncr(req IncrRequest) (Repo, Meta, error) {
	repo, err := OpenRepo(req.Config.ImagesDir, req.Name)
	if err != nil {
		return Repo{}, Meta{}, err
	}
	info, err := os.Stat(req.Upper)
	if err != nil {
		return Repo{}, Meta{}, fmt.Errorf("build: upper: %w", err)
	}
	if !info.IsDir() {
		return Repo{}, Meta{}, fmt.Errorf("build: upper %s is not a directory", req.Upper)
	}
	active, exists, err := loadActive(repo)
	if err != nil {
		return Repo{}, Meta{}, fmt.Errorf("build: %w", err)
	}
	if !exists {
		return Repo{}, Meta{}, errors.New("build: no active full image")
	}
	if 1+len(active.Chain) >= maxStackDepth {
		return Repo{}, Meta{}, fmt.Errorf(
			"build: chain already holds %d layers; build a new full image instead",
			len(active.Chain))
	}
	if err := checkIncrForm(active, req.Config.SplitSize); err != nil {
		return Repo{}, Meta{}, err
	}
	return repo, active, nil
}

// checkIncrForm keeps the active payload form aligned with the
// effective split size. Blocks layers only append onto blocks; a
// single-file active only accepts an unsplit build. A mismatch
// points at a full rebuild so the library stays consistent.
func checkIncrForm(active Meta, splitSize int64) error {
	kind := PayloadKind(active)
	switch {
	case splitSize > 0 && kind != PayloadBlocks:
		return fmt.Errorf(
			"build: active image is %s form but split size is set; build a new full image with --split first",
			kind)
	case splitSize == 0 && kind != PayloadSingle:
		return fmt.Errorf(
			"build: active image is %s form but split is off; build a new full image without --split first",
			kind)
	default:
		return nil
	}
}

// stageUpper returns the directory to pack and a restore function.
// A subdir prefix moves the upper under the same layout the image
// uses. The move is a rename, so character device whiteouts keep
// their inode type; copying is never allowed.
func stageUpper(upper, subdir string) (string, func() error, error) {
	upper = filepath.Clean(upper)
	if subdir == "" {
		return upper, func() error { return nil }, nil
	}
	if err := checkRel(subdir); err != nil {
		return "", nil, fmt.Errorf("build: subdir: %w", err)
	}
	name, err := tempName("subdir")
	if err != nil {
		return "", nil, fmt.Errorf("build: %w", err)
	}
	root := filepath.Join(filepath.Dir(upper), name)
	target := filepath.Join(root, subdir)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", nil, fmt.Errorf("build: %w", err)
	}
	if err := os.Rename(upper, target); err != nil {
		_ = os.RemoveAll(root)
		return "", nil, fmt.Errorf("build: cannot move upper under the subdir prefix: %w", err)
	}
	return target, func() error {
		if err := os.Rename(target, upper); err != nil {
			return fmt.Errorf("cannot move upper back: %w", err)
		}
		_ = os.RemoveAll(root)
		return nil
	}, nil
}

// packIncrTemp packs the staged upper into a synced temporary file
// inside the incr directory of the repository.
func packIncrTemp(repo Repo, src string, run ToolRunner) (string, error) {
	dir := filepath.Join(repo.Dir, dirIncr)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	name, err := tempName(dirIncr + squashExt)
	if err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	tmp := filepath.Join(dir, name)
	if err := packIncrImage(run, src, tmp); err != nil {
		removeFile(tmp)
		return "", err
	}
	if err := syncFile(tmp); err != nil {
		removeFile(tmp)
		return "", fmt.Errorf("build: %w", err)
	}
	if err := syncDir(dir, hooks{}); err != nil {
		removeFile(tmp)
		return "", fmt.Errorf("build: %w", err)
	}
	return tmp, nil
}

// packIncrStaged packs a blocks incr into a cache staging file. The
// packed bytes stay outside incr/, which is not part of a blocks
// layout. The caller removes stage after the split finishes.
func packIncrStaged(cacheDir, src string, run ToolRunner) (stage, tmp string, err error) {
	stage, err = makeStagingDir(cacheDir)
	if err != nil {
		return "", "", err
	}
	tmp = filepath.Join(stage, fileImage)
	if err = packIncrImage(run, src, tmp); err != nil {
		_ = os.RemoveAll(stage)
		return "", "", err
	}
	if err = syncFile(tmp); err != nil {
		_ = os.RemoveAll(stage)
		return "", "", fmt.Errorf("build: %w", err)
	}
	if err = syncDir(stage, hooks{}); err != nil {
		_ = os.RemoveAll(stage)
		return "", "", fmt.Errorf("build: %w", err)
	}
	return stage, tmp, nil
}

// packIncrImage is the mksquashfs argument list for one layer.
// Xattrs stay stored so whiteout markers in the user namespace
// survive; mksquashfs keeps device nodes as they are. The creation
// timestamp is pinned so the same upper packs to the same bytes.
func packIncrImage(run ToolRunner, src, dst string) error {
	return run("mksquashfs", sourceDateEnv(src), src, dst, "-comp", "zstd", "-noappend", "-xattrs")
}

// finishIncr hashes the packed layer, appends the chain entry, and
// publishes layer and metadata. A positive SplitSize splits the
// packed bytes into blocks/<id>/; otherwise the layer lands as
// incr/<id>.sqsh.
func finishIncr(
	repo Repo,
	req IncrRequest,
	active Meta,
	tmp string,
	now func() time.Time,
	seam hooks,
) (IncrResult, error) {
	sum, err := fileSHA256(tmp)
	if err != nil {
		removeFile(tmp)
		return IncrResult{}, fmt.Errorf("build: %w", err)
	}
	id := ID(sum)
	if chainHasID(active.Chain, id) {
		if req.Config.SplitSize > 0 {
			return reuseChainLayerBlocks(repo, active, id, sum, tmp, seam)
		}
		return reuseChainLayer(repo, id, sum, tmp, seam)
	}
	if req.Config.SplitSize > 0 {
		return finishIncrBlocks(repo, req, active, id, sum, tmp, now, seam)
	}
	return finishIncrSingle(repo, req, active, id, sum, tmp, now, seam)
}

// finishIncrSingle publishes one unsplit layer beside the active
// single-file full image.
func finishIncrSingle(
	repo Repo,
	req IncrRequest,
	active Meta,
	id string,
	sum [32]byte,
	tmp string,
	now func() time.Time,
	seam hooks,
) (IncrResult, error) {
	entry, err := chainEntry(req, active, id, sum, tmp, now, nil)
	if err != nil {
		removeFile(tmp)
		return IncrResult{}, err
	}
	next := active
	next.Chain = append(slices.Clone(active.Chain), entry)
	if err := next.Validate(); err != nil {
		removeFile(tmp)
		return IncrResult{}, fmt.Errorf("build: %w", err)
	}
	final, err := repo.IncrPath(id)
	if err != nil {
		removeFile(tmp)
		return IncrResult{}, err
	}
	if err := seam.do(tmp, final); err != nil {
		removeFile(tmp)
		return IncrResult{}, fmt.Errorf("build: %w", err)
	}
	if err := verifyFileSHA256(final, entry.SHA256); err != nil {
		removeFile(final)
		return IncrResult{}, fmt.Errorf("build: %w", err)
	}
	if err := replaceMeta(repo, active, next, seam); err != nil {
		removeFile(final)
		return IncrResult{}, err
	}
	if err := runIncrSmoke(req, final, entry); err != nil {
		return IncrResult{}, err
	}
	return IncrResult{ID: id, Path: final, SHA256: entry.SHA256}, nil
}

// finishIncrBlocks splits the packed layer into blocks/<id>/ and
// records the block manifest on the new chain entry. Digest and size
// stay those of the whole logical squashfs; no incr/<id>.sqsh is
// written.
func finishIncrBlocks(
	repo Repo,
	req IncrRequest,
	active Meta,
	id string,
	sum [32]byte,
	tmp string,
	now func() time.Time,
	seam hooks,
) (IncrResult, error) {
	info, err := os.Stat(tmp)
	if err != nil {
		removeFile(tmp)
		return IncrResult{}, fmt.Errorf("build: %w", err)
	}
	payloadTmp, err := makePayloadTmpDir(repo, tempBlocksDirName)
	if err != nil {
		removeFile(tmp)
		return IncrResult{}, err
	}
	defer func() { _ = os.RemoveAll(payloadTmp) }()
	split, err := splitStagedImage(tmp, payloadTmp, req.Config.SplitSize)
	removeFile(tmp)
	if err != nil {
		return IncrResult{}, err
	}
	if hexDigest(split.whole) != hexDigest(sum) {
		return IncrResult{}, errors.New("build: split digest does not match packed layer")
	}
	entry := ChainEntry{
		ID:        id,
		Parent:    lastChainID(active),
		Size:      split.size,
		SHA256:    hexDigest(split.whole),
		MtimeUnix: info.ModTime().Unix(),
		Source:    req.Source,
		CreatedAt: now().UTC(),
		Blocks:    split.blocks,
	}
	next := active
	next.Chain = append(slices.Clone(active.Chain), entry)
	if err := next.Validate(); err != nil {
		return IncrResult{}, fmt.Errorf("build: %w", err)
	}
	final, err := repo.BlocksDirPath(id)
	if err != nil {
		return IncrResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return IncrResult{}, fmt.Errorf("build: %w", err)
	}
	if err := seam.do(payloadTmp, final); err != nil {
		return IncrResult{}, fmt.Errorf("build: %w", err)
	}
	ok, err := dirMatchesBlocks(final, entry.Blocks)
	if err != nil || !ok {
		_ = os.RemoveAll(final)
		if err != nil {
			return IncrResult{}, fmt.Errorf("build: %w", err)
		}
		return IncrResult{}, fmt.Errorf("build: %s does not match the block manifest", final)
	}
	if err := replaceMeta(repo, active, next, seam); err != nil {
		_ = os.RemoveAll(final)
		return IncrResult{}, err
	}
	if err := runIncrSmoke(req, final, entry); err != nil {
		return IncrResult{}, err
	}
	return IncrResult{ID: id, Path: final, SHA256: entry.SHA256}, nil
}

// ErrSmokeSkipped marks a quality gate that could not run because
// mounting is unavailable here. The build treats it as success.
var ErrSmokeSkipped = errors.New("build: mount smoke gate skipped")

// defaultMountSmoke is the quality gate used when a request injects
// none: a read-only mount spot check of the fresh layer where
// mounting works, and a distinguishable skip where it cannot.
func defaultMountSmoke(path string) error {
	err := workspace.SmokeReadOnly(path)
	if errors.Is(err, workspace.ErrUnavailable) {
		return ErrSmokeSkipped
	}
	return err
}

// runIncrSmoke runs the quality gate on a single-file layer path, or
// on a reassembled blocks layer when the entry carries a Blocks
// manifest.
func runIncrSmoke(req IncrRequest, path string, entry ChainEntry) error {
	smoke := req.MountSmoke
	if smoke == nil {
		smoke = defaultMountSmoke
	}
	target := path
	if len(entry.Blocks) > 0 {
		assembled, err := assembleIncrBlocks(req.Config.CacheDir, path, entry)
		if err != nil {
			return fmt.Errorf("build: quality gate: %w", err)
		}
		target = assembled
	}
	if err := smoke(target); err != nil && !errors.Is(err, ErrSmokeSkipped) {
		return fmt.Errorf("build: quality gate: %w", err)
	}
	return nil
}

// assembleIncrBlocks materializes blocks/<id>/ into the content-
// addressed cache so the smoke gate can mount a real squashfs file.
func assembleIncrBlocks(cacheDir, blocksDir string, entry ChainEntry) (string, error) {
	files := make([]BlockFile, 0, len(entry.Blocks))
	for _, block := range entry.Blocks {
		files = append(files, BlockFile{
			Path:      filepath.Join(blocksDir, block.Name),
			Name:      block.Name,
			Size:      block.Size,
			MtimeUnix: block.MtimeUnix,
			SHA256:    block.SHA256,
		})
	}
	return AssembleBlocks(cacheDir, files, entry.SHA256, entry.Size)
}

// reuseChainLayer is the idempotent rebuild of a single-file layer
// the chain already lists. A missing file is put back from the
// fresh bytes.
func reuseChainLayer(repo Repo, id string, sum [32]byte, tmp string, seam hooks) (IncrResult, error) {
	path, err := repo.IncrPath(id)
	if err != nil {
		removeFile(tmp)
		return IncrResult{}, err
	}
	there, err := pathExists(path)
	if err != nil {
		removeFile(tmp)
		return IncrResult{}, err
	}
	if there {
		removeFile(tmp)
	} else if err := seam.do(tmp, path); err != nil {
		removeFile(tmp)
		return IncrResult{}, fmt.Errorf("build: %w", err)
	}
	if err := verifyFileSHA256(path, hexDigest(sum)); err != nil {
		return IncrResult{}, fmt.Errorf("build: %w", err)
	}
	return IncrResult{ID: id, Path: path, SHA256: hexDigest(sum)}, nil
}

// reuseChainLayerBlocks is the idempotent rebuild of a blocks layer
// the chain already lists. A missing directory is rebuilt from the
// fresh packed bytes; an existing directory is re-checked against
// the recorded manifest.
func reuseChainLayerBlocks(
	repo Repo,
	active Meta,
	id string,
	sum [32]byte,
	tmp string,
	seam hooks,
) (IncrResult, error) {
	entry, ok := chainEntryByID(active.Chain, id)
	if !ok {
		removeFile(tmp)
		return IncrResult{}, fmt.Errorf("build: chain entry %s is missing", id)
	}
	path, err := repo.BlocksDirPath(id)
	if err != nil {
		removeFile(tmp)
		return IncrResult{}, err
	}
	there, err := pathExists(path)
	if err != nil {
		removeFile(tmp)
		return IncrResult{}, err
	}
	if there {
		removeFile(tmp)
		match, err := dirMatchesBlocks(path, entry.Blocks)
		if err != nil {
			return IncrResult{}, fmt.Errorf("build: %w", err)
		}
		if !match {
			return IncrResult{}, fmt.Errorf("build: %s does not match the block manifest", path)
		}
		return IncrResult{ID: id, Path: path, SHA256: hexDigest(sum)}, nil
	}
	payloadTmp, err := makePayloadTmpDir(repo, tempBlocksDirName)
	if err != nil {
		removeFile(tmp)
		return IncrResult{}, err
	}
	defer func() { _ = os.RemoveAll(payloadTmp) }()
	split, err := splitStagedImage(tmp, payloadTmp, entryBlockSize(entry))
	removeFile(tmp)
	if err != nil {
		return IncrResult{}, err
	}
	if hexDigest(split.whole) != hexDigest(sum) {
		return IncrResult{}, errors.New("build: split digest does not match packed layer")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return IncrResult{}, fmt.Errorf("build: %w", err)
	}
	if err := seam.do(payloadTmp, path); err != nil {
		return IncrResult{}, fmt.Errorf("build: %w", err)
	}
	match, err := dirMatchesBlocks(path, entry.Blocks)
	if err != nil || !match {
		_ = os.RemoveAll(path)
		if err != nil {
			return IncrResult{}, fmt.Errorf("build: %w", err)
		}
		return IncrResult{}, fmt.Errorf("build: %s does not match the block manifest", path)
	}
	return IncrResult{ID: id, Path: path, SHA256: hexDigest(sum)}, nil
}

// entryBlockSize recovers the block size used when the layer was
// first published: every full block shares that size, and a single
// short block uses its recorded size.
func entryBlockSize(entry ChainEntry) int64 {
	if len(entry.Blocks) == 0 {
		return entry.Size
	}
	return entry.Blocks[0].Size
}

// chainEntry assembles the new chain member. Its parent is the last
// chain member, or the full image id when the chain is empty.
func chainEntry(
	req IncrRequest,
	active Meta,
	id string,
	sum [32]byte,
	tmp string,
	now func() time.Time,
	blocks []BlockEntry,
) (ChainEntry, error) {
	info, err := os.Stat(tmp)
	if err != nil {
		return ChainEntry{}, fmt.Errorf("build: %w", err)
	}
	return ChainEntry{
		ID:        id,
		Parent:    lastChainID(active),
		Size:      info.Size(),
		SHA256:    hexDigest(sum),
		MtimeUnix: info.ModTime().Unix(),
		Source:    req.Source,
		CreatedAt: now().UTC(),
		Blocks:    blocks,
	}, nil
}

// replaceMeta atomically swaps meta.yaml. When the swap fails after
// the rename landed, the previous metadata is written back so the
// chain and the incr files stay consistent.
func replaceMeta(repo Repo, old, next Meta, seam hooks) error {
	body, err := Encode(next)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	metaTmp, err := writeTemp(repo.Dir, fileMeta, body, hooks{})
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	if err := seam.do(metaTmp, repo.MetaPath()); err != nil {
		undoLanded(metaTmp, repo.MetaPath())
		removeFile(metaTmp)
		if _, statErr := os.Stat(repo.MetaPath()); errors.Is(statErr, os.ErrNotExist) {
			_ = Store(repo.MetaPath(), old)
		}
		return fmt.Errorf("build: %w", err)
	}
	return nil
}

func lastChainID(active Meta) string {
	if len(active.Chain) == 0 {
		return active.ID
	}
	return active.Chain[len(active.Chain)-1].ID
}

func chainHasID(chain []ChainEntry, id string) bool {
	return slices.ContainsFunc(chain, func(entry ChainEntry) bool {
		return entry.ID == id
	})
}

func chainEntryByID(chain []ChainEntry, id string) (ChainEntry, bool) {
	for _, entry := range chain {
		if entry.ID == id {
			return entry, true
		}
	}
	return ChainEntry{}, false
}
