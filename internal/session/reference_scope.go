package session

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"

	"github.com/jiangxin/tiler/internal/image"
)

// ErrReferenceScopeClosed means a scoped store escaped its held-lock callback.
var ErrReferenceScopeClosed = errors.New("session: reference mutation scope is closed")

// WithSession holds library then session locks throughout run. Its local Store
// reuses the regular mutation algorithms, committing every save independently.
// Run must not call locking coordinator APIs or share compound Store operations
// across goroutines. Retained stores are disabled before the locks are released.
func (c ReferenceCoordinator) WithSession(name string, run func(Store) error) error {
	return c.withSessionScope(name, nil, run, false)
}

// WithSessionLocked reuses a library lock owned by the caller. The caller must
// hold it for ImagesDir throughout this call, without already holding session
// locks. This method acquires and releases session locks as needed; it never
// releases the library lock. Store lifetime and callback rules match WithSession.
func (c ReferenceCoordinator) WithSessionLocked(name string, run func(Store) error) error {
	return c.withSessionScope(name, nil, run, true)
}

func (c ReferenceCoordinator) withSessionScope(name string, images []string, run func(Store) error, libraryHeld bool) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if run == nil || c.scope != nil {
		return errors.New("session: invalid reference mutation callback or nested scope")
	}
	runLocked := func() error {
		if _, err := c.ensureInventoryLocked(false, nil); err != nil {
			return err
		}
		before, err := c.loadReferenceSource(name)
		if err != nil {
			return err
		}
		state, _, err := c.trustedReferencesLocked(referenceTargets(before))
		if err != nil {
			return err
		}
		return c.withSession(name, func() (err error) {
			current, err := c.loadReferenceSource(name)
			if err != nil {
				return err
			}
			if !sameReferenceRecord(current, before) {
				return ErrReferenceConflict
			}
			scope := &referenceSessionScope{coordinator: c, name: name, state: state, current: cloneReferenceRecord(current)}
			defer func() { err = errors.Join(err, scope.close()) }()
			return run(Store{Dir: c.Dir, LogsDir: c.LogsDir, scope: scope})
		})
	}
	if libraryHeld {
		return runLocked()
	}
	names, err := c.sessionImageNames(name)
	if err != nil {
		return err
	}
	names = mergeNames(names, images)
	return c.withImageNames(names, func(image.Hold) error {
		if c.ImagesDir != "" {
			if err := image.PrepareImagesLocked(c.ImagesDir, names); err != nil {
				return err
			}
		}
		return runLocked()
	})
}

type referenceSessionScope struct {
	mu          sync.Mutex
	coordinator ReferenceCoordinator
	name        string
	state       ReferenceState
	current     *Record
	closed      bool
	failure     error
}

func (scope *referenceSessionScope) check(name string) error {
	if scope.closed {
		return ErrReferenceScopeClosed
	}
	if scope.failure != nil {
		return scope.failure
	}
	if name != scope.name {
		return fmt.Errorf("session: reference scope for %s cannot access %s", scope.name, name)
	}
	return nil
}

func (scope *referenceSessionScope) load(name string) (Record, error) {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if err := scope.check(name); err != nil {
		return Record{}, err
	}
	if scope.current == nil {
		return Record{}, fmt.Errorf("session: %w: %s", ErrNotFound, name)
	}
	return *cloneReferenceRecord(scope.current), nil
}

func (scope *referenceSessionScope) save(rec Record) error {
	return scope.mutate(rec.Name, &rec)
}

func (scope *referenceSessionScope) remove(name string) error {
	return scope.mutate(name, nil)
}

func (scope *referenceSessionScope) mutate(name string, after *Record) (err error) {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	defer func() {
		if !scope.closed {
			scope.failure = err
		}
	}()
	if err := scope.check(name); err != nil {
		return err
	}
	if after == nil && scope.current == nil {
		return ErrNotFound
	}
	if after != nil {
		if err := after.ValidateVersion(); err != nil {
			return err
		}
		if scope.current != nil && after.InstanceID != scope.current.InstanceID {
			return ErrReferenceConflict
		}
	}
	after = cloneReferenceRecord(after)
	c := scope.coordinator
	// Rebuilding here would take this same session lock recursively. Damaged
	// newly requested shards fail closed; repair can run after the scope exits.
	if _, err := c.readTrustedReferenceBuckets(scope.state, referenceTargets(scope.current, after)); err != nil {
		return err
	}
	if err := c.applyReferenceLocked(scope.state, scope.current, after); err != nil {
		return err
	}
	scope.current = after
	// Retirement of the last session removes the reference store, including
	// state.json. The committed generation is still the one this apply wrote.
	generation := scope.state.Generation + 1
	loaded, err := c.LoadReferenceState()
	if errors.Is(err, os.ErrNotExist) {
		loaded = ReferenceState{
			Version: ReferenceControlVersion, Generation: generation, CatalogComplete: true,
			Inventory: map[string]ReferenceRevision{}, ShardDigests: map[string]string{},
		}
		err = nil
	}
	if err != nil {
		return err
	}
	scope.state = loaded
	if after == nil {
		return c.deleteRecordFiles(name)
	}
	return nil
}

func (scope *referenceSessionScope) close() error {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	scope.closed = true
	return scope.failure
}

func cloneReferenceRecord(rec *Record) *Record {
	if rec == nil {
		return nil
	}
	cloned := *rec
	cloned.Actions = slices.Clone(rec.Actions)
	return &cloned
}
