package session

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
)

// ReferenceTarget identifies one complete, library-scoped version hash.
type ReferenceTarget struct{ Name, Tip string }

// References returns verified reference snapshots. After its lock is released,
// the result cannot authorize a later mutation; use ReferencesLocked for that.
func (c ReferenceCoordinator) References(targets ...ReferenceTarget) (buckets []ReferenceBucket, err error) {
	if err := validateReferenceTargets(targets); err != nil {
		return nil, err
	}
	err = c.withLibrary(func() error {
		buckets, err = c.ReferencesLocked(targets...)
		return err
	})
	return buckets, err
}

// ReferencesLocked requires the caller to hold the library lock through this
// lookup and any authorized mutation. It never acquires that lock recursively.
func (c ReferenceCoordinator) ReferencesLocked(targets ...ReferenceTarget) ([]ReferenceBucket, error) {
	_, buckets, err := c.trustedReferencesLocked(targets)
	return buckets, err
}

func validateReferenceTargets(targets []ReferenceTarget) error {
	for _, target := range targets {
		if err := (ReferenceBucket{Name: target.Name, Tip: target.Tip}).validate(); err != nil {
			return err
		}
	}
	return nil
}

func (c ReferenceCoordinator) trustedReferencesLocked(targets []ReferenceTarget) (ReferenceState, []ReferenceBucket, error) {
	if err := validateReferenceTargets(targets); err != nil {
		return ReferenceState{}, nil, err
	}
	state, err := c.ensureInventoryLocked(false, nil)
	if err != nil {
		return state, nil, err
	}
	buckets, err := c.readTrustedReferenceBuckets(state, targets)
	if err == nil {
		return state, buckets, nil
	}
	state, err = c.ensureInventoryLocked(true, nil)
	if err == nil {
		buckets, err = c.readTrustedReferenceBuckets(state, targets)
	}
	if err != nil {
		return state, nil, err
	}
	return state, buckets, nil
}

func (s Store) readTrustedReferenceBuckets(state ReferenceState, targets []ReferenceTarget) ([]ReferenceBucket, error) {
	buckets := make([]ReferenceBucket, 0, len(targets))
	for _, target := range targets {
		expected, found := state.ShardDigests[referenceCatalogKey(target.Name, target.Tip)]
		bucket := ReferenceBucket{Name: target.Name, Tip: target.Tip}
		if found {
			loaded, data, err := s.loadReferenceBucket(target.Name, target.Tip)
			if err != nil {
				return nil, err
			}
			digest := sha256.Sum256(data)
			if hex.EncodeToString(digest[:]) != expected {
				return nil, fmt.Errorf("session: reference shard %s:%s digest mismatch", target.Name, target.Tip)
			}
			bucket = loaded
		}
		buckets = append(buckets, bucket)
	}
	return buckets, nil
}

func referenceTargets(records ...*Record) []ReferenceTarget {
	seen := make(map[ReferenceTarget]bool)
	for _, rec := range records {
		for key := range recordReferences(rec) {
			seen[ReferenceTarget{key.name, key.tip}] = true
		}
	}
	targets := make([]ReferenceTarget, 0, len(seen))
	for target := range seen {
		targets = append(targets, target)
	}
	slices.SortFunc(targets, func(a, b ReferenceTarget) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Tip, b.Tip))
	})
	return targets
}
