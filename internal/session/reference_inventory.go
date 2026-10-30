package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// EnsureInventory checks source metadata without parsing unchanged records. The
// result is a snapshot, not permission to delete: callers must coordinate their
// subsequent index use with the library lock and verify requested shard digests.
func (c ReferenceCoordinator) EnsureInventory() (state ReferenceState, err error) {
	err = c.withLibrary(func() error {
		state, err = c.ensureInventoryLocked(false, nil)
		return err
	})
	return state, err
}

// RebuildInventory reconstructs derived references from all validated sources.
func (c ReferenceCoordinator) RebuildInventory() (state ReferenceState, err error) {
	err = c.withLibrary(func() error {
		state, err = c.ensureInventoryLocked(true, nil)
		return err
	})
	return state, err
}

func (c ReferenceCoordinator) ensureInventoryLocked(force bool, checkpoint func(string) error) (ReferenceState, error) {
	if _, err := c.LoadReferenceJournal(); err == nil {
		if err := c.recoverLocked(); err != nil {
			return ReferenceState{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ReferenceState{}, err
	}
	state, stateErr := c.LoadReferenceState()
	sources, err := c.referenceSources()
	if err != nil {
		return ReferenceState{}, err
	}
	if !force && stateErr == nil && !state.Dirty && state.CatalogComplete && inventoryMatches(state, sources) {
		return c.finishReferenceState(state)
	}
	if stateErr != nil {
		state = ReferenceState{Version: ReferenceControlVersion}
	}
	return c.rebuildInventoryLocked(state, sources, checkpoint)
}

func (s Store) referenceSources() (map[string]os.FileInfo, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]os.FileInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	sources := make(map[string]os.FileInfo)
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), referenceDir) || ValidateName(entry.Name()) != nil ||
			(!entry.IsDir() && entry.Type()&os.ModeSymlink == 0) {
			continue
		}
		path, _ := s.recordPath(entry.Name())
		if _, err := s.safeStoreFile(path); err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("session: inspect reference source %s: %w", path, err)
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("session: inspect reference source %s: %w", path, err)
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
		sources[entry.Name()] = info
	}
	return sources, nil
}

func inventoryMatches(state ReferenceState, sources map[string]os.FileInfo) bool {
	if len(state.Inventory) != len(sources) {
		return false
	}
	for name, info := range sources {
		revision, found := state.Inventory[name]
		if !found || revision.Size != info.Size() || revision.ModifiedNano != info.ModTime().UnixNano() {
			return false
		}
	}
	return true
}

func (c ReferenceCoordinator) rebuildInventoryLocked(previous ReferenceState, sources map[string]os.FileInfo, checkpoint func(string) error) (ReferenceState, error) {
	if previous.Generation == ^uint64(0) {
		return ReferenceState{}, errors.New("session: reference generation exhausted")
	}
	state := ReferenceState{Version: ReferenceControlVersion, Generation: previous.Generation + 1, Dirty: true,
		Inventory: make(map[string]ReferenceRevision), ShardDigests: make(map[string]string)}
	buckets := make(map[referenceKey]ReferenceBucket)
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		err := c.withSession(name, func() error {
			rec, err := c.loadReferenceSource(name)
			if err != nil {
				return err
			}
			if rec == nil {
				return fmt.Errorf("session: reference source disappeared: %s", filepath.Join(c.Dir, name, recordFileName))
			}
			revision, err := c.referenceRevision(*rec)
			if err != nil {
				return err
			}
			state.Inventory[name] = revision
			for key, owners := range recordReferences(rec) {
				bucket := buckets[key]
				bucket.Name, bucket.Tip = key.name, key.tip
				bucket.Owners = append(bucket.Owners, owners...)
				buckets[key] = bucket
			}
			return nil
		})
		if err != nil {
			return ReferenceState{}, err
		}
	}
	if err := referenceCheckpoint("rebuild dirty", c.SaveReferenceState(state), checkpoint); err != nil {
		return ReferenceState{}, err
	}
	keys := make([]string, 0, len(buckets))
	byKey := make(map[string]ReferenceBucket, len(buckets))
	for key, bucket := range buckets {
		id := referenceCatalogKey(key.name, key.tip)
		keys, byKey[id] = append(keys, id), bucket
	}
	slices.Sort(keys)
	for _, key := range keys {
		bucket := byKey[key]
		if err := c.SaveReferenceBucket(bucket); err != nil {
			return ReferenceState{}, err
		}
		path, _ := c.referenceBucketPath(bucket.Name, bucket.Tip)
		if err := referenceCheckpoint("rebuild shard", c.syncReferenceParents(filepath.Dir(path)), checkpoint); err != nil {
			return ReferenceState{}, err
		}
		digest, err := c.referenceShardDigest(bucket.Name, bucket.Tip)
		if err != nil {
			return ReferenceState{}, err
		}
		state.ShardDigests[key] = digest
	}
	state.Dirty, state.CatalogComplete = false, true
	if err := referenceCheckpoint("rebuild clean", c.SaveReferenceState(state), checkpoint); err != nil {
		return ReferenceState{}, err
	}
	return c.finishReferenceState(state)
}
