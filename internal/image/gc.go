package image

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/jiangxin/tiler/internal/config"
)

const (
	// GCActionArchived is a hold promoted into archives.
	GCActionArchived = "archived"
	// GCActionRemoved is a deleted archive directory, or a branch
	// layer reclaimed from the active DAG.
	GCActionRemoved = "removed"
)

// GCEvent is one garbage collection action a command can report.
type GCEvent struct {
	Name   string
	Action string
	FullID string
}

// GC reclaims the unreferenced parts of the library: branch layers
// of an active DAG that neither the head nor a pinned tip reaches,
// every unreferenced hold promoted into archives, and the archives
// that exceed the retention count or age. A zero keep_versions or
// max_age leaves that rule unlimited; with both set, an archive
// beyond either rule is deleted. Held archives, active images,
// referenced holds, and session records are never deleted. An
// unreadable archive is kept.
func GC(cfg config.Config, refs Refs, now time.Time) ([]GCEvent, error) {
	repos, err := ListRepos(cfg.ImagesDir)
	if err != nil {
		return nil, fmt.Errorf("gc: %w", err)
	}
	var events []GCEvent
	for _, repo := range repos {
		repoEvents, err := gcRepo(repo, cfg, refs, now)
		events = append(events, repoEvents...)
		if err != nil {
			return events, fmt.Errorf("gc: %s: %w", repo.Name(), err)
		}
	}
	return events, nil
}

func gcRepo(repo Repo, cfg config.Config, refs Refs, now time.Time) ([]GCEvent, error) {
	reclaimed, err := reclaimBranches(repo, refs)
	if err != nil {
		return nil, err
	}
	promoted, err := promoteHolds(repo, refs)
	if err != nil {
		return reclaimed, err
	}
	expired, err := expireArchives(repo, cfg, refs, now)
	if err != nil {
		return append(reclaimed, promoted...), err
	}
	return append(append(reclaimed, promoted...), expired...), nil
}

