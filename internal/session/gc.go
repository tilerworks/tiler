package session

import (
	"errors"
	"time"
)

// Cleanup actions a command can report.
const (
	// CleanupRemoved is an aged closed record deleted.
	CleanupRemoved = "removed"
	// CleanupStale is an active record without a healthy mount,
	// reported for attention and never auto-deleted.
	CleanupStale = "stale"
	// CleanupClosed is an aged stale active record the autoclose
	// toggle flipped to closed, which makes it eligible for a later
	// age-based removal.
	CleanupClosed = "closed"
)

// CleanupEvent is one record hygiene action.
type CleanupEvent struct {
	// Session is the record's name.
	Session string
	// Action is removed, stale, or closed.
	Action string
}

// Cleanup walks the records for one hygiene pass, the session half
// of the garbage collection entry. A closed record whose completion
// time passed the age limit is deleted under the non-blocking lock
// and reported removed; a busy record is skipped this round. An
// active record is generally never auto-deleted: one without a
// healthy mount is reported stale, and with the autoclose rule
// enabled an aged one is flipped to closed instead, entering the
// removal scope of a later pass. The exception is a --rm record: one
// whose views are all dead is torn down, its runtime purged, and its
// record deleted whatever its age, reported removed. A zero max age
// disables every age rule but not the --rm purge.
func (e *Engine) Cleanup(
	maxAge time.Duration,
	autocloseStale bool,
	now time.Time,
	umount UmountFunc,
) ([]CleanupEvent, error) {
	names, err := e.names()
	if err != nil {
		return nil, err
	}
	health, err := e.healthFunc()
	if err != nil {
		return nil, err
	}
	var events []CleanupEvent
	for _, name := range names {
		event, err := e.cleanupOne(name, maxAge, autocloseStale, now, health, umount)
		if err != nil {
			return events, err
		}
		if event != nil {
			events = append(events, *event)
		}
	}
	return events, nil
}

// cleanupOne classifies one record and applies its rule.
func (e *Engine) cleanupOne(
	name string,
	maxAge time.Duration,
	autocloseStale bool,
	now time.Time,
	health HealthFunc,
	umount UmountFunc,
) (*CleanupEvent, error) {
	release, ok := e.tryReadLock(name)
	if !ok {
		return nil, nil
	}
	rec, err := e.Load(name)
	release()
	if err != nil {
		return nil, err
	}
	if rec.State == StateClosed {
		if !closedExpired(rec, maxAge, now) {
			return nil, nil
		}
		return e.removeExpired(name, maxAge, now)
	}
	if hasHealthyMount(rec, health) {
		return nil, nil
	}
	if rec.RemoveOnClose {
		return e.purgeStaleRemovable(name, health, umount)
	}
	if autocloseStale && maxAge > 0 && now.Sub(rec.CreatedAt) > maxAge {
		return e.closeStale(name, now, health)
	}
	if hasRecordedMounts(rec) {
		return &CleanupEvent{Session: name, Action: CleanupStale}, nil
	}
	return nil, nil
}

// removeExpired deletes one aged closed record under the
// non-blocking lock, re-checking the rule under the lock. A busy
// record waits for a later round.
func (e *Engine) removeExpired(name string, maxAge time.Duration, now time.Time) (*CleanupEvent, error) {
	release, err := e.lockFor(name, 0)
	if err != nil {
		if errors.Is(err, ErrLockHeld) || errors.Is(err, ErrLockUnavailable) {
			return nil, nil
		}
		return nil, err
	}
	defer release()
	rec, err := e.Load(name)
	if errors.Is(err, ErrNotFound) {
		// A concurrent pass already removed the record.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !closedExpired(rec, maxAge, now) {
		return nil, nil
	}
	if err := e.deleteRecordFiles(name); err != nil {
		return nil, err
	}
	return &CleanupEvent{Session: name, Action: CleanupRemoved}, nil
}

// closeStale flips one aged active record without a healthy mount
// to closed under the non-blocking lock, re-checking the rule under
// the lock so a concurrent recovery keeps the session open.
func (e *Engine) closeStale(name string, now time.Time, health HealthFunc) (*CleanupEvent, error) {
	release, err := e.lockFor(name, 0)
	if err != nil {
		if errors.Is(err, ErrLockHeld) || errors.Is(err, ErrLockUnavailable) {
			return nil, nil
		}
		return nil, err
	}
	defer release()
	rec, err := e.Load(name)
	if errors.Is(err, ErrNotFound) {
		// A concurrent remove deleted the record.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rec.State != StateActive || hasHealthyMount(rec, health) {
		return nil, nil
	}
	rec.State = StateClosed
	rec.ClosedAt = now
	if err := e.Save(rec); err != nil {
		return nil, err
	}
	return &CleanupEvent{Session: name, Action: CleanupClosed}, nil
}

// purgeStaleRemovable tears down and deletes one active --rm record
// whose views are all dead, the gc tail of the --rm opt-in: the
// session was marked to delete itself on close, and a view that died
// without a clean close is purged here instead of lingering and
// pinning its image. It takes the non-blocking exclusive lock and
// re-checks the rule under it, so a concurrent recovery that revived
// a view keeps the session open; a busy or unavailable lock skips
// this round. The teardown reuses closeLocked, so the full umount
// cascade brings down any dead transit point before the runtime
// purge, and the record deletion is what unpins the image.
func (e *Engine) purgeStaleRemovable(
	name string,
	health HealthFunc,
	umount UmountFunc,
) (*CleanupEvent, error) {
	release, err := e.lockFor(name, 0)
	if err != nil {
		if errors.Is(err, ErrLockHeld) || errors.Is(err, ErrLockUnavailable) {
			return nil, nil
		}
		return nil, err
	}
	defer release()
	rec, err := e.Load(name)
	if errors.Is(err, ErrNotFound) {
		// A concurrent pass already removed the record.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rec.State != StateActive || !rec.RemoveOnClose ||
		hasHealthyMount(rec, health) {
		return nil, nil
	}
	if _, err := e.closeLocked(name, umount); err != nil {
		return nil, err
	}
	return &CleanupEvent{Session: name, Action: CleanupRemoved}, nil
}

// closedExpired reports whether a closed record passed the age
// limit. A zero max age disables age deletion, and a record whose
// completion time was never stamped is never deleted.
func closedExpired(rec Record, maxAge time.Duration, now time.Time) bool {
	if rec.State != StateClosed || maxAge <= 0 || rec.ClosedAt.IsZero() {
		return false
	}
	return now.Sub(rec.ClosedAt) > maxAge
}

// hasHealthyMount reports whether one recorded mount still serves.
func hasHealthyMount(rec Record, health HealthFunc) bool {
	for _, action := range rec.Actions {
		if action.Type == TypeMount && action.Status != StatusUnmounted &&
			action.Mountpoint != "" && health(action.Mountpoint) {
			return true
		}
	}
	return false
}

// hasRecordedMounts reports whether the record still carries mount
// actions a recovery could replay.
func hasRecordedMounts(rec Record) bool {
	for _, action := range rec.Actions {
		if action.Type == TypeMount && action.Status != StatusUnmounted {
			return true
		}
	}
	return false
}
