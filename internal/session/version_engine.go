package session

import (
	"errors"
	"fmt"
	"time"

	"github.com/jiangxin/tiler/internal/filelock"
)

func (e *Engine) needsVersionScope() bool {
	return e.Versioned && e.scope == nil
}

// withVersionSession copies the engine so concurrent operations never replace
// its shared Store or Lock. The inner lock is redundant only in this scope.
func (e *Engine) withVersionSession(name string, run func(*Engine) error) error {
	return e.withVersionSessionImages(name, nil, e.lockTimeout(), run)
}

func (e *Engine) withVersionSessionImages(name string, images []string, timeout time.Duration, run func(*Engine) error) error {
	c := ReferenceCoordinator{Store: e.Store, ImagesDir: e.ImagesDir, Timeout: timeout}
	return c.withSessionScope(name, images, e.versionSessionCallback(name, run), false)
}

// tryVersionSession skips contention before entering the cleanup algorithm.
func (e *Engine) tryVersionSession(name string, run func(*Engine) error) error {
	entered := false
	err := e.withVersionSessionImages(name, nil, -1, func(local *Engine) error {
		entered = true
		return run(local)
	})
	if !entered && (errors.Is(err, ErrLockHeld) || errors.Is(err, ErrLockUnavailable) ||
		errors.Is(err, filelock.ErrHeld) || errors.Is(err, filelock.ErrUnavailable)) {
		return nil
	}
	return err
}

func (e *Engine) withVersionSessionTimeout(name string, timeout time.Duration, run func(*Engine) error) error {
	return e.withVersionSessionImages(name, nil, timeout, run)
}

// WithVersionSessionLocked runs lifecycle operations under a caller-owned library
// lock. The lock must cover ImagesDir and remain held until this method returns.
// As with WithSessionLocked, callers must not already hold session locks. The
// local engine and its Store must not escape run or be shared across goroutines.
func (e *Engine) WithVersionSessionLocked(name string, run func(*Engine) error) error {
	if !e.Versioned || run == nil {
		return errors.New("session: invalid version mutation callback or unversioned engine")
	}
	c := ReferenceCoordinator{Store: e.Store, ImagesDir: e.ImagesDir, Timeout: e.lockTimeout()}
	return c.WithSessionLocked(name, e.versionSessionCallback(name, run))
}

func (e *Engine) versionSessionCallback(name string, run func(*Engine) error) func(Store) error {
	return func(store Store) error {
		local := *e
		local.Store = store
		local.Lock = func(requested string, _ time.Duration) (func(), error) {
			if requested != name {
				return nil, fmt.Errorf("session: locked scope for %s cannot lock %s", name, requested)
			}
			return func() {}, nil
		}
		return run(&local)
	}
}

// Append keeps direct engine callers on the same write-ahead reference path.
func (e *Engine) Append(name string, action Action) (Action, error) {
	if !e.needsVersionScope() {
		return e.Store.Append(name, action)
	}
	var stored Action
	err := e.withVersionSessionImages(name, []string{action.Name}, e.lockTimeout(), func(local *Engine) (err error) {
		stored, err = local.Store.Append(name, action)
		return err
	})
	return stored, err
}

// Writeback keeps direct status updates coordinated with session references.
func (e *Engine) Writeback(name string, seq int, status, mountpoint string) error {
	if !e.needsVersionScope() {
		return e.Store.Writeback(name, seq, status, mountpoint)
	}
	return e.withVersionSession(name, func(local *Engine) error {
		return local.Store.Writeback(name, seq, status, mountpoint)
	})
}

// WritebackMounted atomically updates mounted status and served references.
func (e *Engine) WritebackMounted(name string, seq int, mountpoint string, clearServed bool) error {
	if !e.needsVersionScope() {
		return e.Store.WritebackMounted(name, seq, mountpoint, clearServed)
	}
	return e.withVersionSession(name, func(local *Engine) error {
		return local.Store.WritebackMounted(name, seq, mountpoint, clearServed)
	})
}
