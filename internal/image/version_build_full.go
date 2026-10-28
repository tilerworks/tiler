package image

import (
	"cmp"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/jiangxin/tiler/internal/config"
)

// BuildFullVersion packs and publishes an immutable non-DMG full version.
// It is intentionally separate from BuildFull until the v3 runtime is active.
func BuildFullVersion(req FullRequest) (node ImageMeta, err error) {
	if req.Description, err = NormalizeDescription(req.Description); err != nil {
		return node, err
	}
	req.Format = cmp.Or(req.Format, req.Config.ImageFormat)
	req.Compression = cmp.Or(req.Compression, req.Config.ImageCompression)
	repo, err := validateFull(&req)
	if err != nil {
		return node, err
	}
	// The platform gate guards the real Linux-only toolchain. A caller
	// that injects the packer owns that requirement, so tests can pack
	// with a fake runner on any host.
	if (req.Run == nil && runtime.GOOS != "linux") || req.Format == FormatDMG {
		return node, errors.New("version build: full versions require Linux squashfs or erofs")
	}
	if err := prepareScene(req); err != nil {
		return node, err
	}
	if err := configOutsideStage(req); err != nil {
		return node, err
	}
	if err := ensureBlocksSpace(req.Config.CacheDir, repo.Dir, req.SrcDir); err != nil {
		return node, err
	}
	stage, tmp, err := packFullStaged(req)
	if err != nil {
		return node, err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	sum, err := fileSHA256(tmp)
	if err != nil {
		return node, err
	}
	info, err := os.Stat(tmp)
	if err != nil {
		return node, err
	}
	node = ImageMeta{ID: ID(sum), Kind: KindFull, Size: info.Size(), SHA256: ID(sum),
		MtimeUnix: info.ModTime().Unix(), CreatedAt: buildNow(req)().UTC(), Builder: req.Builder,
		Format: Format{Type: req.Format, Compression: req.Compression}, Description: req.Description}
	payload := tmp
	if size := cmp.Or(req.BlockSize, req.Config.SplitSize); size > 0 {
		dir := filepath.Join(stage, "split")
		if err := os.Mkdir(dir, 0o755); err != nil {
			return ImageMeta{}, err
		}
		split, err := splitStagedImage(tmp, dir, size)
		if err != nil {
			return ImageMeta{}, err
		}
		node.Blocks, payload = split.blocks, dir
	}
	if err := runFullVersionSmoke(req, payload, node); err != nil {
		return ImageMeta{}, err
	}
	return PublishFullVersion(req.Config.ImagesDir, repo.Name(), payload, node, req.DescriptionSet)
}

func configOutsideStage(req FullRequest) error {
	if req.Config.CacheDir == "" {
		return errors.New("version build: cache_dir is required")
	}
	return config.OutsideReserved(req.Config.CacheDir, config.Config{ImagesDir: req.Config.ImagesDir})
}

func packFullStaged(req FullRequest) (string, string, error) {
	stage, err := makeStagingDir(req.Config.CacheDir)
	if err != nil {
		return "", "", err
	}
	tmp := filepath.Join(stage, fileImage)
	if err := packStagedImage(toolRunner(req), req.SrcDir, tmp, req.Format, req.Compression); err != nil {
		_ = os.RemoveAll(stage)
		return "", "", err
	}
	return stage, tmp, nil
}

func runFullVersionSmoke(req FullRequest, payload string, node ImageMeta) error {
	return runIncrSmoke(IncrRequest{Config: req.Config, MountSmoke: req.MountSmoke}, payload, node, node.Format)
}
