package session

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

// DefaultLockTimeout bounds how long one lifecycle operation waits
// for a session's exclusive lock before failing with a runtime
// error instead of hanging.
const DefaultLockTimeout = 30 * time.Second

// MountFunc executes one recorded mount action and reports the
// serving view. The command layer injects the real workspace engine;
// unit tests inject fakes.
type MountFunc func(Action) (Result, error)

// Result is what one executed mount reports back.
type Result struct {
	// Mountpoint is the directory serving the view.
	Mountpoint string
	// Upper is the writable upper directory, empty for a read-only
	// view.
	Upper string
	// Reused reports that an existing healthy view was adopted and
	// nothing new was created.
	Reused bool
}

// UmountFunc tears the view at one mountpoint down. The cascade
// inside one view is the engine's business; the call is idempotent,
// so an unmounted point succeeds silently.
type UmountFunc func(mountpoint string) error

// HealthFunc reports whether a mountpoint still serves a healthy
// view.
type HealthFunc func(mountpoint string) bool

// CleanFunc tears one stale point down before a rebuild.
type CleanFunc func(mountpoint string) error

// Engine drives the session lifecycle over a record Store: create,
// write-ahead mount, recovery replay, reverse-order teardown, and
// record removal. Mount, umount, health, and cleanup run through
// injected seams, so the package never hard-depends on the workspace
// engine and unit tests inject fakes. Every operation serializes on
// the session's exclusive lock: one session is only ever mutated by
// one process at a time, while different sessions stay parallel.
type Engine struct {
	// Store is the record store the lifecycle reads and writes.
	Store
	// Now returns the current time. Nil uses the UTC wall clock.
	Now func() time.Time
	// LoadTable snapshots the host mount table for the health
	// check. Nil leaves the table condition to the stat probe.
	LoadTable func() ([]mountinfo.Mount, error)
	// Health overrides the shared two-condition check. Nil uses
	// Healthy against one table snapshot.
	Health HealthFunc
	// Clean overrides the stale-point teardown. Nil uses CleanStale.
	Clean CleanFunc
	// RuntimeDir is the configured runtime tree. A path inside it —
	// an upper, a work directory, or a lower transit point —
	// resolves back to the session owning the combination. Empty
	// disables the runtime branch of the reverse lookup.
	RuntimeDir string
	// LockTimeout bounds the exclusive-lock wait. Zero uses
	// DefaultLockTimeout.
	LockTimeout time.Duration

	// lock overrides the advisory lock for in-package tests on
	// platforms without flock. Production callers always take the
	// real lock.
	lock func(name string, timeout time.Duration) (func(), error)
}

// New creates one fresh active record under the session's exclusive
// lock. A name already recorded — an explicit session or an implicit
// auto-* one — is refused.
func (e *Engine) New(name string) error {
	release, err := e.lockExclusive(name)
	if err != nil {
		return err
	}
	defer release()
	_, err = e.Load(name)
	switch {
	case err == nil:
		return fmt.Errorf("session: %s already exists", name)
	case !errors.Is(err, ErrNotFound):
		return err
	}
	return e.Save(Record{Name: name, State: StateActive, CreatedAt: e.now()})
}

// RunMount records one mount action write-ahead and executes it
// under the session's exclusive lock: the pending action is durable
// before the mount runs, and the outcome is written back after. A
// failed execution keeps the write-ahead record with a failed
// status, so a later Open replays it, and returns the execution
// error.
func (e *Engine) RunMount(name string, action Action, mount MountFunc) (Result, error) {
	release, err := e.lockExclusive(name)
	if err != nil {
		return Result{}, err
	}
	defer release()
	action.Type = TypeMount
	if action.Timestamp.IsZero() {
		action.Timestamp = e.now()
	}
	stored, err := e.Append(name, action)
	if err != nil {
		return Result{}, err
	}
	result, err := mount(stored)
	if err != nil {
		if wbErr := e.Writeback(name, stored.Seq, StatusFailed, ""); wbErr != nil {
			return Result{}, fmt.Errorf("%w (writeback: %v)", err, wbErr)
		}
		return Result{}, err
	}
	if wbErr := e.Writeback(name, stored.Seq, StatusMounted, result.Mountpoint); wbErr != nil {
		return result, wbErr
	}
	return result, nil
}

// Open replays one active record into a consistent mounted state,
// the crash-recovery entry point. Mount actions walk in record
// order: a healthy view is skipped, an unhealthy one is cleaned
// (fusermount3 -u, escalating to -uz) and rebuilt through the mount
// seam, and the lower transit points of a view recover with the
// main mount. Repeated opens skip every healthy mount, so recovery
// is idempotent. Umount actions record teardowns that already
// happened and restore nothing; a mount already torn down stays
// down.
func (e *Engine) Open(name string, mount MountFunc) error {
	release, err := e.lockExclusive(name)
	if err != nil {
		return err
	}
	defer release()
	rec, err := e.Load(name)
	if err != nil {
		return err
	}
	if rec.State != StateActive {
		return fmt.Errorf("session: %s is %s; only an active session opens",
			name, rec.State)
	}
	health, err := e.healthFunc()
	if err != nil {
		return err
	}
	clean := e.cleanFunc()
	for _, action := range rec.Actions {
		if action.Type != TypeMount || action.Status == StatusUnmounted {
			continue
		}
		if action.Mountpoint != "" && health(action.Mountpoint) {
			// Reconcile the record with the healthy reality: an
			// action left pending or failed by a crash between the
			// execution and its writeback becomes mounted without a
			// remount, keeping a repeated open a pure no-op.
			if action.Status != StatusMounted {
				if err := e.Writeback(name, action.Seq, StatusMounted, ""); err != nil {
					return err
				}
			}
			continue
		}
		if err := e.replay(name, action, clean, mount); err != nil {
			return err
		}
	}
	return nil
}