// reclaimBranches prunes the active DAG back to the nodes the head
// and the pinned tips still reach. A layer file is reclaimable when
// it is not in Reachable(head) and no reference pins a tip whose
// reachable set covers it; reclaiming drops the node from the
// manifest and deletes its layer file. Hold archive decisions are a
// separate question and still ask refs by full id alone. The meta
// swap is guarded: the document is re-read immediately before it,
// and a manifest that changed since the snapshot — a concurrent
// publish landed — abandons this reclaim instead of silently
// rolling the writer back.
func reclaimBranches(repo Repo, refs Refs) ([]GCEvent, error) {
	manifest, err := LoadManifest(repo.MetaPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	root, err := manifest.Full()
	if err != nil {
		return nil, err
	}
	next, pruned, err := pruneUnpinnedNodes(repo, manifest, root.ID, refs)
	if err != nil || len(pruned) == 0 {
		return nil, err
	}
	changed, err := metaChangedSince(repo.MetaPath(), manifest)
	if err != nil {
		return nil, err
	}
	if changed {
		return nil, nil
	}
	if err := replaceMeta(repo, manifest, next, hooks{}); err != nil {
		return nil, err
	}
	var events []GCEvent
	for _, node := range pruned {
		if err := removeLayerFiles(repo, node); err != nil {
			return events, err
		}
		events = append(events, GCEvent{
			Name: repo.Name(), Action: GCActionRemoved, FullID: node.ID,
		})
	}
	return events, nil
}

// metaChangedSince re-reads the meta document at path and reports
// whether it no longer matches the snapshot's canonical encoding; a
// document that vanished counts as changed. The image package holds
// no repo-level lock, so the check narrows — but does not fully
// close — the window in which a concurrent writer can land between
// the comparison and the caller's rename.
func metaChangedSince(path string, snapshot Manifest) (bool, error) {
	current, err := LoadManifest(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	curBody, err := EncodeManifest(current)
	if err != nil {
		return false, err
	}
	oldBody, err := EncodeManifest(snapshot)
	if err != nil {
		return false, err
	}
	return !bytes.Equal(curBody, oldBody), nil
}

// pruneUnpinnedNodes splits the manifest into the kept DAG and the
// pruned nodes. A node off the head stack survives when a reference
// pins one of the tips whose reachable set covers it; the covering
// relation is ancestor-closed, so the kept side stays a valid DAG.
func pruneUnpinnedNodes(repo Repo, manifest Manifest, fullID string, refs Refs) (Manifest, []ImageMeta, error) {
	kept := manifest.Reachable(manifest.Head)
	next := manifest
	next.Images = make([]ImageMeta, 0, len(manifest.Images))
	var pruned []ImageMeta
	for _, node := range manifest.Images {
		drop := false
		if !kept[node.ID] {
			pinned, err := tipPinned(refs, repo.Name(), fullID,
				coveringTips(manifest, node.ID))
			if err != nil {
				return Manifest{}, nil, err
			}
			drop = !pinned
		}
		if drop {
			pruned = append(pruned, node)
			continue
		}
		next.Images = append(next.Images, node)
	}
	return next, pruned, nil
}

// coveringTips lists the tips whose reachable sets include id: the
// node itself and every descendant of it.
func coveringTips(manifest Manifest, id string) []string {
	tips := []string{id}
	for changed := true; changed; {
		changed = false
		for _, node := range manifest.Images {
			if node.Parent == "" ||
				!slices.Contains(tips, node.Parent) ||
				slices.Contains(tips, node.ID) {
				continue
			}
			tips = append(tips, node.ID)
			changed = true
		}
	}
	return tips
}

// tipPinned asks refs whether any of the tips is pinned for the
// full image. A Refs implementation that predates stack tips
// matches the full id alone, which conservatively answers held for
// every tip query while the full image is pinned.
func tipPinned(refs Refs, name, fullID string, tips []string) (bool, error) {
	for _, tip := range tips {
		held, err := heldBy(refs, Ref{Name: name, FullID: fullID, TipID: tip})
		if err != nil || held {
			return held, err
		}
	}
	return false, nil
}

// removeLayerFiles deletes the payload of one pruned node: a
// _blocks/<id>/ directory for a split layer, an _incr/<id>.sqsh
// file otherwise. A payload already gone is not an error.
func removeLayerFiles(repo Repo, node ImageMeta) error {
	if len(node.Blocks) > 0 {
		dir, err := repo.BlocksDirPath(node.ID)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		removeEmpty(filepath.Join(repo.Dir, dirBlocks))
		return nil
	}
	path, err := repo.IncrPath(node.ID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	removeEmpty(filepath.Join(repo.Dir, dirIncr))
	return nil
}

// promoteHolds renames every hold no reference pins into archives.
// Legacy 16-hex hold directories stay put; pin checks still see them
// through listHoldIDs, but they are not renamed.
func promoteHolds(repo Repo, refs Refs) ([]GCEvent, error) {
	ids, err := listHoldIDs(repo)
	if err != nil {
		return nil, err
	}
	var events []GCEvent
	for _, id := range ids {
		if legacyID(id) {
			continue
		}
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
			Name: repo.Name(), Action: GCActionArchived, FullID: id,
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
			Name: repo.Name(), Action: GCActionRemoved, FullID: archive.id,
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
		if !entry.IsDir() || !onDiskID(id) {
			continue
		}
		manifest, err := LoadManifest(filepath.Join(dir, id, fileMeta))
		if err != nil {
			continue
		}
		root, err := manifest.Full()
		if err != nil {
			continue
		}
		archives = append(archives, archiveInfo{id: id, createdAt: root.CreatedAt})
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
// the hold being checked, so a chain never references itself. The
// reference question asks by full id: pins that guard a hold or an
// archive name the full image, never a stack tip.
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
	manifest, err := LoadManifest(filepath.Join(chainDir, fileMeta))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, node := range manifest.Images {
		if node.Kind == KindIncremental && parents[node.ID] {
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
	active, exists, err := loadActiveManifest(repo)
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
		manifest, err := LoadManifest(filepath.Join(hold, fileMeta))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		addParents(parents, manifest)
	}
	return parents, nil
}

// addParents records every parent edge of the manifest: the ids the
// DAG's incremental nodes are built on.
func addParents(parents map[string]bool, manifest Manifest) {
	for _, node := range manifest.Images {
		if node.Parent != "" {
			parents[node.Parent] = true
		}
	}
}
