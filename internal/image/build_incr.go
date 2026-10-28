package image

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/container"
	"github.com/jiangxin/tiler/internal/workspace"
)

// maxStackDepth is one full image plus at most seven increments.
const maxStackDepth = 8

// IncrRequest is one incremental layer build. The upper directory of
// a merged view is packed as-is and appended to the active chain.
type IncrRequest struct {
	// Config supplies the image library location and split policy.
	Config config.Config
	// Name is the resolved 1-to-5 segment image name.
	Name string
	// Format, when set, must match the active image container type.
	// Empty inherits that type. A mismatch is ErrFormatConflict and
	// packs nothing.
	Format string
	// Compression, when set, must match the active image compression.
	// Empty inherits it. A mismatch is ErrFormatConflict.
	Compression string
	// Upper is the writable upper directory to pack. It lives under
	// the runtime tree, so no reserved-tree check applies.
	Upper string
	// Subdir is an optional image-relative prefix. The upper is
	// moved under it before packing and moved back afterwards.
	Subdir string
	// BaseTip names the manifest graph node the new layer is built
	// on: the served tip of the view whose upper is packed. Empty
	// builds on the active head, preserving the historical append
	// behavior. A base tip that trails the head forms a branch, and
	// a successful build still moves the head to the new layer. An
	// unknown base tip is refused before packing.
	BaseTip string
	// Refs is accepted for symmetry with full builds; appending a
	// layer retires no chain, so nothing consults it yet.
	Refs Refs
	// Now returns the creation time. Nil means time.Now.
	Now func() time.Time
	// Run executes external tools. Nil runs the container packer
	// through exec. The packer is the active image format.
	Run ToolRunner
	// MountSmoke is the quality gate run on the published layer.
	// Nil mounts that layer read-only through the active image
	// format. Missing format tools or a missing /dev/fuse become
	// ErrSmokeSkipped, which still succeeds the build. A mount
	// that fails is a real error.
	MountSmoke func(image string) error
}

// IncrResult reports one successful incremental build. When the
// packed digest was already in the graph, the result reports that
// existing node: it may be parented on another tip than the request's
// BaseTip, and the head does not move. Callers must not infer the
// node's parent or a head advance from the result alone; read the
// manifest when those facts matter.
type IncrResult struct {
	// ID is the lowercase hex sha256 of the layer payload.
	ID string
	// Path is the published layer: _incr/<id>.sqsh for a single-file
	// layer, or _blocks/<id>/ when SplitSize is positive.
	Path string
	// SHA256 is the 64 hex digit layer digest.
	SHA256 string
}

// BuildIncr packs the upper directory of a merged view and publishes
// it as a new layer parented on the request's base tip in the active
// manifest graph.
//
// An active full image is required, and the root-to-base-tip path
// plus the new layer must stay within the stack depth limit. A
// subdir prefix moves the upper under the image layout with rename,
// never copy, so device nodes keep their inode type; the upper is
// moved back even when packing fails. The container packer keeps
// whiteout and opaque markers. An unsplit layer is renamed into
// _incr/ first. A split layer is packed outside _incr/ and published
// as _blocks/<id>/, so the blocks layout never gains an _incr/
// directory. On success the head moves to the new layer, even when
// the base tip trailed the head and the layer forms a branch.
// Metadata is replaced atomically afterwards; a failed metadata
// replace deletes the fresh layer and keeps the old metadata. A
// digest already in the graph is an idempotent success: it reports
// the existing node even when that node is parented on another tip
// than the base tip, and leaves the metadata and the head untouched.
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
	if err := ensureIncrSpace(req, repo, src); err != nil {
		return IncrResult{}, err
	}
	var tmp string
	typ := active.Format.Type
	compression := active.Format.Compression
	if req.Config.SplitSize > 0 {
		var stage string
		stage, tmp, err = packIncrStaged(req.Config.CacheDir, src, run, typ, compression)
		if err != nil {
			return IncrResult{}, err
		}
		defer func() { _ = os.RemoveAll(stage) }()
	} else {
		tmp, err = packIncrTemp(repo, src, run, typ, compression)
		if err != nil {
			return IncrResult{}, err
		}
	}
	return finishIncr(repo, req, active, tmp, now, seam)
}

