package session

import (
	"errors"
	"os"
	"slices"
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
	return c.withImageNames(referenceImageNames(before, after), func(image.Hold) error {
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
	journal, err := c.LoadReferenceJournal()
	if errors.Is(err, os.ErrNotExist) {
		return c.recoverLocked()
	}
	if err != nil {
		return err
	}
	names := referenceImageNames(journal.Before, journal.After)
	return c.withImageNames(names, func(image.Hold) error {
		again, err := c.LoadReferenceJournal()
		if errors.Is(err, os.ErrNotExist) {
			return c.recoverLocked()
		}
		if err != nil {
			return err
		}
		if !sameNameList(names, referenceImageNames(again.Before, again.After)) {
			return ErrReferenceConflict
		}
		return c.recoverLocked()
	})
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
		again, err := c.LoadReferenceJournal()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !sameReferenceJournal(journal, again) {
			return ErrReferenceConflict
		}
		return c.replayReferenceJournal(state, again, nil)
	})
}

func sameReferenceJournal(a, b ReferenceJournal) bool {
	return a.Generation == b.Generation && sameReferenceRecord(a.Before, b.Before) && sameReferenceRecord(a.After, b.After)
}

func (c ReferenceCoordinator) sessionImageNames(name string) ([]string, error) {
	rec, err := c.loadReferenceSource(name)
	if err != nil {
		return nil, err
	}
	names := referenceImageNames(rec)
	journal, err := c.LoadReferenceJournal()
	if errors.Is(err, os.ErrNotExist) {
		return names, nil
	}
	if err != nil {
		return nil, err
	}
	return mergeNames(names, referenceImageNames(journal.Before, journal.After)), nil
}

func mergeNames(groups ...[]string) []string {
	seen := make(map[string]bool)
	var names []string
	for _, group := range groups {
		for _, name := range group {
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func referenceJournalName(journal ReferenceJournal) string {
	if journal.After != nil {
		return journal.After.Name
	}
	return journal.Before.Name
}

func (c ReferenceCoordinator) withLibrary(run func() error) error {
	return c.withAllImages(func(image.Hold) error { return run() })
}

func (c ReferenceCoordinator) withAllImages(run func(image.Hold) error) error {
	if c.ImagesDir == "" {
		return run(image.Hold{})
	}
	var last error
	for range 8 {
		names, err := c.coordinatedImageNames()
		if err != nil {
			return err
		}
		hold, err := image.LockImages(c.ImagesDir, names, c.timeout())
		if err != nil {
			return err
		}
		again, err := c.coordinatedImageNames()
		if err != nil {
			hold.Release()
			return err
		}
		if !sameNameList(names, again) {
			hold.Release()
			last = errors.New("image lock: repository set changed")
			continue
		}
		err = run(hold)
		hold.Release()
		return err
	}
	if last == nil {
		last = errors.New("image lock: repository set changed")
	}
	return last
}

func (c ReferenceCoordinator) coordinatedImageNames() ([]string, error) {
	var groups [][]string
	if c.ImagesDir != "" {
		repos, err := image.ListRepos(c.ImagesDir)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(repos))
		for _, repo := range repos {
			names = append(names, repo.Name())
		}
		groups = append(groups, names)
	}
	entries, err := os.ReadDir(c.Dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if ValidateName(entry.Name()) != nil {
			continue
		}
		rec, err := c.LoadVersion(entry.Name())
		if err != nil {
			continue
		}
		groups = append(groups, referenceImageNames(&rec))
	}
	return mergeNames(groups...), nil
}

func (c ReferenceCoordinator) withImageNames(names []string, run func(image.Hold) error) error {
	if c.ImagesDir == "" {
		return run(image.Hold{})
	}
	hold, err := image.LockImages(c.ImagesDir, names, c.timeout())
	if err != nil {
		return err
	}
	defer hold.Release()
	return run(hold)
}

func sameNameList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
