package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// RemoveVersionSetLocked removes exactly ids after the caller has checked
// references under the library lock. Retained nodes must keep their parents.
// Pending cleanup is refused, never implicitly replayed by a GC execution.
func RemoveVersionSetLocked(repo Repo, ids []string) error {
	return removeVersionSetLocked(repo, ids, hooks{})
}

func removeVersionSetLocked(repo Repo, ids []string, seam hooks) error {
	if err := CheckVersionGCReadyLocked(libraryRoot(repo)); err != nil {
		return err
	}
	before, err := LoadVersionManifest(repo)
	if err != nil || len(ids) == 0 {
		return err
	}
	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		if checkID(id) != nil || selected[id] {
			return errors.New("image gc: invalid or duplicate approved version")
		}
		selected[id] = true
	}
	after := before
	after.Images = slices.DeleteFunc(slices.Clone(before.Images), func(node ImageMeta) bool { return selected[node.ID] })
	if len(after.Images)+len(ids) != len(before.Images) {
		return errors.New("image gc: approved version disappeared")
	}
	if err := after.Validate(); err != nil {
		return fmt.Errorf("image gc: retained dependencies: %w", err)
	}
	removeLeaf, err := gcCanRemoveLeaf(repo, after, selected)
	if err != nil {
		return err
	}
	j := removalJournal{Name: repo.Name(), Before: before, After: after, IDs: ids, Delete: removeLeaf}
	if err := saveRemovalJournal(libraryRoot(repo), j, seam); err != nil {
		return err
	}
	return replayVersionRemoval(libraryRoot(repo), j, seam)
}

// gcCanRemoveLeaf keeps unapproved orphan objects for their own preview.
func gcCanRemoveLeaf(repo Repo, after VersionManifest, selected map[string]bool) (bool, error) {
	if len(after.Images) != 0 {
		return false, nil
	}
	entries, err := os.ReadDir(filepath.Join(repo.Dir, dirObjects))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for _, entry := range entries {
		if !selected[entry.Name()] {
			return false, nil
		}
	}
	return true, nil
}
