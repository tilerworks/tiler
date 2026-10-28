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

// Holds reports whether an active session pins the full image. It
// is the holder-name answer reduced to its bool.
func (r Refs) Holds(ref image.Ref) (bool, error) {
	_, held, err := r.Holder(ref)
	return held, err
}

// Holder names the first active session pinning the full image. Only
// library identities participate: a file-path image records no
// name or id and never protects. Actions already unmounted do not
// pin; pending and failed actions do, because a crashed execution
// may have left the mount up. A record another process holds counts
// as holding — its name is the conservative answer that never causes
// a wrong delete — and a corrupt record is reported instead of
// ignored.
func (r Refs) Holder(ref image.Ref) (string, bool, error) {
	if ref.Name == "" || ref.FullID == "" {
		return "", false, nil
	}
	store := Store(r)
	names, err := store.names()
	if err != nil {
		return "", false, err
	}
	for _, name := range names {
		held, err := r.recordHolds(store, name, ref)
		if err != nil {
			return "", false, err
		}
		if held {
			return name, true, nil
		}
	}
	return "", false, nil
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
// action of the referenced image. A rebased action names two stacks
// and protects both: the desired stack (FullID, TipID) the next
// mount serves and the served stack (ServedID, ServedTip) the live
// view still serves, so the full side matches either full id and the
// tip side matches either tip. An empty ref tip asks about the full
// image alone — a hold archive decision — and matches on the full
// side without the tip constraint.
func recordPins(rec Record, ref image.Ref) bool {
	if rec.State != StateActive {
		return false
	}
	for _, action := range rec.Actions {
		if action.Type != TypeMount || action.Status == StatusUnmounted ||
			action.Name != ref.Name {
			continue
		}
		if pinsFull(action, ref.FullID) && pinsTip(action, ref.TipID) {
			return true
		}
	}
	return false
}

// pinsFull reports whether either side of the action's stacks roots
// at the referenced full image.
func pinsFull(action Action, fullID string) bool {
	return fullID == action.FullID || fullID == action.ServedID
}

// pinsTip reports whether either side of the action's stacks tops at
// the referenced tip. An empty tip is a question about the full image
// alone and matches without consulting the tip side.
func pinsTip(action Action, tipID string) bool {
	return tipID == "" || tipID == action.TipID || tipID == action.ServedTip
}
