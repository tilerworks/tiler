package image

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// CleanupItem labels existing deletion recovery or an uncommitted object.
type CleanupItem struct {
	Name, ID, Kind string
	tree           map[string]os.FileInfo
}

// CleanupPlan is a read-only snapshot, never an authorization to expand work.
// A pending journal limits the whole GC run to its displayed cleanup batch.
type CleanupPlan struct {
	Items   []CleanupItem
	Pending bool

	journal     *removalJournal
	journalBody []byte
	journalInfo os.FileInfo
	repoTree    map[string]os.FileInfo
}

// InspectCleanupLocked lists existing cleanup without replaying any deletion.
// The caller must hold the library lock and separately validate session pins.
func InspectCleanupLocked(imagesDir string) (CleanupPlan, error) {
	plan, err := inspectPendingCleanup(imagesDir)
	if err != nil || plan.Pending {
		return plan, err
	}
	repos, err := ListRepos(imagesDir)
	if err != nil {
		return plan, err
	}
	for _, repo := range repos {
		manifest, err := LoadVersionManifest(repo)
		if err != nil {
			return plan, err
		}
		items, err := inspectOrphans(repo, manifest)
		if err != nil {
			return plan, err
		}
		plan.Items = append(plan.Items, items...)
	}
	return plan, nil
}

func inspectOrphans(repo Repo, manifest VersionManifest) ([]CleanupItem, error) {
	entries, err := os.ReadDir(filepath.Join(repo.Dir, dirObjects))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var items []CleanupItem
	for _, entry := range entries {
		if !entry.IsDir() || checkID(entry.Name()) != nil || slices.ContainsFunc(manifest.Images,
			func(node ImageMeta) bool { return node.ID == entry.Name() }) {
			continue
		}
		path, err := repo.ObjectDirPath(entry.Name())
		if err != nil {
			return nil, err
		}
		tree, err := cleanupTree(path)
		if err != nil {
			return nil, err
		}
		items = append(items, CleanupItem{repo.Name(), entry.Name(), "orphan", tree})
	}
	return items, nil
}

func removalJournalPaths(imagesDir string) ([]string, error) {
	var paths []string
	legacy := filepath.Join(imagesDir, versionRemovalJournal)
	info, err := os.Lstat(legacy)
	if err == nil && info.Mode().IsRegular() {
		paths = append(paths, legacy)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	root := filepath.Join(imagesDir, imageLockDir)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), versionRemovalJournal) {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(paths)
	return paths, nil
}

func inspectPendingCleanup(imagesDir string) (CleanupPlan, error) {
	plan := CleanupPlan{}
	paths, err := removalJournalPaths(imagesDir)
	if err != nil || len(paths) == 0 {
		return plan, err
	}
	path := paths[0]
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return plan, nil
	}
	if err != nil {
		return plan, err
	}
	if !info.Mode().IsRegular() {
		return plan, errors.New("image gc: removal journal is not a regular file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return plan, err
	}
	var j removalJournal
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&j); err != nil || !json.Valid(body) {
		return plan, errors.Join(errors.New("image gc: invalid removal journal"), err)
	}
	before, beforeErr := EncodeVersionManifest(j.Before)
	after, afterErr := EncodeVersionManifest(j.After)
	repo, err := OpenRepo(imagesDir, j.Name)
	if err != nil || beforeErr != nil || afterErr != nil || !validRemovalJournal(j) {
		return plan, errors.New("image gc: invalid removal journal snapshots")
	}
	if err := repo.checkObjectComponents(repo.MetaPath(), false); err != nil {
		return plan, err
	}
	for _, id := range j.IDs {
		if _, err := repo.ObjectDirPath(id); err != nil {
			return plan, err
		}
	}
	current, err := os.ReadFile(repo.MetaPath())
	if errors.Is(err, os.ErrNotExist) && j.Delete {
		current, err = after, nil
	}
	if err != nil {
		return plan, err
	}
	manifest, err := DecodeVersionManifest(current)
	if err != nil {
		return plan, err
	}
	canonical, err := EncodeVersionManifest(manifest)
	if err != nil || (!bytes.Equal(canonical, before) && !bytes.Equal(canonical, after)) {
		return plan, errors.New("image gc: removal journal conflicts with metadata")
	}
	tree, err := cleanupTree(repo.Dir)
	if err != nil {
		return plan, err
	}
	plan = CleanupPlan{Pending: true, journal: &j, journalBody: body, journalInfo: info, repoTree: tree}
	for _, id := range j.IDs {
		plan.Items = append(plan.Items, CleanupItem{Name: j.Name, ID: id, Kind: "pending-cleanup"})
	}
	if j.Delete {
		for rel := range tree {
			if filepath.Dir(rel) == "." && rel != "." && rel != dirObjects &&
				rel != fileMeta && rel != fileSHA256Index && rel != fileContentSHA256Index && rel != fileVersionRevision {
				return CleanupPlan{}, fmt.Errorf("image gc: unrecognized repository entry %q", rel)
			}
		}
		entries, err := os.ReadDir(filepath.Join(repo.Dir, dirObjects))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return CleanupPlan{}, err
		}
		for _, entry := range entries {
			if !entry.IsDir() || checkID(entry.Name()) != nil {
				return CleanupPlan{}, fmt.Errorf("image gc: unsafe pending object %q", entry.Name())
			}
		}
		orphans, err := inspectOrphans(repo, j.Before)
		if err != nil {
			return CleanupPlan{}, err
		}
		plan.Items = append(plan.Items, orphans...)
	}
	if len(plan.Items) == 0 {
		plan.Items = append(plan.Items, CleanupItem{Name: j.Name, Kind: "pending-cleanup"})
	}
	return plan, nil
}

// cleanupTree pins directory and leaf identities without reading payload bytes
// or following external symlinks. Replacements and ordinary edits revoke approval.
func cleanupTree(root string) (map[string]os.FileInfo, error) {
	tree := make(map[string]os.FileInfo)
	err := filepath.WalkDir(root, func(path string, _ os.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) && path == root {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err == nil {
			rel, _ := filepath.Rel(root, path)
			tree[rel] = info
		}
		return err
	})
	return tree, err
}

func sameCleanupTree(a, b map[string]os.FileInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for path, info := range a {
		other := b[path]
		if other == nil || !os.SameFile(info, other) || info.Size() != other.Size() ||
			info.Mode() != other.Mode() || !info.ModTime().Equal(other.ModTime()) {
			return false
		}
	}
	return true
}