func validateIncr(req IncrRequest) (Repo, Manifest, error) {
	repo, err := OpenRepo(req.Config.ImagesDir, req.Name)
	if err != nil {
		return Repo{}, Manifest{}, err
	}
	if err := checkNamePlacement(req.Config.ImagesDir, repo.Segments); err != nil {
		return Repo{}, Manifest{}, err
	}
	info, err := os.Stat(req.Upper)
	if err != nil {
		return Repo{}, Manifest{}, fmt.Errorf("build: upper: %w", err)
	}
	if !info.IsDir() {
		return Repo{}, Manifest{}, fmt.Errorf("build: upper %s is not a directory", req.Upper)
	}
	active, exists, err := loadActiveManifest(repo)
	if err != nil {
		return Repo{}, Manifest{}, fmt.Errorf("build: %w", err)
	}
	if !exists {
		return Repo{}, Manifest{}, errors.New("build: no active full image")
	}
	if err := checkIncrBase(active, req.BaseTip); err != nil {
		return Repo{}, Manifest{}, err
	}
	if err := checkIncrForm(active.PayloadKind(), req.Config.SplitSize); err != nil {
		return Repo{}, Manifest{}, err
	}
	if err := formatConflict(active.Format, req.Format, req.Compression); err != nil {
		return Repo{}, Manifest{}, err
	}
	if err := acceptStoredContainer(active.Format); err != nil {
		return Repo{}, Manifest{}, err
	}
	return repo, active, nil
}

// checkIncrBase validates the build base point: it must name a node
// of the manifest graph, and the root-to-base path plus the new
// layer must stay within the stack limit. An empty base builds on
// the head, so the limit is measured along the head stack, which
// preserves the historical whole-chain check. A base that trails the
// head is measured along its own branch instead.
func checkIncrBase(active Manifest, baseTip string) error {
	stack, err := active.Stack(incrParent(active, baseTip))
	if err != nil {
		return fmt.Errorf("build: base tip: %w", err)
	}
	if len(stack)+1 > maxStackDepth {
		// The count includes the full root, so it is reported as
		// images to match the documented stack limit.
		return fmt.Errorf(
			"build: stack to the base tip already holds %d images; build a new full image instead",
			len(stack))
	}
	return nil
}

// incrParent is the effective parent of a new layer: the requested
// base tip, or the manifest head when the request names none.
func incrParent(active Manifest, baseTip string) string {
	if baseTip != "" {
		return baseTip
	}
	return active.Head
}

// ErrFormatConflict means an incremental build named a container type
// or compression other than the active image. Callers map it to a
// usage error.
var ErrFormatConflict = errors.New("format does not match the active image")

// FormatConflict reports when typ or compression disagrees with the
// active image. Empty fields inherit and are not a conflict.
func FormatConflict(active Format, typ, compression string) error {
	return formatConflict(active, typ, compression)
}

