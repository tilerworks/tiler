package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jiangxin/tiler/internal/config"
)

// GCActionRemoved reports an orphan immutable object deletion.
const GCActionRemoved = "removed"

// GCEvent is one garbage collection action a command can report.
type GCEvent struct {
	Name   string
	Action string
	FullID string
}

// GC recovers interrupted removal and deletes complete object directories
// that are not referenced by v3 metadata. Every recorded version is retained.
func GC(cfg config.Config, _ Refs, _ time.Time) ([]GCEvent, error) {
	release, err := LockLibrary(cfg.ImagesDir, DefaultLibraryLockTimeout)
	if err != nil {
		return nil, fmt.Errorf("gc: %w", err)
	}
	defer release()
	if err := recoverVersionRemoval(cfg.ImagesDir, hooks{}); err != nil {
		return nil, fmt.Errorf("gc: %w", err)
	}
	repos, err := ListRepos(cfg.ImagesDir)
	if err != nil {
		return nil, fmt.Errorf("gc: %w", err)
	}
	var events []GCEvent
	for _, repo := range repos {
		manifest, err := LoadVersionManifest(repo)
		if err != nil {
			return events, fmt.Errorf("gc: %s: %w", repo.Name(), err)
		}
		repoEvents, err := removeOrphanVersionObjects(repo, manifest)
		events = append(events, repoEvents...)
		if err != nil {
			return events, fmt.Errorf("gc: %s: %w", repo.Name(), err)
		}
	}
	return events, nil
}

func removeOrphanVersionObjects(repo Repo, manifest VersionManifest) ([]GCEvent, error) {
	dir := filepath.Join(repo.Dir, dirObjects)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	retained := make(map[string]bool, len(manifest.Images))
	for _, node := range manifest.Images {
		retained[node.ID] = true
	}
	var events []GCEvent
	for _, entry := range entries {
		id := entry.Name()
		if !entry.IsDir() || checkID(id) != nil || retained[id] {
			continue
		}
		path, err := repo.ObjectDirPath(id)
		if err != nil {
			return events, err
		}
		if err := os.RemoveAll(path); err != nil {
			return events, err
		}
		events = append(events, GCEvent{Name: repo.Name(), Action: GCActionRemoved, FullID: id})
	}
	return events, nil
}
