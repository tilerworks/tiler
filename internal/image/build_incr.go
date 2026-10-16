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
	// Path is the published incr/<id>.sqsh file.
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
// opaque markers survive. The layer is renamed into incr/ first and
// the metadata is replaced atomically afterwards; a failed metadata
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
	tmp, err := packIncrTemp(repo, src, run)
	if err != nil {
		return IncrResult{}, err
	}
	return finishIncr(repo, req, active, tmp, now, seam)
}

func validateIncr(req IncrRequest) (Repo, Meta, error) {
	repo, err := OpenRepo(req.Config.ImagesDir, req.Name)
	if err != nil {
		return Repo{}, Meta{}, err
	}
	if req.Config.SplitEnabled {
		return Repo{}, Meta{}, errors.New("build: split storage is enabled; incremental builds are refused")
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
	return repo, active, nil
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

// packIncrImage is the mksquashfs argument list for one layer.
// Xattrs stay stored so whiteout markers in the user namespace
// survive; mksquashfs keeps device nodes as they are.
func packIncrImage(run ToolRunner, src, dst string) error {
	return run("mksquashfs", src, dst, "-comp", "zstd", "-noappend", "-xattrs")
}

// finishIncr hashes the packed layer, appends the chain entry, and
// publishes layer and metadata.
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
		return reuseChainLayer(repo, id, sum, tmp, seam)
	}
	entry, err := chainEntry(req, active, id, sum, tmp, now)
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
	smoke := req.MountSmoke
	if smoke == nil {
		smoke = defaultMountSmoke
	}
	if err := smoke(final); err != nil && !errors.Is(err, ErrSmokeSkipped) {
		return IncrResult{}, fmt.Errorf("build: quality gate: %w", err)
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

// reuseChainLayer is the idempotent rebuild of a layer the chain
// already lists. A missing file is put back from the fresh bytes.
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

// chainEntry assembles the new chain member. Its parent is the last
// chain member, or the full image id when the chain is empty.
func chainEntry(
	req IncrRequest,
	active Meta,
	id string,
	sum [32]byte,
	tmp string,
	now func() time.Time,
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
