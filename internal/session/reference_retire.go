package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// retireEmptyReferenceShards deletes cataloged shards whose owners are gone.
// A complete catalog with no sessions and no remaining shards is removed, so a
// deleted session does not leave .refs behind. Callers hold the library lock.
// A pending journal keeps its empty replacement files; replay still needs them.
func (s Store) retireEmptyReferenceShards() error {
	if _, err := s.LoadReferenceJournal(); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state, err := s.LoadReferenceState()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || state.Dirty {
		return err
	}
	changed, err := s.dropEmptyCatalogShards(&state)
	if err != nil {
		return err
	}
	if state.CatalogComplete && len(state.Inventory) == 0 && len(state.ShardDigests) == 0 {
		return s.removeReferenceDir()
	}
	if changed {
		if err := s.SaveReferenceState(state); err != nil {
			return err
		}
	}
	if state.CatalogComplete {
		return s.pruneUncatalogedReferenceShards(state)
	}
	return nil
}

// finishReferenceState retires empty shards after a trusted read or rebuild.
// The returned state matches what a later locked read will observe.
func (c ReferenceCoordinator) finishReferenceState(state ReferenceState) (ReferenceState, error) {
	if err := c.retireEmptyReferenceShards(); err != nil {
		return ReferenceState{}, err
	}
	loaded, err := c.LoadReferenceState()
	if errors.Is(err, os.ErrNotExist) {
		state.Dirty = false
		state.CatalogComplete = true
		state.Inventory = map[string]ReferenceRevision{}
		state.ShardDigests = map[string]string{}
		return state, nil
	}
	if err != nil {
		return ReferenceState{}, err
	}
	return loaded, nil
}

func (s Store) dropEmptyCatalogShards(state *ReferenceState) (bool, error) {
	changed := false
	for key, digest := range state.ShardDigests {
		name, tip, _ := strings.Cut(key, ":")
		bucket, data, err := s.loadReferenceBucket(name, tip)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != digest || len(bucket.Owners) != 0 {
			continue
		}
		if err := s.removeReferenceBucket(name, tip); err != nil {
			return false, err
		}
		delete(state.ShardDigests, key)
		changed = true
	}
	return changed, nil
}

func (s Store) removeReferenceBucket(name, tip string) error {
	path, err := s.referenceBucketPath(name, tip)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := removeDirIfEmpty(filepath.Dir(path)); err != nil {
		return err
	}
	root, err := s.referenceRoot()
	if err != nil {
		return err
	}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return s.syncReferenceParents(root)
}

// retireReplayedShards deletes only the empty replacements this journal wrote.
// Unrelated catalog entries, including empty shards from other images, stay.
func (s Store) retireReplayedShards(updates []ReferenceBucket) error {
	if _, err := s.LoadReferenceJournal(); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state, err := s.LoadReferenceState()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || state.Dirty {
		return err
	}
	changed := false
	for _, bucket := range updates {
		if len(bucket.Owners) != 0 {
			continue
		}
		key := referenceCatalogKey(bucket.Name, bucket.Tip)
		if digest, found := state.ShardDigests[key]; found {
			loaded, data, loadErr := s.loadReferenceBucket(bucket.Name, bucket.Tip)
			if loadErr == nil {
				sum := sha256.Sum256(data)
				if hex.EncodeToString(sum[:]) != digest || len(loaded.Owners) != 0 {
					continue
				}
			} else if !errors.Is(loadErr, os.ErrNotExist) {
				return loadErr
			}
			delete(state.ShardDigests, key)
			changed = true
		}
		if err := s.removeReferenceBucket(bucket.Name, bucket.Tip); err != nil {
			return err
		}
	}
	if state.CatalogComplete && len(state.Inventory) == 0 && len(state.ShardDigests) == 0 {
		return s.removeReferenceDir()
	}
	if changed {
		return s.SaveReferenceState(state)
	}
	return nil
}

func (s Store) removeReferenceDir() error {
	if _, err := s.LoadReferenceJournal(); err == nil {
		return errors.New("session: reference journal still exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir, err := s.referenceRoot()
	if err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("session: unsafe reference path %s", dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return s.syncReferenceParents(s.Dir)
}

func (s Store) pruneUncatalogedReferenceShards(state ReferenceState) error {
	root, err := s.referenceRoot()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		dir := filepath.Join(root, entry.Name())
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("session: unsafe reference path %s", dir)
		}
		if !entry.IsDir() {
			continue
		}
		shards, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, shard := range shards {
			if err := pruneReferenceShard(dir, shard, state); err != nil {
				return err
			}
		}
		if err := removeDirIfEmpty(dir); err != nil {
			return err
		}
	}
	return s.syncReferenceParents(root)
}

func pruneReferenceShard(dir string, shard os.DirEntry, state ReferenceState) error {
	path := filepath.Join(dir, shard.Name())
	if shard.Type()&os.ModeSymlink != 0 || !shard.Type().IsRegular() {
		return fmt.Errorf("session: unsafe reference path %s", path)
	}
	if !strings.HasSuffix(shard.Name(), ".json") {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var bucket ReferenceBucket
	if json.Unmarshal(data, &bucket) == nil {
		if _, found := state.ShardDigests[referenceCatalogKey(bucket.Name, bucket.Tip)]; found {
			return nil
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// referenceRoot is the .refs directory. referencePath requires a regular file,
// so the directory itself cannot be checked through that helper.
func (s Store) referenceRoot() (string, error) {
	if _, err := s.referencePath("state.json"); err != nil {
		return "", err
	}
	return filepath.Join(s.Dir, referenceDir), nil
}

func removeDirIfEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return nil
	}
	return os.Remove(dir)
}
