package session

import (
	"fmt"
	"time"
)

func (e *Engine) needsVersionScope() bool {
	return e.Versioned && e.scope == nil
}

// withVersionSession copies the engine so concurrent operations never replace
// its shared Store or Lock. The inner lock is redundant only in this scope.
func (e *Engine) withVersionSession(name string, run func(*Engine) error) error {
	c := ReferenceCoordinator{Store: e.Store, ImagesDir: e.ImagesDir, Timeout: e.lockTimeout()}
	return c.WithSession(name, func(store Store) error {
		local := *e
		local.Store = store
		local.Lock = func(requested string, _ time.Duration) (func(), error) {
			if requested != name {
				return nil, fmt.Errorf("session: locked scope for %s cannot lock %s", name, requested)
			}
			return func() {}, nil
		}
		return run(&local)
	})
}

// Append keeps direct engine callers on the same write-ahead reference path.
func (e *Engine) Append(name string, action Action) (Action, error) {
	if !e.needsVersionScope() {
		return e.Store.Append(name, action)
	}
	var stored Action
	err := e.withVersionSession(name, func(local *Engine) (err error) {
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
