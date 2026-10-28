package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
)

// replayReferenceJournal completes an already durable intent. The caller holds
// the library and named session locks; index trust is established separately.
// A checkpoint error simulates interruption after a durable transition.
func (s Store) replayReferenceJournal(state ReferenceState, journal ReferenceJournal, checkpoint func(string) error) error {
	name, err := s.checkReferenceReplay(state, journal)
	if err != nil {
		return err
	}
	updates, err := s.referenceReplayUpdates(state, journal)
	if err != nil {
		return err
	}
	state.Generation, state.Dirty = journal.Generation, true
	state.Inventory = maps.Clone(state.Inventory)
	state.ShardDigests = maps.Clone(state.ShardDigests)
	if state.Inventory == nil {
		state.Inventory = make(map[string]ReferenceRevision)
	}
	if state.ShardDigests == nil {
		state.ShardDigests = make(map[string]string)
	}
	if err := referenceCheckpoint("dirty", s.SaveReferenceState(state), checkpoint); err != nil {
		return err
	}
	if err := referenceCheckpoint("source", s.writeReferenceSource(name, journal.After), checkpoint); err != nil {
		return err
	}
	for _, bucket := range updates {
		if err := s.SaveReferenceBucket(bucket); err != nil {
			return err
		}
		path, _ := s.referenceBucketPath(bucket.Name, bucket.Tip)
		if err := referenceCheckpoint("bucket", s.syncReferenceParents(filepath.Dir(path)), checkpoint); err != nil {
			return err
		}
		digest, err := s.referenceShardDigest(bucket.Name, bucket.Tip)
		if err != nil {
			return err
		}
		state.ShardDigests[referenceCatalogKey(bucket.Name, bucket.Tip)] = digest
	}
	delete(state.Inventory, name)
	if journal.After != nil {
		revision, err := s.referenceRevision(*journal.After)
		if err != nil {
			return err
		}
		state.Inventory[name] = revision
	}
	if err := referenceCheckpoint("inventory", s.SaveReferenceState(state), checkpoint); err != nil {
		return err
	}
	state.Dirty = false
	if err := referenceCheckpoint("clean", s.SaveReferenceState(state), checkpoint); err != nil {
		return err
	}
	path, err := s.referencePath("journal.json")
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return referenceCheckpoint("removed", s.syncReferenceParents(filepath.Dir(path)), checkpoint)
}

func (s Store) checkReferenceReplay(state ReferenceState, journal ReferenceJournal) (string, error) {
	if err := errors.Join(state.validate(), journal.validate()); err != nil {
		return "", err
	}
	if journal.Generation != state.Generation && (state.Dirty || state.Generation == ^uint64(0) || journal.Generation != state.Generation+1) {
		return "", errors.New("session: inconsistent reference transaction generation")
	}
	rec := journal.After
	if rec == nil {
		rec = journal.Before
	}
	source, err := s.loadReferenceSource(rec.Name)
	if err != nil {
		return "", err
	}
	after := sameReferenceRecord(source, journal.After)
	if (!after && !sameReferenceRecord(source, journal.Before)) || (!state.Dirty && state.Generation == journal.Generation && !after) {
		return "", errors.New("session: reference journal conflicts with source record")
	}
	return rec.Name, nil
}

func (s Store) loadReferenceSource(name string) (*Record, error) {
	path, err := s.recordPath(name)
	if err != nil {
		return nil, err
	}
	if _, err := s.safeStoreFile(path); err != nil {
		return nil, err
	}
	rec, err := s.LoadVersion(name)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return &rec, err
}

func sameReferenceRecord(a, b *Record) bool {
	left, errA := json.Marshal(a)
	right, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(left, right)
}

func (s Store) writeReferenceSource(name string, after *Record) error {
	path, _ := s.recordPath(name)
	if after != nil {
		if err := s.SaveVersion(*after); err != nil {
			return err
		}
	} else if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.syncReferenceParents(filepath.Dir(path))
}

func (s Store) referenceRevision(rec Record) (ReferenceRevision, error) {
	path, _ := s.recordPath(rec.Name)
	data, err := os.ReadFile(path)
	if err != nil {
		return ReferenceRevision{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return ReferenceRevision{}, err
	}
	digest := sha256.Sum256(data)
	return ReferenceRevision{rec.InstanceID, hex.EncodeToString(digest[:]), info.Size(), info.ModTime().UnixNano()}, nil
}

func referenceCheckpoint(stage string, err error, checkpoint func(string) error) error {
	if err == nil && checkpoint != nil {
		err = checkpoint(stage)
	}
	if err != nil {
		return fmt.Errorf("session: reference transaction %s: %w", stage, err)
	}
	return nil
}
