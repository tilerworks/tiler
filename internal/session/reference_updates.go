package session

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"slices"
)

type referenceKey struct{ name, tip string }

// ReferenceUpdates plans complete shard replacements without writing any data.
// Callers must hold the library lock and establish index trust independently.
// Missing old shards require rebuilding; new shards may start empty only within
// that trusted transaction. Persist empty replacements so replay remains safe.
func (s Store) ReferenceUpdates(journal ReferenceJournal) ([]ReferenceBucket, error) {
	return s.referenceUpdates(journal, s.LoadReferenceBucket)
}

func (s Store) referenceUpdates(journal ReferenceJournal, load func(string, string) (ReferenceBucket, error)) ([]ReferenceBucket, error) {
	if err := journal.validate(); err != nil {
		return nil, err
	}
	before, after := recordReferences(journal.Before), recordReferences(journal.After)
	keys := make(map[referenceKey]bool)
	for _, refs := range []map[referenceKey][]ReferenceOwner{before, after} {
		for key := range refs {
			keys[key] = true
		}
	}
	updates := make([]ReferenceBucket, 0, len(keys))
	for key := range keys {
		bucket, err := load(key.name, key.tip)
		if errors.Is(err, os.ErrNotExist) && len(before[key]) == 0 {
			bucket, err = ReferenceBucket{Name: key.name, Tip: key.tip}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("session: plan reference %s:%s: %w", key.name, key.tip, err)
		}
		bucket.Owners = slices.DeleteFunc(bucket.Owners, func(owner ReferenceOwner) bool {
			return snapshotOwns(journal.Before, owner) || snapshotOwns(journal.After, owner)
		})
		bucket.Owners = append(bucket.Owners, after[key]...)
		slices.SortFunc(bucket.Owners, compareReferenceOwners)
		updates = append(updates, bucket)
	}
	slices.SortFunc(updates, func(a, b ReferenceBucket) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Tip, b.Tip))
	})
	return updates, nil
}

func recordReferences(rec *Record) map[referenceKey][]ReferenceOwner {
	refs := make(map[referenceKey][]ReferenceOwner)
	if rec == nil {
		return refs
	}
	for _, action := range rec.Actions {
		if action.Type != TypeMount || action.Name == "" {
			continue
		}
		for role, tip := range map[string]string{"desired": action.TipID, "served": action.ServedTip} {
			if tip != "" {
				key := referenceKey{action.Name, tip}
				refs[key] = append(refs[key], ReferenceOwner{rec.Name, rec.InstanceID, action.Seq, role})
			}
		}
	}
	return refs
}

func snapshotOwns(rec *Record, owner ReferenceOwner) bool {
	return rec != nil && owner.Session == rec.Name && owner.InstanceID == rec.InstanceID
}

func compareReferenceOwners(a, b ReferenceOwner) int {
	return cmp.Or(cmp.Compare(a.Session, b.Session), cmp.Compare(a.InstanceID, b.InstanceID),
		cmp.Compare(a.ActionID, b.ActionID), cmp.Compare(a.Role, b.Role))
}
