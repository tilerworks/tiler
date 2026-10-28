package session

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"slices"
)

// referenceReplayUpdates uses verified shards on the normal transaction path.
// A resumed transaction may already have written its replacement shards, so a
// digest mismatch requires source reconstruction rather than trusting new bytes
// or rejecting every legitimate interrupted replay. The library lock keeps all
// coordinated source writers out while the affected owners are reconstructed.
func (s Store) referenceReplayUpdates(state ReferenceState, journal ReferenceJournal) ([]ReferenceBucket, error) {
	var sources map[referenceKey]ReferenceBucket
	return s.referenceUpdates(journal, func(name, tip string) (ReferenceBucket, error) {
		if sources == nil && state.CatalogComplete {
			expected, found := state.ShardDigests[referenceCatalogKey(name, tip)]
			if !found {
				return ReferenceBucket{}, os.ErrNotExist
			}
			bucket, data, err := s.loadReferenceBucket(name, tip)
			digest := sha256.Sum256(data)
			if err == nil && hex.EncodeToString(digest[:]) == expected {
				return bucket, nil
			}
		}
		if sources == nil {
			var err error
			sources, err = s.referenceSourceBuckets()
			if err != nil {
				return ReferenceBucket{}, err
			}
		}
		bucket := sources[referenceKey{name, tip}]
		bucket.Name, bucket.Tip = name, tip
		return bucket, nil
	})
}

func (s Store) referenceSourceBuckets() (map[referenceKey]ReferenceBucket, error) {
	sources, err := s.referenceSources()
	if err != nil {
		return nil, err
	}
	buckets := make(map[referenceKey]ReferenceBucket)
	for _, name := range slices.Sorted(maps.Keys(sources)) {
		rec, err := s.loadReferenceSource(name)
		if err != nil {
			return nil, err
		}
		if rec == nil {
			return nil, fmt.Errorf("session: reference source disappeared: %s", name)
		}
		for key, owners := range recordReferences(rec) {
			bucket := buckets[key]
			bucket.Name, bucket.Tip = key.name, key.tip
			bucket.Owners = append(bucket.Owners, owners...)
			buckets[key] = bucket
		}
	}
	return buckets, nil
}