// replay cleans one stale action and rebuilds it through the mount
// seam, writing the outcome back to the record.
func (e *Engine) replay(name string, action Action, clean CleanFunc, mount MountFunc) error {
	if action.Mountpoint != "" {
		if err := clean(action.Mountpoint); err != nil {
			return err
		}
	}
	result, err := mount(action)
	if err != nil {
		if wbErr := e.Writeback(name, action.Seq, StatusFailed, ""); wbErr != nil {
			return fmt.Errorf("%w (writeback: %v)", err, wbErr)
		}
		return fmt.Errorf("session: %s: replay seq %d: %w", name, action.Seq, err)
	}
	if err := e.Writeback(name, action.Seq, StatusMounted, result.Mountpoint); err != nil {
		return err
	}
	return nil
}

// Close tears every recorded mount down in reverse record order and
// marks the record closed, keeping it for audit. The cascade inside
// one view is the umount seam's business. An action that never
// reached its writeback — pending after a crash — is torn down too;
// the idempotent umount makes that safe. Closing a closed session
// is a no-op.
func (e *Engine) Close(name string, umount UmountFunc) error {
	release, err := e.lockExclusive(name)
	if err != nil {
		return err
	}
	defer release()
	rec, err := e.Load(name)
	if err != nil {
		return err
	}
	if rec.State == StateClosed {
		return nil
	}
	for i := len(rec.Actions) - 1; i >= 0; i-- {
		action := rec.Actions[i]
		if action.Type != TypeMount || action.Mountpoint == "" ||
			action.Status == StatusUnmounted {
			continue
		}
		if err := umount(action.Mountpoint); err != nil {
			return fmt.Errorf("session: %s: close seq %d: %w",
				name, action.Seq, err)
		}
		if err := e.Writeback(name, action.Seq, StatusUnmounted, ""); err != nil {
			return err
		}
	}
	closed, err := e.Load(name)
	if err != nil {
		return err
	}
	closed.State = StateClosed
	closed.ClosedAt = e.now()
	return e.Save(closed)
}

// Remove deletes one closed record and its lock file. An active
// record is refused: close it first, so a session holding mounts is
// never orphaned.
func (e *Engine) Remove(name string) error {
	release, err := e.lockExclusive(name)
	if err != nil {
		return err
	}
	defer release()
	rec, err := e.Load(name)
	if err != nil {
		return err
	}
	if rec.State != StateClosed {
		return fmt.Errorf("session: %s is %s; close it before removing it",
			name, rec.State)
	}
	recordPath, err := e.recordPath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(recordPath); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	lockPath, err := e.lockPath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("session: %w", err)
	}
	return nil
}

// healthFunc resolves the health seam over one fresh table
// snapshot.
func (e *Engine) healthFunc() (HealthFunc, error) {
	table, err := e.loadTable()
	if err != nil {
		return nil, err
	}
	return e.healthFor(table), nil
}

// loadTable snapshots the host mount table through the seam. A
// missing seam leaves the table conditions to the stat probes.
func (e *Engine) loadTable() ([]mountinfo.Mount, error) {
	if e.LoadTable == nil {
		return nil, nil
	}
	table, err := e.LoadTable()
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	return table, nil
}

// healthFor builds the health check over one table snapshot: the
// injected seam, or the shared two-condition check.
func (e *Engine) healthFor(table []mountinfo.Mount) HealthFunc {
	if e.Health != nil {
		return e.Health
	}
	return func(point string) bool {
		ok, err := Healthy(table, point)
		return err == nil && ok
	}
}

// cleanFunc resolves the cleanup seam.
func (e *Engine) cleanFunc() CleanFunc {
	if e.Clean != nil {
		return e.Clean
	}
	return CleanStale
}

// lockExclusive takes the session's exclusive lock through the test
// seam, defaulting to the real advisory flock.
func (e *Engine) lockExclusive(name string) (func(), error) {
	if e.lock != nil {
		return e.lock(name, e.lockTimeout())
	}
	return e.lockSession(name, e.lockTimeout())
}

// lockTimeout is the configured lock wait, or the default.
func (e *Engine) lockTimeout() time.Duration {
	if e.LockTimeout > 0 {
		return e.LockTimeout
	}
	return DefaultLockTimeout
}

// now is the injected clock, defaulting to the UTC wall clock.
func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now().UTC()
}

// List returns every record for command rendering, ordered by file
// name. A corrupt record is reported, never silently dropped.
func (e *Engine) List() ([]Record, error) {
	return e.Store.List()
}

// Show returns one record for command rendering.
func (e *Engine) Show(name string) (Record, error) {
	return e.Load(name)
}

// Summary is the one-line read model of one session for the list
// command.
type Summary struct {
	// Name is the session name.
	Name string
	// State is active or closed.
	State string
	// Mounts counts the currently mounted actions.
	Mounts int
	// Created is when the record was written first.
	Created time.Time
	// Closed is when the record reached the closed state.
	Closed time.Time
}

// Summarize reduces one record to its list line.
func Summarize(rec Record) Summary {
	mounts := 0
	for _, action := range rec.Actions {
		if action.Type == TypeMount && action.Status == StatusMounted {
			mounts++
		}
	}
	return Summary{
		Name:    rec.Name,
		State:   rec.State,
		Mounts:  mounts,
		Created: rec.CreatedAt,
		Closed:  rec.ClosedAt,
	}
}

// Summaries reduces records to list lines.
func Summaries(records []Record) []Summary {
	summaries := make([]Summary, 0, len(records))
	for _, rec := range records {
		summaries = append(summaries, Summarize(rec))
	}
	return summaries
}
