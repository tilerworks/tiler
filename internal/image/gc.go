package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jiangxin/tiler/internal/config"
)

const (
	// GCActionArchived is a hold promoted into archives.
	GCActionArchived = "archived"
	// GCActionRemoved is an archive directory deleted.
	GCActionRemoved = "removed"
)

// GCEvent is one garbage collection action a command can report.
type GCEvent struct {
	Owner  string
	Repo   string
	Action string
	FullID string
}

// GC promotes every unreferenced hold into archives and deletes the
// archives that exceed the retention count or age across the whole
// library. A zero keep_versions or max_age leaves that rule
// unlimited; with both set, an archive beyond either rule is
// deleted. Held archives, active images, referenced holds, and
// session records are never deleted. An unreadable archive is kept.
func GC(cfg config.Config, refs Refs, now time.Time) ([]GCEvent, error) {
	repos, err := listRepos(cfg.ImagesDir)
	if err != nil {
		return nil, fmt.Errorf("gc: %w", err)
	}
	var events []GCEvent
	for _, repo := range repos {
		repoEvents, err := gcRepo(repo, cfg, refs, now)
		events = append(events, repoEvents...)
		if err != nil {
			return events, fmt.Errorf("gc: %s/%s: %w", repo.Owner, repo.Repo, err)
		}
	}
	return events, nil
}

// listRepos enumerates every valid owner/repo directory under the
// image library. Entries that do not parse as names are skipped.
func listRepos(imagesDir string) ([]Repo, error) {
	owners, err := os.ReadDir(imagesDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var repos []Repo
	for _, owner := range owners {
		if !owner.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(imagesDir, owner.Name()))
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			repo, err := OpenRepo(imagesDir, owner.Name()+"/"+entry.Name())
			if err != nil {
				continue
			}
			repos = append(repos, repo)
		}
	}
	return repos, nil
}

func gcRepo(repo Repo, cfg config.Config, refs Refs, now time.Time) ([]GCEvent, error) {
	promoted, err := promoteHolds(repo, refs)
	if err != nil {
		return nil, err
	}
	expired, err := expireArchives(repo, cfg, refs, now)
	if err != nil {
		return promoted, err
	}
	return append(promoted, expired...), nil
}

// promoteHolds renames every hold no reference pins into archives.
func promoteHolds(repo Repo, refs Refs) ([]GCEvent, error) {
	ids, err := listHoldIDs(repo)
	if err != nil {
		return nil, err
	}
	var events []GCEvent
	for _, id := range ids {
		hold, err := repo.HoldPath(id)
		if err != nil {
			return events, err
		}
		referenced, err := chainReferenced(repo, hold, id, refs, id)
		if err != nil {
			return events, err
		}
		if referenced {
			continue
		}
		if err := archiveHold(repo, id, hooks{}); err != nil {
			return events, err
		}
		events = append(events, GCEvent{
			Owner: repo.Owner, Repo: repo.Repo,
			Action: GCActionArchived, FullID: id,
		})
	}
	return events, nil
}

// archiveInfo is one archive ranked for retention.
type archiveInfo struct {
	id        string
	createdAt time.Time
}

// expireArchives deletes archives beyond the retention rules,
// newest first, and keeps every archive a reference pins.
func expireArchives(repo Repo, cfg config.Config, refs Refs, now time.Time) ([]GCEvent, error) {
	dir := filepath.Join(repo.Dir, dirArchives)
	archives, err := rankArchives(dir)
	if err != nil {
		return nil, err
	}
	var events []GCEvent
	for rank, archive := range archives {
		if !archiveExpired(cfg, now, rank, archive.createdAt) {
			continue
		}
		arch := filepath.Join(dir, archive.id)
		referenced, err := chainReferenced(repo, arch, archive.id, refs, "")
		if err != nil {
			return events, err
		}
		if referenced {
			continue
		}
		if err := os.RemoveAll(arch); err != nil {
			return events, err
		}
		events = append(events, GCEvent{
			Owner: repo.Owner, Repo: repo.Repo,
			Action: GCActionRemoved, FullID: archive.id,
		})
	}
	removeEmpty(dir)
	return events, nil
}

// rankArchives lists readable archives newest first. Parked,
// unreadable, and foreign entries never rank and are kept.
func rankArchives(dir string) ([]archiveInfo, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var archives []archiveInfo
	for _, entry := range entries {
		id := entry.Name()
		if !entry.IsDir() || checkID(id) != nil {
			continue
		}
		meta, err := Load(filepath.Join(dir, id, fileMeta))
		if err != nil {
			continue
		}
		archives = append(archives, archiveInfo{id: id, createdAt: meta.CreatedAt})
	}
	sort.Slice(archives, func(i, j int) bool {
		if archives[i].createdAt.Equal(archives[j].createdAt) {
			return archives[i].id < archives[j].id
		}
		return archives[i].createdAt.After(archives[j].createdAt)
	})
	return archives, nil
}

func archiveExpired(cfg config.Config, now time.Time, rank int, createdAt time.Time) bool {
	if cfg.KeepVersions > 0 && rank >= cfg.KeepVersions {
		return true
	}
	return cfg.MaxAge > 0 && createdAt.Before(now.Add(-cfg.MaxAge))
}

// chainReferenced reports whether a session reference or the parent
// link of another live chain in this repository still points at the
// chain's full image or one of its members. chainDir is the hold or
// archive directory holding the chain metadata; excludeHold names
// the hold being checked, so a chain never references itself.
func chainReferenced(repo Repo, chainDir, id string, refs Refs, excludeHold string) (bool, error) {
	held, err := heldBy(refs, refFor(repo, id))
	if err != nil || held {
		return held, err
	}
	parents, err := liveChainParents(repo, excludeHold)
	if err != nil {
		return false, err
	}
	if parents[id] {
		return true, nil
	}
	meta, err := Load(filepath.Join(chainDir, fileMeta))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range meta.Chain {
		if parents[entry.ID] {
			return true, nil
		}
	}
	return false, nil
}

// liveChainParents collects every id the active chain and the hold
// chains of one repository point at through their parent links. The
// hold named by excludeHold does not count.
func liveChainParents(repo Repo, excludeHold string) (map[string]bool, error) {
	parents := make(map[string]bool)
	active, exists, err := loadActive(repo)
	if err != nil {
		return nil, err
	}
	if exists {
		addParents(parents, active)
	}
	ids, err := listHoldIDs(repo)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if id == excludeHold {
			continue
		}
		hold, err := repo.HoldPath(id)
		if err != nil {
			return nil, err
		}
		meta, err := Load(filepath.Join(hold, fileMeta))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		addParents(parents, meta)
	}
	return parents, nil
}

func addParents(parents map[string]bool, meta Meta) {
	for _, entry := range meta.Chain {
		parents[entry.Parent] = true
	}
}