// formatConflict refuses a requested type or compression that would
// diverge from the active image. Empty fields inherit.
func formatConflict(active Format, typ, compression string) error {
	var bad []string
	if typ != "" && typ != active.Type {
		bad = append(bad, "--format "+typ)
	}
	if compression != "" && compression != active.Compression {
		bad = append(bad, "--compression "+compression)
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("build: %s: %w (active %s/%s)",
		strings.Join(bad, ", "), ErrFormatConflict, active.Type, active.Compression)
}

// checkIncrForm keeps the active payload form aligned with the
// effective split size. Blocks layers only append onto blocks; a
// single-file active only accepts an unsplit build. A mismatch
// points at a full rebuild so the library stays consistent.
func checkIncrForm(kind string, splitSize int64) error {
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

// ensureIncrSpace requires room for the packed layer. A blocks layer
// also keeps a full copy in the cache while the repository receives
// the split. An empty cache dir is left to the staging helper.
func ensureIncrSpace(req IncrRequest, repo Repo, src string) error {
	need, err := estimateTree(src)
	if err != nil {
		return err
	}
	if req.Config.SplitSize > 0 {
		if strings.TrimSpace(req.Config.CacheDir) == "" {
			return nil
		}
		return ensureSpace(
			spaceWrite{path: req.Config.CacheDir, bytes: need},
			spaceWrite{path: repo.Dir, bytes: need},
		)
	}
	return ensureSpace(spaceWrite{path: filepath.Join(repo.Dir, dirIncr), bytes: need})
}

// packIncrTemp packs the staged upper into a synced temporary file
// inside the _incr directory of the repository.
func packIncrTemp(repo Repo, src string, run ToolRunner, typ, compression string) (string, error) {
	dir := filepath.Join(repo.Dir, dirIncr)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	name, err := tempName(dirIncr + squashExt)
	if err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	tmp := filepath.Join(dir, name)
	if err := packIncrImage(run, src, tmp, typ, compression); err != nil {
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
// packed bytes stay outside _incr/, which is not part of a blocks
// layout. The caller removes stage after the split finishes.
func packIncrStaged(cacheDir, src string, run ToolRunner, typ, compression string) (stage, tmp string, err error) {
	stage, err = makeStagingDir(cacheDir)
	if err != nil {
		return "", "", err
	}
	tmp = filepath.Join(stage, fileImage)
	if err = packIncrImage(run, src, tmp, typ, compression); err != nil {
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

// packIncrImage packs one layer through the registered container.
// The format keeps whiteouts and pins the creation timestamp so the
// same upper packs to the same bytes.
func packIncrImage(run ToolRunner, src, dst, typ, compression string) error {
	return packContainer(run, src, dst, typ, compression, true)
}

// finishIncr hashes the packed layer, appends the graph node, and
// publishes layer and metadata. A positive SplitSize splits the
// packed bytes into _blocks/<id>/; otherwise the layer lands as
// _incr/<id>.sqsh.
func finishIncr(
	repo Repo,
	req IncrRequest,
	active Manifest,
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
	if active.Has(id) {
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
	active Manifest,
	id string,
	sum [32]byte,
	tmp string,
	now func() time.Time,
	seam hooks,
) (IncrResult, error) {
	node, err := incrNode(active, req.BaseTip, id, sum, tmp, now, nil)
	if err != nil {
		removeFile(tmp)
		return IncrResult{}, err
	}
	next := withNode(active, node)
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
	if err := verifyFileSHA256(final, node.SHA256); err != nil {
		removeFile(final)
		return IncrResult{}, fmt.Errorf("build: %w", err)
	}
	if err := replaceMeta(repo, active, next, seam); err != nil {
		removeFile(final)
		return IncrResult{}, err
	}
	if err := runIncrSmoke(req, final, node, active.Format); err != nil {
		return IncrResult{}, err
	}
	return IncrResult{ID: id, Path: final, SHA256: node.SHA256}, nil
}

// finishIncrBlocks splits the packed layer into _blocks/<id>/ and
// records the block manifest on the new graph node. Digest and size
// stay those of the whole logical squashfs; no _incr/<id>.sqsh is
// written.
func finishIncrBlocks(
	repo Repo,
	req IncrRequest,
	active Manifest,
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
	node := ImageMeta{
		ID:        id,
		Kind:      KindIncremental,
		Parent:    incrParent(active, req.BaseTip),
		Size:      split.size,
		SHA256:    hexDigest(split.whole),
		MtimeUnix: info.ModTime().Unix(),
		CreatedAt: now().UTC(),
		Blocks:    split.blocks,
	}
	next := withNode(active, node)
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
	ok, err := dirMatchesBlocks(final, node.Blocks)
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
	if err := runIncrSmoke(req, final, node, active.Format); err != nil {
		return IncrResult{}, err
	}
	return IncrResult{ID: id, Path: final, SHA256: node.SHA256}, nil
}

// ErrSmokeSkipped marks a quality gate that could not run because
// mounting is unavailable here. The build treats it as success.
var ErrSmokeSkipped = errors.New("build: mount smoke gate skipped")

// smokeReady and smokeRun are the default quality gate's host check
// and command runner. Tests replace them to observe MountArgv
// without attaching FUSE.
var (
	smokeReady = defaultSmokeReady
	smokeRun   = defaultSmokeRun
)

func defaultSmokeRun(name string, args ...string) error {
	return runTool(name, nil, args...)
}

// defaultMountSmoke mounts path with the active image format and
// lists the root. The command is that format's MountArgv, so an
// erofs layer is not handed to squashfuse. Missing /dev/fuse or
// missing format tools become ErrSmokeSkipped; a mount that fails
// is a real error.
func defaultMountSmoke(format Format, path string) error {
	f, err := container.Lookup(format.Type)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	if err := smokeReady(f); err != nil {
		if errors.Is(err, workspace.ErrUnavailable) {
			return ErrSmokeSkipped
		}
		return err
	}
	return smokeMount(f, path)
}

// defaultSmokeReady reports when this host cannot mount format.
// Non-Linux, a missing /dev/fuse, a missing format tool, or a
// missing fusermount3 are all unavailable. fusermount3 is required
// to detach the smoke mount; skipping first avoids leaving it up.
func defaultSmokeReady(f container.Format) error {
	if runtime.GOOS != "linux" {
		return workspace.ErrUnavailable
	}
	if _, err := os.Stat("/dev/fuse"); err != nil {
		return fmt.Errorf("%w: /dev/fuse: %v", workspace.ErrUnavailable, err)
	}
	tools := append(append([]string{}, f.Tools()...), "fusermount3")
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%w: %s is missing", workspace.ErrUnavailable, tool)
		}
	}
	return nil
}

// smokeMount attaches path with format.MountArgv, lists the root,
// and detaches it. A failed mount does not unmount.
func smokeMount(f container.Format, imagePath string) error {
	point, err := os.MkdirTemp("", "tiler-smoke-")
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	defer func() { _ = os.RemoveAll(point) }()

	argv := f.MountArgv(imagePath, "", "", imagePath, point)
	if len(argv) == 0 {
		return errors.New("build: empty mount command")
	}
	if err := smokeRun(argv[0], argv[1:]...); err != nil {
		return fmt.Errorf("build: smoke mount: %w", err)
	}
	_, listErr := os.ReadDir(point)
	unmountErr := smokeRun("fusermount3", "-u", point)
	if listErr != nil {
		return fmt.Errorf("build: smoke read: %w", listErr)
	}
	if unmountErr != nil {
		return fmt.Errorf("build: smoke unmount: %w", unmountErr)
	}
	return nil
}

// runIncrSmoke runs the quality gate on a single-file layer path, or
// on a reassembled blocks layer when the node carries a Blocks
// manifest. A nil MountSmoke mounts through the active format.
func runIncrSmoke(req IncrRequest, path string, node ImageMeta, format Format) error {
	smoke := req.MountSmoke
	if smoke == nil {
		smoke = func(image string) error {
			return defaultMountSmoke(format, image)
		}
	}
	target := path
	if len(node.Blocks) > 0 {
		assembled, err := assembleIncrBlocks(req.Config.CacheDir, path, node)
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

// assembleIncrBlocks materializes _blocks/<id>/ into the content-
// addressed cache so the smoke gate can mount one image file.
func assembleIncrBlocks(cacheDir, blocksDir string, node ImageMeta) (string, error) {
	files := make([]BlockFile, 0, len(node.Blocks))
	for _, block := range node.Blocks {
		files = append(files, BlockFile{
			Path:      filepath.Join(blocksDir, block.Name),
			Name:      block.Name,
			Size:      block.Size,
			MtimeUnix: block.MtimeUnix,
			SHA256:    block.SHA256,
		})
	}
	return AssembleBlocks(cacheDir, files, node.SHA256, node.Size)
}

// reuseChainLayer is the idempotent rebuild of a single-file layer
// the graph already lists. A missing file is put back from the
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
// the graph already lists. A missing directory is rebuilt from the
// fresh packed bytes; an existing directory is re-checked against
// the recorded manifest.
func reuseChainLayerBlocks(
	repo Repo,
	active Manifest,
	id string,
	sum [32]byte,
	tmp string,
	seam hooks,
) (IncrResult, error) {
	node, ok := active.Node(id)
	if !ok {
		removeFile(tmp)
		return IncrResult{}, fmt.Errorf("build: image node %s is missing", id)
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
		match, err := dirMatchesBlocks(path, node.Blocks)
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
	split, err := splitStagedImage(tmp, payloadTmp, nodeBlockSize(node))
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
	match, err := dirMatchesBlocks(path, node.Blocks)
	if err != nil || !match {
		_ = os.RemoveAll(path)
		if err != nil {
			return IncrResult{}, fmt.Errorf("build: %w", err)
		}
		return IncrResult{}, fmt.Errorf("build: %s does not match the block manifest", path)
	}
	return IncrResult{ID: id, Path: path, SHA256: hexDigest(sum)}, nil
}

// nodeBlockSize recovers the block size used when the layer was
// first published: every full block shares that size, and a single
// short block uses its recorded size.
func nodeBlockSize(node ImageMeta) int64 {
	if len(node.Blocks) == 0 {
		return node.Size
	}
	return node.Blocks[0].Size
}

// incrNode assembles the new graph member. Its parent is the
// requested base tip, or the manifest head when the request names
// none.
func incrNode(
	active Manifest,
	baseTip string,
	id string,
	sum [32]byte,
	tmp string,
	now func() time.Time,
	blocks []BlockEntry,
) (ImageMeta, error) {
	info, err := os.Stat(tmp)
	if err != nil {
		return ImageMeta{}, fmt.Errorf("build: %w", err)
	}
	return ImageMeta{
		ID:        id,
		Kind:      KindIncremental,
		Parent:    incrParent(active, baseTip),
		Size:      info.Size(),
		SHA256:    hexDigest(sum),
		MtimeUnix: info.ModTime().Unix(),
		CreatedAt: now().UTC(),
		Blocks:    blocks,
	}, nil
}

// withNode returns the manifest with node appended to the graph and
// the head moved onto it, even when the node branches off the
// previous head.
func withNode(active Manifest, node ImageMeta) Manifest {
	next := active
	next.Version = ManifestVersion
	next.Images = append(slices.Clone(active.Images), node)
	next.Head = node.ID
	return next
}

// replaceMeta atomically swaps meta.yaml to the next manifest. When
// the swap fails after the rename landed, the previous manifest is
// written back so the graph and the incr files stay consistent.
func replaceMeta(repo Repo, old, next Manifest, seam hooks) error {
	body, err := EncodeManifest(next)
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
			_ = StoreManifest(repo.MetaPath(), old)
		}
		return fmt.Errorf("build: %w", err)
	}
	return nil
}
