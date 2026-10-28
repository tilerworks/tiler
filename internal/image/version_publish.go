package image

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/config"
)

// PublishFullVersion publishes a staged single-file full version under a lock.
// Staging must be outside imagesDir; the caller always owns its cleanup.
func PublishFullVersion(imagesDir, name, staged string, node ImageMeta, descriptionSet bool) (ImageMeta, error) {
	repo, err := OpenRepo(imagesDir, name)
	if err != nil {
		return ImageMeta{}, err
	}
	release, err := LockLibrary(imagesDir, DefaultLibraryLockTimeout)
	if err != nil {
		return ImageMeta{}, err
	}
	defer release()
	if err := checkNamePlacement(imagesDir, repo.Segments); err != nil {
		return ImageMeta{}, err
	}
	return publishFullVersion(imagesDir, repo, staged, node, descriptionSet, hooks{})
}

func publishFullVersion(imagesDir string, repo Repo, staged string, node ImageMeta, descriptionSet bool, seam hooks) (ImageMeta, error) {
	if node.Kind != KindFull || len(node.Blocks) != 0 {
		return ImageMeta{}, errors.New("version publish: single-file full image required")
	}
	if err := (VersionManifest{Version: VersionManifestVersion, Name: repo.Name(), Images: []ImageMeta{node}}).Validate(); err != nil {
		return ImageMeta{}, err
	}
	if _, _, err := loadVersionMeta(repo); err != nil {
		return ImageMeta{}, err
	}
	dst, err := repo.ObjectPayloadPath(node)
	if err != nil {
		return ImageMeta{}, err
	}
	if err := config.OutsideReserved(staged, config.Config{ImagesDir: imagesDir}); err != nil {
		return ImageMeta{}, fmt.Errorf("version publish: invalid staging path %q: %w", staged, err)
	}
	if _, err := verifyVersionFile(staged, node); err != nil {
		return ImageMeta{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return ImageMeta{}, err
	}
	if err := copyVersionFile(staged, dst); err != nil {
		return ImageMeta{}, err
	}
	info, err := verifyVersionFile(dst, node)
	if err != nil {
		return ImageMeta{}, err
	}
	node.MtimeUnix = info.ModTime().Unix()
	// Flush the object and every new directory entry up to the library root.
	dir := dst
	for range len(repo.Segments) + 4 {
		if err := syncDir(dir, seam); err != nil {
			return ImageMeta{}, err
		}
		dir = filepath.Dir(dir)
	}
	return appendFullVersion(repo, node, descriptionSet, seam)
}

func verifyVersionFile(path string, node ImageMeta) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("version publish: regular file required: %s", path)
	}
	if ok, err := fileMatches(path, node.Size, node.SHA256); err != nil || !ok {
		return nil, errors.Join(err, fmt.Errorf("version publish: payload mismatch: %s", path))
	}
	return info, nil
}

func copyVersionFile(src, dst string) error {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err == nil {
		_, err = io.Copy(out, in)
		err = errors.Join(err, in.Close())
	}
	err = errors.Join(err, out.Sync(), out.Close())
	if err != nil {
		err = errors.Join(err, os.Remove(dst))
	}
	return err
}
