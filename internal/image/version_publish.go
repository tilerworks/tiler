package image

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/config"
)

// PublishFullVersion publishes a staged full file or split directory under a lock.
// Staging must be outside imagesDir; the caller always owns its cleanup.
func PublishFullVersion(imagesDir, name, staged string, node ImageMeta, descriptionSet bool) (ImageMeta, error) {
	if node.Kind != KindFull {
		return ImageMeta{}, errors.New("version publish: full image required")
	}
	return publishVersionLocked(imagesDir, name, staged, node, nil, descriptionSet)
}

// PublishFullVersionLocked publishes while the caller holds the library lock.
// It exists for compound publication and reference mutations.
func PublishFullVersionLocked(imagesDir, name, staged string, node ImageMeta, descriptionSet bool) (ImageMeta, error) {
	if node.Kind != KindFull {
		return ImageMeta{}, errors.New("version publish: full image required")
	}
	return publishVersionCallerLocked(imagesDir, name, staged, node, nil, descriptionSet)
}

// PublishIncrementalVersion pins publication to a captured parent's identity.
// The parent may be any retained version; staging remains caller-owned outside imagesDir.
func PublishIncrementalVersion(imagesDir, name, staged string, node, parent ImageMeta, descriptionSet bool) (ImageMeta, error) {
	if node.Kind != KindIncremental || node.Parent != parent.ID || parent.ID == "" {
		return ImageMeta{}, errors.New("version publish: incremental image requires its pinned parent")
	}
	return publishVersionLocked(imagesDir, name, staged, node, &parent, descriptionSet)
}

// PublishIncrementalVersionLocked publishes against a pinned parent while the
// caller holds the library lock.
func PublishIncrementalVersionLocked(imagesDir, name, staged string, node, parent ImageMeta, descriptionSet bool) (ImageMeta, error) {
	if node.Kind != KindIncremental || node.Parent != parent.ID || parent.ID == "" {
		return ImageMeta{}, errors.New("version publish: incremental image requires its pinned parent")
	}
	return publishVersionCallerLocked(imagesDir, name, staged, node, &parent, descriptionSet)
}

func publishVersionLocked(imagesDir, name, staged string, node ImageMeta, parent *ImageMeta, descriptionSet bool) (ImageMeta, error) {
	release, err := LockLibrary(imagesDir, DefaultLibraryLockTimeout)
	if err != nil {
		return ImageMeta{}, err
	}
	defer release()
	return publishVersionCallerLocked(imagesDir, name, staged, node, parent, descriptionSet)
}

func publishVersionCallerLocked(imagesDir, name, staged string, node ImageMeta, parent *ImageMeta, descriptionSet bool) (ImageMeta, error) {
	if err := PrepareTargetLocked(imagesDir); err != nil {
		return ImageMeta{}, err
	}
	repo, err := OpenRepo(imagesDir, name)
	if err != nil {
		return ImageMeta{}, err
	}
	if err := checkNamePlacement(imagesDir, repo.Segments); err != nil {
		return ImageMeta{}, err
	}
	return publishVersion(imagesDir, repo, staged, node, parent, descriptionSet, hooks{})
}

func publishFullVersion(imagesDir string, repo Repo, staged string, node ImageMeta, descriptionSet bool, seam hooks) (ImageMeta, error) {
	if node.Kind != KindFull {
		return ImageMeta{}, errors.New("version publish: full image required")
	}
	return publishVersion(imagesDir, repo, staged, node, nil, descriptionSet, seam)
}

func publishVersion(imagesDir string, repo Repo, staged string, node ImageMeta, parent *ImageMeta, descriptionSet bool, seam hooks) (ImageMeta, error) {
	manifest, _, err := loadVersionMeta(repo)
	if err != nil {
		return ImageMeta{}, err
	}
	if parent != nil {
		var found bool
		for _, old := range manifest.Images {
			found = found || (old.ID == parent.ID && sameVersionIdentity(old, *parent))
		}
		if !found || node.Kind != KindIncremental || node.Parent != parent.ID {
			return ImageMeta{}, errors.New("version publish: pinned parent is missing or changed")
		}
	}
	if err := versionWithNode(manifest, node).Validate(); err != nil {
		return ImageMeta{}, err
	}
	dst, err := repo.ObjectPayloadPath(node)
	if err != nil {
		return ImageMeta{}, err
	}
	if err := config.OutsideReserved(staged, config.Config{ImagesDir: imagesDir}); err != nil {
		return ImageMeta{}, fmt.Errorf("version publish: invalid staging path %q: %w", staged, err)
	}
	if len(node.Blocks) != 0 {
		return publishSplitVersion(repo, staged, node, descriptionSet, seam)
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
	return appendVersion(repo, node, descriptionSet, seam)
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

// verifyVersionPayload verifies a published library leaf. The leaf may
// be a plain file, a symlink, or a path-ref to an external image, so
// the real bytes are resolved before the size and digest are compared.
func verifyVersionPayload(leaf string, node ImageMeta) error {
	path, err := ResolvePayloadBytesPath(leaf)
	if err != nil {
		return err
	}
	_, err = verifyVersionFile(path, node)
	return err
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
