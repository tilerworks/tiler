package image

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/jiangxin/tiler/internal/config"
)

// BuildIncrVersion packs a new layer independently of its pinned parent's format.
func BuildIncrVersion(req IncrRequest) (node ImageMeta, err error) {
	repo, err := OpenRepo(req.Config.ImagesDir, req.Name)
	if err != nil {
		return node, err
	}
	if req.Description, err = NormalizeDescription(req.Description); err != nil {
		return node, err
	}
	full := FullRequest{
		Format: cmp.Or(req.Format, req.Config.ImageFormat), Compression: cmp.Or(req.Compression, req.Config.ImageCompression), Now: req.Now,
	}
	if err := resolveFullRequest(&full); err != nil {
		return node, err
	}
	// The platform gate guards the real Linux-only toolchain. A caller
	// that injects the packer owns that requirement, so tests can pack
	// with a fake runner on any host.
	if (req.Run == nil && runtime.GOOS != "linux") || full.Format == FormatDMG {
		return node, errors.New("version build: incremental layers require Linux squashfs or erofs")
	}
	if err := checkSourceDir(req.Upper, config.Config{ImagesDir: req.Config.ImagesDir, CacheDir: req.Config.CacheDir}); err != nil {
		return node, err
	}
	parent, err := incrVersionParent(repo, req)
	if err != nil {
		return node, err
	}
	if err := config.OutsideReserved(req.Config.CacheDir, config.Config{ImagesDir: req.Config.ImagesDir}); err != nil {
		return node, err
	}
	if err := ensureBlocksSpace(req.Config.CacheDir, repo.Dir, req.Upper); err != nil {
		return node, err
	}
	src, restore, err := stageUpper(req.Upper, req.Subdir)
	if err != nil {
		return node, err
	}
	defer func() { err = errors.Join(err, restore()) }()
	stage, tmp, err := packIncrStaged(req.Config.CacheDir, src, toolRunner(FullRequest{Run: req.Run}), full.Format, full.Compression)
	if err != nil {
		return node, err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	sum, err := fileSHA256(tmp)
	if err != nil {
		return node, err
	}
	if parent.ID == ID(sum) {
		if req.DescriptionSet && parent.Description != req.Description {
			return node, fmt.Errorf("version publish: use images describe %s:%s to change description", repo.Name(), parent.ID)
		}
		return parent, nil
	}
	node, err = incrNode(Manifest{}, parent.ID, ID(sum), sum, tmp, buildNow(full), nil)
	if err != nil {
		return node, err
	}
	node.Format, node.Description = Format{Type: full.Format, Compression: full.Compression}, req.Description
	node.placeAfter = true
	if err := runIncrSmoke(req, tmp, node, node.Format); err != nil {
		return ImageMeta{}, err
	}
	if req.Config.SplitSize > 0 {
		dir := filepath.Join(stage, "split")
		if err := os.Mkdir(dir, 0o755); err != nil {
			return ImageMeta{}, err
		}
		split, err := splitStagedImage(tmp, dir, req.Config.SplitSize)
		if err != nil {
			return ImageMeta{}, err
		}
		node.Blocks, tmp = split.blocks, dir
	}
	return PublishIncrementalVersion(req.Config.ImagesDir, repo.Name(), tmp, node, parent, req.DescriptionSet)
}

func incrVersionParent(repo Repo, req IncrRequest) (ImageMeta, error) {
	release, err := LockLibrary(req.Config.ImagesDir, DefaultLibraryLockTimeout)
	if err != nil {
		return ImageMeta{}, err
	}
	defer release()
	if err := PrepareTargetLocked(req.Config.ImagesDir); err != nil {
		return ImageMeta{}, err
	}
	manifest, _, err := loadVersionMeta(repo)
	if err != nil {
		return ImageMeta{}, err
	}
	selector := repo.Name()
	if req.BaseTip != "" {
		if err := checkID(req.BaseTip); err != nil {
			return ImageMeta{}, err
		}
		selector += ":" + req.BaseTip
	}
	stack, err := manifest.SelectVersion(selector)
	if err != nil {
		return ImageMeta{}, err
	}
	for _, member := range stack {
		if member.Format.Type != FormatSquashfs && member.Format.Type != FormatErofs {
			return ImageMeta{}, errors.New("version build: incremental bases require squashfs or erofs")
		}
	}
	return stack[len(stack)-1], nil
}
