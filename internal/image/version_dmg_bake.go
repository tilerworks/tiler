package image

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/container"
)

func bakeDMGVersion(repo Repo, req DMGRequest) (ImageMeta, error) {
	base, path, err := versionBakeSource(repo, req)
	if err != nil {
		return ImageMeta{}, err
	}
	if req.Compression == "" {
		req.Compression = base.Format.Compression
	}
	if err := requireDMGCompression(req.Compression); err != nil {
		return ImageMeta{}, err
	}
	content, err := versionShadowContent(req)
	if err != nil {
		return ImageMeta{}, err
	}
	if idle, err := commitDMGVersion(repo, req, "", content); err != nil || idle.ID != "" {
		return idle, err
	}
	dir, err := os.MkdirTemp("", "tiler-dmg-bake-")
	if err != nil {
		return ImageMeta{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := config.OutsideReserved(dir, req.Config); err != nil {
		return ImageMeta{}, err
	}
	need, err := estimateFiles(path, req.Shadow)
	if err != nil {
		return ImageMeta{}, err
	}
	if err := ensureSpace(spaceWrite{dir, need}, spaceWrite{repo.Dir, need}); err != nil {
		return ImageMeta{}, err
	}
	staged := filepath.Join(dir, ImageDMG)
	argv, consErr := container.HdiutilConvertArgv(path, req.Compression, base.Format.Compression, req.Shadow, staged)
	if err := runDMG(req, argv, consErr, staged); err != nil {
		return ImageMeta{}, err
	}
	content, err = versionShadowContent(req)
	if err != nil {
		return ImageMeta{}, err
	}
	return commitDMGVersion(repo, req, staged, content)
}

func versionBakeSource(repo Repo, req DMGRequest) (ImageMeta, string, error) {
	release, err := LockLibrary(req.Config.ImagesDir, DefaultLibraryLockTimeout)
	if err != nil {
		return ImageMeta{}, "", err
	}
	defer release()
	manifest, _, err := loadVersionMeta(repo)
	if err != nil {
		return ImageMeta{}, "", err
	}
	return mountedDMGVersion(repo, manifest, req.MountedID)
}

func mountedDMGVersion(repo Repo, manifest VersionManifest, id string) (ImageMeta, string, error) {
	if err := checkID(id); err != nil {
		return ImageMeta{}, "", err
	}
	for _, node := range manifest.Images {
		if node.ID != id || node.Kind != KindFull || node.Format.Type != FormatDMG {
			continue
		}
		leaf, err := repo.ObjectPayloadPath(node)
		if err != nil {
			return ImageMeta{}, "", err
		}
		// A linked version stores its payload as a symlink or a
		// path-ref, so convert and verify the external file itself.
		path, err := ResolvePayloadBytesPath(leaf)
		if err != nil {
			return ImageMeta{}, "", err
		}
		if _, err := verifyVersionFile(path, node); err != nil {
			return ImageMeta{}, "", err
		}
		return node, path, nil
	}
	return ImageMeta{}, "", errors.New("version bake: pinned full DMG version not found")
}

func versionShadowContent(req DMGRequest) (string, error) {
	content, err := shadowContentSHA256(req.Shadow)
	if err != nil {
		return "", err
	}
	return ID(sha256.Sum256([]byte("dmg-shadow\x00" + req.MountedID + "\x00" + content))), nil
}
