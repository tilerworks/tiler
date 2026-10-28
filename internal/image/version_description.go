package image

import (
	"errors"
	"fmt"
)

// UpdateVersionDescription replaces mutable description metadata when the
// caller's snapshot is still current. The full ID prevents a short selector
// from being resolved differently after concurrent metadata changes.
func UpdateVersionDescription(imagesDir, name, id, old, next string) (bool, error) {
	normalized, err := NormalizeDescription(next)
	if err != nil {
		return false, err
	}
	release, err := LockLibrary(imagesDir, DefaultLibraryLockTimeout)
	if err != nil {
		return false, err
	}
	defer release()
	if err := recoverVersionRemoval(imagesDir, hooks{}); err != nil {
		return false, err
	}
	repo, err := OpenRepo(imagesDir, name)
	if err != nil {
		return false, err
	}
	manifest, previous, err := loadVersionMeta(repo)
	if err != nil {
		return false, err
	}
	for i := range manifest.Images {
		node := &manifest.Images[i]
		if node.ID != id {
			continue
		}
		if node.Description != old {
			if node.Description == normalized {
				return false, nil
			}
			return false, errors.New("image description changed concurrently")
		}
		if old == normalized {
			return false, nil
		}
		node.Description = normalized
		body, err := EncodeVersionManifest(manifest)
		if err != nil {
			return false, err
		}
		if err := replaceVersionMeta(repo, previous, body, hooks{}); err != nil {
			return false, err
		}
		_, err = EnsureVersionIndexes(repo)
		return err == nil, err
	}
	return false, fmt.Errorf("image version %s was removed", id)
}
