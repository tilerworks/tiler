package session

import (
	"errors"

	"github.com/jiangxin/tiler/internal/image"
)

// Refs implements the image reference protection over the session
// records: a library image stays pinned while an active session
// records a live mount of it, so garbage collection and removal
// never take an image a workspace still serves.
type Refs struct {
	// Dir is the sessions directory holding the records.
	Dir string
}

var _ image.Refs = Refs{}

// Holds reports whether an active session pins the full image. Only
// library identities participate: a file-path image records no
// owner, repository, or id and never protects. Actions already
// unmounted do not pin; pending and failed actions do, because a
// crashed execution may have left the mount up. A record another
// process holds counts as holding — the conservative side that
// never causes a wrong delete — and a corrupt record is reported
// instead of ignored.
func (r Refs) Holds(ref image.Ref) (bool, error) {
	if ref.Owner == "" || ref.Repo == "" || ref.FullID == "" {
		return false, nil
	}
	store := Store(r)
	names, err := store.names()
	if err != nil {
		return false, err
	}
	for _, name := range names {
		held, err := r.recordHolds(store, name, ref)
		if err != nil || held {
			return held, err
		}
	}
	return false, nil
}

// recordHolds checks one record. A busy lock answers held without
// reading; a platform without advisory locking reads unlocked; a
// record removed after the listing does not hold.
func (r Refs) recordHolds(store Store, name string, ref image.Ref) (bool, error) {
	release, err := store.tryLockSession(name)
	switch {
	case err == nil:
	case errors.Is(err, ErrLockHeld):
		return true, nil
	case errors.Is(err, ErrLockUnavailable):
	default:
		return false, err
	}
	rec, err := store.Load(name)
	if release != nil {
		release()
	}
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return recordPins(rec, ref), nil
}

// recordPins reports whether one active record carries a live mount
// action of the referenced image.
func recordPins(rec Record, ref image.Ref) bool {
	if rec.State != StateActive {
		return false
	}
	for _, action := range rec.Actions {
		if action.Type == TypeMount && action.Status != StatusUnmounted &&
			action.Owner == ref.Owner && action.Repo == ref.Repo &&
			action.FullID == ref.FullID {
			return true
		}
	}
	return false
}
