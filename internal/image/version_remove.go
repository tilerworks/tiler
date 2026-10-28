package image

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

const versionRemovalJournal = ".version-removal.json"

// ErrCleanupPending means metadata committed but obsolete objects still need
// recovery. A later mutation retries the durable journal before proceeding.
var ErrCleanupPending = errors.New("version removal: cleanup pending")

type removalJournal struct {
	Name   string          `json:"name"`
	Before VersionManifest `json:"before"`
	After  VersionManifest `json:"after"`
	IDs    []string        `json:"ids"`
	Delete bool            `json:"delete_repository,omitempty"`
}

// RemoveVersionSubtreeLocked commits removal of root and all descendants.
// The caller must hold the library lock and must already have checked refs.
func RemoveVersionSubtreeLocked(imagesDir, name, root string) ([]ImageMeta, error) {
	if err := recoverVersionRemoval(imagesDir, hooks{}); err != nil {
		return nil, err
	}
	repo, err := OpenRepo(imagesDir, name)
	if err != nil {
		return nil, err
	}
	manifest, err := LoadVersionManifest(repo)
	if err != nil {
		return nil, err
	}
	forest, err := NewForest(manifest.Images)
	if err != nil {
		return nil, err
	}
	nodes, err := forest.Descendants(root)
	if err != nil {
		return nil, err
	}
	removed := make(map[string]bool, len(nodes))
	ids := make([]string, len(nodes))
	for i, node := range nodes {
		removed[node.ID], ids[i] = true, node.ID
	}
	after := manifest
	after.Images = slices.DeleteFunc(slices.Clone(manifest.Images), func(node ImageMeta) bool {
		return removed[node.ID]
	})
	j := removalJournal{Name: name, Before: manifest, After: after, IDs: ids}
	if err := saveRemovalJournal(imagesDir, j, hooks{}); err != nil {
		return nil, err
	}
	if err := replayVersionRemoval(imagesDir, j, hooks{}); err != nil {
		return nil, err
	}
	return nodes, nil
}

// RemoveVersionRepositoryLocked commits removal of every version and then
// removes the repository leaf. The journal and library lock remain outside it.
func RemoveVersionRepositoryLocked(imagesDir, name string) ([]ImageMeta, error) {
	if err := recoverVersionRemoval(imagesDir, hooks{}); err != nil {
		return nil, err
	}
	repo, err := OpenRepo(imagesDir, name)
	if err != nil {
		return nil, err
	}
	manifest, err := LoadVersionManifest(repo)
	if err != nil {
		return nil, err
	}
	forest, err := NewForest(manifest.Images)
	if err != nil {
		return nil, err
	}
	nodes := forest.Topology()
	slices.Reverse(nodes)
	ids := make([]string, len(nodes))
	for i := range nodes {
		ids[i] = nodes[i].ID
	}
	after := manifest
	after.Images = nil
	j := removalJournal{Name: name, Before: manifest, After: after, IDs: ids, Delete: true}
	if err := saveRemovalJournal(imagesDir, j, hooks{}); err != nil {
		return nil, err
	}
	if err := replayVersionRemoval(imagesDir, j, hooks{}); err != nil {
		return nil, err
	}
	return nodes, nil
}

func recoverVersionRemoval(imagesDir string, seam hooks) error {
	body, err := os.ReadFile(filepath.Join(imagesDir, versionRemovalJournal))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var j removalJournal
	if err := json.Unmarshal(body, &j); err != nil {
		return fmt.Errorf("version removal: read journal: %w", err)
	}
	return replayVersionRemoval(imagesDir, j, seam)
}

func replayVersionRemoval(imagesDir string, j removalJournal, seam hooks) error {
	repo, err := OpenRepo(imagesDir, j.Name)
	before, beforeErr := EncodeVersionManifest(j.Before)
	after, afterErr := EncodeVersionManifest(j.After)
	if err != nil || beforeErr != nil || afterErr != nil || !validRemovalJournal(j) {
		return errors.Join(err, beforeErr, afterErr, errors.New("version removal: invalid journal"))
	}
	if err == nil && j.Delete {
		if _, statErr := os.Stat(repo.MetaPath()); errors.Is(statErr, os.ErrNotExist) {
			if removeErr := os.RemoveAll(repo.Dir); removeErr != nil {
				return fmt.Errorf("%w: %v", ErrCleanupPending, removeErr)
			}
			removeEmptyAncestors(filepath.Dir(repo.Dir), imagesDir)
			return finishRemovalJournal(imagesDir, seam)
		}
	}
	current, previous, err := loadVersionMeta(repo)
	if err != nil {
		return err
	}
	currentBody, currentErr := EncodeVersionManifest(current)
	if currentErr != nil {
		return currentErr
	}
	if string(currentBody) == string(before) {
		if err := replaceVersionMeta(repo, previous, after, seam); err != nil {
			return err
		}
	} else if string(currentBody) != string(after) {
		return errors.New("version removal: journal conflicts with metadata")
	}
	for _, id := range j.IDs {
		dir, pathErr := repo.ObjectDirPath(id)
		if pathErr != nil {
			return pathErr
		}
		if seam.before != nil {
			if err := seam.before(dir, ""); err != nil {
				return fmt.Errorf("%w: %v", ErrCleanupPending, err)
			}
		}
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("%w: %v", ErrCleanupPending, err)
		}
	}
	if j.Delete {
		if err := os.RemoveAll(repo.Dir); err != nil {
			return fmt.Errorf("%w: %v", ErrCleanupPending, err)
		}
		removeEmptyAncestors(filepath.Dir(repo.Dir), imagesDir)
	}
	return finishRemovalJournal(imagesDir, seam)
}

func finishRemovalJournal(imagesDir string, seam hooks) error {
	if err := os.Remove(filepath.Join(imagesDir, versionRemovalJournal)); err != nil {
		return fmt.Errorf("%w: %v", ErrCleanupPending, err)
	}
	return syncDir(imagesDir, seam)
}

func validRemovalJournal(j removalJournal) bool {
	if (!j.Delete && len(j.IDs) == 0) || j.Before.Name != j.Name || j.After.Name != j.Name {
		return false
	}
	removed := make(map[string]bool, len(j.IDs))
	for _, id := range j.IDs {
		if checkID(id) != nil || removed[id] {
			return false
		}
		removed[id] = true
	}
	want := slices.DeleteFunc(slices.Clone(j.Before.Images), func(node ImageMeta) bool { return removed[node.ID] })
	if j.Delete && len(want) != 0 {
		return false
	}
	return slices.EqualFunc(want, j.After.Images, func(a, b ImageMeta) bool { return a.ID == b.ID }) &&
		len(want)+len(removed) == len(j.Before.Images)
}

func saveRemovalJournal(imagesDir string, j removalJournal, seam hooks) error {
	body, err := json.Marshal(j)
	if err != nil {
		return err
	}
	tmp, err := writeTemp(imagesDir, versionRemovalJournal, body, hooks{})
	if err != nil {
		return err
	}
	defer removeFile(tmp)
	return seam.do(tmp, filepath.Join(imagesDir, versionRemovalJournal))
}
