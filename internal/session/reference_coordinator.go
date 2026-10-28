package session

import (
	"errors"
	"os"
	"time"

	"github.com/jiangxin/tiler/internal/image"
)

// ErrReferenceConflict means the source changed after its snapshot was read.
var ErrReferenceConflict = errors.New("session: reference source changed")

// ReferenceCoordinator validates and serializes source and shard changes.
// A zero Timeout uses the normal session timeout; negative values try once.
type ReferenceCoordinator struct {
	Store
	ImagesDir string
	Timeout   time.Duration
}

// Apply compares the captured source before durably recording a save or removal.
// Before nil creates a record; After nil removes one. Pending work is recovered
// first, so a conflicting stale request cannot overwrite that recovered source.
func (c ReferenceCoordinator) Apply(before, after *Record) error {
	journal := ReferenceJournal{Version: ReferenceControlVersion, Generation: 1, Before: before, After: after}
	if err := journal.validate(); err != nil {
		return err
	}
	return c.withLibrary(func() error {
		state, _, err := c.trustedReferencesLocked(referenceTargets(before, after))
		if err != nil {
			return err
		}
		return c.withSession(referenceJournalName(journal), func() error {
			return c.applyReferenceLocked(state, before, after)
		})
	})
}

// applyReferenceLocked requires the library/session locks and trusted shards.
func (c ReferenceCoordinator) applyReferenceLocked(state ReferenceState, before, after *Record) error {
	if state.Generation == ^uint64(0) {
		return errors.New("session: reference generation exhausted")
	}
	journal := ReferenceJournal{Version: ReferenceControlVersion, Generation: state.Generation + 1, Before: before, After: after}
	if err := journal.validate(); err != nil {
		return err
	}
	current, err := c.loadReferenceSource(referenceJournalName(journal))
	if err != nil {
		return err
	}
	if !sameReferenceRecord(current, before) {
		return ErrReferenceConflict
	}
	if err := c.SaveReferenceJournal(journal); err != nil {
		return err
	}
	return c.replayReferenceJournal(state, journal, nil)
}

// Recover completes a pending journal under its own session lock. Missing state,
// damaged journals and dirty state without an intent are errors, never trust.
func (c ReferenceCoordinator) Recover() error {
	return c.withLibrary(c.recoverLocked)
}

func (c ReferenceCoordinator) recoverLocked() error {
	state, err := c.LoadReferenceState()
	if err != nil {
		return err
	}
	journal, err := c.LoadReferenceJournal()
	if errors.Is(err, os.ErrNotExist) {
		if state.Dirty {
			return errors.New("session: dirty reference state has no journal")
		}
		return nil
	}
	if err != nil {
		return err
	}
	return c.withSession(referenceJournalName(journal), func() error {
		return c.replayReferenceJournal(state, journal, nil)
	})
}

func referenceJournalName(journal ReferenceJournal) string {
	if journal.After != nil {
		return journal.After.Name
	}
	return journal.Before.Name
}

func (c ReferenceCoordinator) withLibrary(run func() error) error {
	release, err := image.LockLibrary(c.ImagesDir, c.timeout())
	if err != nil {
		return err
	}
	defer release()
	return run()
}

func (c ReferenceCoordinator) withSession(name string, run func() error) error {
	path, err := c.lockPath(name)
	if err != nil {
		return err
	}
	if _, err := c.safeStoreFile(path); err != nil {
		return err
	}
	release, err := c.lockSession(name, c.timeout())
	if err != nil {
		return err
	}
	defer release()
	return run()
}

func (c ReferenceCoordinator) timeout() time.Duration {
	if c.Timeout == 0 {
		return DefaultLockTimeout
	}
	return c.Timeout
}
