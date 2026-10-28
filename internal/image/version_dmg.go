package image

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/container"
)

// BuildDMGVersion creates an immutable full version without activating v3 runtime.
func BuildDMGVersion(req DMGRequest) (ImageMeta, error) {
	repo, err := OpenRepo(req.Config.ImagesDir, req.Name)
	if err != nil {
		return ImageMeta{}, err
	}
	if req.Description, err = NormalizeDescription(req.Description); err != nil {
		return ImageMeta{}, err
	}
	if req.SrcDir == "" && req.Shadow != "" {
		return bakeDMGVersion(repo, req)
	}
	if req.SrcDir == "" || req.Shadow != "" || req.MountedID != "" {
		return ImageMeta{}, errors.New("version build: directory source required")
	}
	if err := checkSourceDir(req.SrcDir, req.Config); err != nil {
		return ImageMeta{}, err
	}
	if req.Compression == "" {
		req.Compression = container.DMG().DefaultCompression()
	}
	if err := requireDMGCompression(req.Compression); err != nil {
		return ImageMeta{}, err
	}
	content, err := dirContentSHA256(req.SrcDir)
	if err != nil {
		return ImageMeta{}, err
	}
	if idle, err := commitDMGVersion(repo, req, "", content); err != nil || idle.ID != "" {
		return idle, err
	}
	dir, err := os.MkdirTemp("", "tiler-dmg-")
	if err != nil {
		return ImageMeta{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := config.OutsideReserved(dir, req.Config); err != nil {
		return ImageMeta{}, err
	}
	need, err := estimateTree(req.SrcDir)
	if err != nil {
		return ImageMeta{}, err
	}
	if err := ensureSpace(spaceWrite{dir, need}, spaceWrite{repo.Dir, need}); err != nil {
		return ImageMeta{}, err
	}
	staged := filepath.Join(dir, ImageDMG)
	argv, consErr := container.HdiutilCreateArgv(req.SrcDir, req.Compression, repo.Segments[len(repo.Segments)-1], staged)
	if err := runDMG(req, argv, consErr, staged); err != nil {
		return ImageMeta{}, err
	}
	return commitDMGVersion(repo, req, staged, content)
}

// commitDMGVersion checks logical reuse and commits under the same library lock.
// An empty staged path performs only the pre-build reuse check.
func commitDMGVersion(repo Repo, req DMGRequest, staged, content string) (ImageMeta, error) {
	release, err := LockLibrary(req.Config.ImagesDir, DefaultLibraryLockTimeout)
	if err != nil {
		return ImageMeta{}, err
	}
	defer release()
	if err := recoverVersionRemoval(req.Config.ImagesDir, hooks{}); err != nil {
		return ImageMeta{}, err
	}
	if err := checkNamePlacement(req.Config.ImagesDir, repo.Segments); err != nil {
		return ImageMeta{}, err
	}
	m, _, err := loadVersionMeta(repo)
	if err != nil {
		return ImageMeta{}, err
	}
	if req.MountedID != "" {
		if _, _, err := mountedDMGVersion(repo, m, req.MountedID); err != nil {
			return ImageMeta{}, err
		}
	}
	var matches []ImageMeta
	for _, node := range m.Images {
		if node.Kind == KindFull && node.Format == (Format{Type: FormatDMG, Compression: req.Compression}) && node.ContentSHA256 == content {
			matches = append(matches, node)
		}
	}
	forest, err := NewForest(matches)
	if err != nil {
		return ImageMeta{}, err
	}
	if node, found := forest.Latest(); found {
		path, err := repo.ObjectPayloadPath(node)
		if err == nil {
			err = verifyVersionPayload(path, node)
		}
		if err != nil {
			return ImageMeta{}, err
		}
		node.Description = req.Description
		return appendFullVersion(repo, node, req.DescriptionSet, hooks{})
	}
	if staged == "" {
		return ImageMeta{}, nil
	}
	manifest, err := dmgManifest(repo, req, staged, req.Compression, content)
	if err != nil {
		return ImageMeta{}, err
	}
	node := manifest.Images[0]
	node.Format, node.Description = manifest.Format, req.Description
	return publishFullVersion(req.Config.ImagesDir, repo, staged, node, req.DescriptionSet, hooks{})
}
