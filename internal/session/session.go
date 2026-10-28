package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	// Versioned routes core lifecycle writes through immutable-version references.
	// Runtime callers opt in when the new image and session layouts are activated.
	Versioned bool
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
	// RemoveRuntime purges the runtime directory of one mountpoint:
	// the sessions/<name>/runtime/m<seq> combination the mount
	// engine built for it, upper and work data included. Nil derives
	// the directory from the session name and the action's RuntimeID
	// and removes it wholesale; a mount with no RuntimeID is a
	// no-op. The --rm close path and the gc purge of a stale --rm
	// record share it, and tests inject a fake to observe the calls.
	RemoveRuntime func(mountpoint string) error
	// SessionsDir is the configured sessions root. A path under
	// <SessionsDir>/<name>/runtime/... — an upper, a work directory,
	// or a lower transit point — resolves back to the session owning
	// the combination. Empty disables the runtime branch of the
	// reverse lookup. It is the same tree Store.Dir points at.
	SessionsDir string
	// ImagesDir is the image library root. Disk-image reconciliation
	// matches a library record to images/<owner>/<repo>/image.dmg
	// and treats an attached image under this tree as a tiler
	// volume. Empty disables that half of orphan detection. The
	// Linux mount-table path does not read it.
	ImagesDir string
	// CacheDir is the content-addressed cache. When CacheEnabled is
	// set, hdiutil attach opens <CacheDir>/<sha256>.sqsh instead of
	// the library payload, and reconciliation accepts that copy.
	CacheDir string
	// CacheEnabled reports that attach copied the payload into
	// CacheDir. A library mount with this off still matches
	// image.dmg. A file-path mount still matches the absolute path
	// attach opened.
	CacheEnabled bool
	// LoadDiskImages snapshots hdiutil info. Nil keeps the
	// mount-table reconciliation Linux uses. When it is set and
	// Health is not overridden, status and session open reconcile
	// through this snapshot instead of the host mount table.
	LoadDiskImages func() ([]DiskImage, error)
	// LockTimeout bounds the exclusive-lock wait. Zero uses
	// DefaultLockTimeout.
	LockTimeout time.Duration
	// Lock replaces the advisory session locking, mirroring the
	// mount engine's Run seam. Nil takes the real flock: a positive
	// timeout polls until the deadline, a zero timeout attempts
	// once and reports ErrLockHeld when busy. Production callers
	// leave it nil; tests inject it where flock is unavailable or
	// not the subject.
	Lock func(name string, timeout time.Duration) (func(), error)
}

// New creates one fresh active record under the session's exclusive
// lock. A name already recorded is refused.
func (e *Engine) New(name string) error {
	return e.newRecord(name, false)
}

// NewRemovable creates one fresh active record marked for removal on
// close: a --rm session deletes itself and purges its runtime when it
// closes instead of staying closed for audit. It shares New's
// exclusive lock and duplicate refusal.
func (e *Engine) NewRemovable(name string) error {
	return e.newRecord(name, true)
}

// newRecord creates one fresh active record carrying the --rm marker
// under the session's exclusive lock. A name already recorded is
// refused.
func (e *Engine) newRecord(name string, removeOnClose bool) error {
	if e.needsVersionScope() {
		return e.withVersionSession(name, func(local *Engine) error { return local.newRecord(name, removeOnClose) })
	}
	release, err := e.lockExclusive(name)
	if err != nil {
		return err
	}
	defer release()
	_, err = e.Load(name)
	switch {
	case err == nil:
		return fmt.Errorf("session: %s: %w", name, ErrExists)
	case !errors.Is(err, ErrNotFound):
		return err
	}
	rec := Record{
		Name:          name,
		State:         StateActive,
		CreatedAt:     e.now(),
		RemoveOnClose: removeOnClose,
	}
	if e.Versioned {
		rec, err = NewVersionRecord(name, rec.CreatedAt)
		if err != nil {
			return err
		}
		rec.RemoveOnClose = removeOnClose
	}
	return e.Save(rec)
}

// RunMount records one mount action write-ahead and executes it
// under the session's exclusive lock: the pending action is durable
// before the mount runs, and the outcome is written back after. A
// failed execution keeps the write-ahead record with a failed
// status, so a later Open replays it, and returns the execution
// error.
func (e *Engine) RunMount(name string, action Action, mount MountFunc) (Result, error) {
	if e.needsVersionScope() {
		var result Result
		err := e.withVersionSession(name, func(local *Engine) (err error) {
			result, err = local.RunMount(name, action, mount)
			return err
		})
		return result, err
	}
	release, err := e.lockExclusive(name)
	if err != nil {
		return Result{}, err
	}
	defer release()
	rec, err := e.Load(name)
	if err != nil {
		return Result{}, err
	}
	if rec.State != StateActive {
		return Result{}, fmt.Errorf(
			"session: %s is %s; create a new session to mount into it",
			name, rec.State)
	}
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

// Open replays one record into a consistent mounted state: the
// crash-recovery entry point and the way a closed session resumes.
// Mount actions walk in record order and reality decides: a healthy
// view is skipped, an unhealthy one — never mounted, dead with its
// daemon, or torn down by an earlier close — is cleaned
// (fusermount3 -u, escalating to -uz) and rebuilt through the mount
// seam, and the lower transit points of a view recover with the
// main mount. A closed record reopens: after a successful replay
// its state is active again. Repeated opens skip every healthy
// mount, so recovery is idempotent. Umount actions record teardowns
// that already happened and restore nothing.
func (e *Engine) Open(name string, mount MountFunc) error {
	if e.needsVersionScope() {
		return e.withVersionSession(name, func(local *Engine) error { return local.Open(name, mount) })
	}
	release, err := e.lockExclusive(name)
	if err != nil {
		return err
	}
	defer release()
	rec, err := e.Load(name)
	if err != nil {
		return err
	}
	if e.useDiskImages() {
		return e.openFromDisk(name, rec, mount)
	}
	health, err := e.healthFunc()
	if err != nil {
		return err
	}
	clean := e.cleanFunc()
	for _, action := range rec.Actions {
		if action.Type != TypeMount {
			continue
		}
		if action.Mountpoint != "" && health(action.Mountpoint) {
			// Reconcile the record with the healthy reality: an
			// action left pending or failed by a crash between the
			// execution and its writeback becomes mounted without a
			// remount, keeping a repeated open a pure no-op. A
			// served-carrying action only reaches this branch after
			// its replay cleaned the stale point and the remount
			// actually succeeded, so the reconcile empties the
			// replaced served stack ids in the same writeback
			// instead of stranding them beside status=mounted. An
			// already-mounted action is left untouched: a healthy
			// mounted view carrying served ids still serves the
			// replaced stack.
			if action.Status != StatusMounted {
				if err := e.WritebackMounted(name, action.Seq, "", true); err != nil {
					return err
				}
			}
			continue
		}
		if err := e.replay(name, action, clean, mount); err != nil {
			return err
		}
	}
	if rec.State == StateClosed {
		return e.reopen(name)
	}
	return nil
}

// reopen flips one replayed closed record back to active: its views
// are up again, so the closed state no longer describes reality.
func (e *Engine) reopen(name string) error {
	fresh, err := e.Load(name)
	if err != nil {
		return err
	}
	fresh.State = StateActive
	fresh.ClosedAt = time.Time{}
	return e.Save(fresh)
}

// replay cleans one stale action and rebuilds it through the mount
// seam, writing the outcome back to the record. A real remount —
// not the adoption of an already-healthy view — serves the desired
// stack again, so the mounted status and the emptied served stack
// ids land in the same atomic write and the old stack stops being
// pinned through them. A second write for the clear would leave a
// crash in between strand status=mounted beside stale served ids
// forever: a healthy mounted action carrying served ids is exactly
// the post-rebase steady state, so no later open could tell the two
// apart and heal it.
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
	// An adopted (Reused) view still serves the stack its served
	// ids name, so only a real remount clears them.
	return e.WritebackMounted(name, action.Seq, result.Mountpoint, !result.Reused)
}

// Close tears every recorded mount down in reverse record order. A
// plain record is then marked closed and kept for audit; a --rm
// record is deleted instead and the runtime directory of every view
// is purged, upper data included. The cascade inside one view is the
// umount seam's business. An action that never reached its writeback
// — pending after a crash — is torn down too; the idempotent umount
// makes that safe. Closing a closed session is a no-op. The return
// reports whether the record was removed, which is how the command
// layer tells a --rm deletion from an audit-kept close.
func (e *Engine) Close(name string, umount UmountFunc) (bool, error) {
	if e.needsVersionScope() {
		var removed bool
		err := e.withVersionSession(name, func(local *Engine) (err error) {
			removed, err = local.Close(name, umount)
			return err
		})
		return removed, err
	}
	release, err := e.lockExclusive(name)
	if err != nil {
		return false, err
	}
	defer release()
	return e.closeLocked(name, umount)
}

// closeLocked performs the reverse-order teardown of Close and then
// either flips the record to closed or, for a --rm record, purges
// its runtime and deletes it. The caller holds the session's
// exclusive lock. The return reports whether the record was removed.
func (e *Engine) closeLocked(name string, umount UmountFunc) (bool, error) {
	rec, err := e.Load(name)
	if err != nil {
		return false, err
	}
	if rec.State == StateClosed {
		return false, nil
	}
	for i := len(rec.Actions) - 1; i >= 0; i-- {
		action := rec.Actions[i]
		if action.Type != TypeMount || action.Mountpoint == "" ||
			action.Status == StatusUnmounted {
			continue
		}
		if err := umount(action.Mountpoint); err != nil {
			return false, fmt.Errorf("session: %s: close seq %d: %w",
				name, action.Seq, err)
		}
		if err := e.Writeback(name, action.Seq, StatusUnmounted, ""); err != nil {
			return false, err
		}
	}
	if rec.RemoveOnClose {
		return true, e.purgeRemovable(name, rec)
	}
	closed, err := e.Load(name)
	if err != nil {
		return false, err
	}
	closed.State = StateClosed
	closed.ClosedAt = e.now()
	return false, e.Save(closed)
}

// purgeRemovable removes the runtime directory of every recorded
// view and then deletes the session directory. The views are already
// down, so each runtime directory is safe to remove wholesale. When
// no RemoveRuntime seam is injected, deleting the session directory
// clears the record, lock, and every runtime subtree in one step. A
// purge failure is collected and reported but never blocks the
// record deletion: dropping the record is what unpins the image, the
// primary goal of a --rm close.
func (e *Engine) purgeRemovable(name string, rec Record) error {
	var errs []error
	for _, action := range rec.Actions {
		if action.Type != TypeMount || action.Mountpoint == "" {
			continue
		}
		if err := e.removeRuntime(name, action); err != nil {
			errs = append(errs, fmt.Errorf(
				"session: %s: purge runtime of %s: %w",
				name, action.Mountpoint, err))
		}
	}
	if err := e.deleteRecordFiles(name); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// removeRuntime purges the runtime directory of one mount action
// through the injected seam, defaulting to sessions/<name>/runtime/
// <RuntimeID>. A mount with no RuntimeID and no derivable seq purges
// nothing.
func (e *Engine) removeRuntime(name string, action Action) error {
	if e.RemoveRuntime != nil {
		return e.RemoveRuntime(action.Mountpoint)
	}
	root := e.SessionsDir
	if root == "" {
		root = e.Dir
	}
	id := action.RuntimeID
	if id == "" && action.Seq > 0 {
		id = runtimeIDOf(action.Seq)
	}
	if root == "" || name == "" || id == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(root, name, "runtime", id))
}

// CloseRemovableFor closes the --rm session whose record carries a
// mount at dir once no live view remains on it — the lifecycle tail
// of an umount that removed the session's last view. Only records
// marked RemoveOnClose auto-close; other sessions stay active until
// an explicit close. The candidate is picked under the non-blocking
// scan lock and the rule is re-checked under the non-blocking
// exclusive lock that mutates, so a concurrent mount that revived
// the session keeps it open; a busy or unavailable lock skips the
// close, the conservative side that leaves the record to the
// garbage collection fallback. The view at dir is already down, so
// the idempotent umount seam makes the reverse-order teardown a
// no-op and closeLocked purges the --rm record. The return is the
// closed session name, empty when nothing closed.
func (e *Engine) CloseRemovableFor(dir string, umount UmountFunc) (string, error) {
	health, err := e.healthFunc()
	if err != nil {
		return "", err
	}
	name, err := e.removableCandidate(dir, health)
	if err != nil || name == "" {
		return "", err
	}
	return e.closeRemovable(name, dir, health, umount)
}

// ReleaseMount marks every active mount action at dir as unmounted
// without closing the session. Umount and preempt use this after
// tearing a view down so a non--rm record stays active but no longer
// claims StatusMounted, and a remount that mints a new name does not
// compete with a dead claim when resolving the runtime path.
func (e *Engine) ReleaseMount(dir string) error {
	var names []string
	err := e.eachRecord(func(rec Record) bool {
		if rec.State != StateActive {
			return false
		}
		for _, action := range rec.Actions {
			if action.Type == TypeMount &&
				action.Status != StatusUnmounted &&
				action.Mountpoint == dir {
				names = append(names, rec.Name)
				return false
			}
		}
		return false
	})
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := e.releaseMountActions(name, dir); err != nil {
			return err
		}
	}
	return nil
}

// releaseMountActions writebacks StatusUnmounted for every live
// mount action at dir under one session.
func (e *Engine) releaseMountActions(name, dir string) error {
	if e.needsVersionScope() {
		return e.withVersionSession(name, func(local *Engine) error { return local.releaseMountActions(name, dir) })
	}
	release, err := e.lockExclusive(name)
	if err != nil {
		return err
	}
	defer release()
	rec, err := e.Load(name)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, action := range rec.Actions {
		if action.Type != TypeMount ||
			action.Status == StatusUnmounted ||
			action.Mountpoint != dir {
			continue
		}
		if err := e.Writeback(name, action.Seq, StatusUnmounted, ""); err != nil {
			return err
		}
	}
	return nil
}

// RebaseMountedStack rewrites the desired stack of every live mount
// action serving mountpoint from the library image name: a build
// published from the mounted view itself moves the next mount to the
// new stack, while the live view keeps serving the replaced one until
// a real remount. The replaced stack lands in the served fields so
// reference protection covers both stacks, and a chained rebase keeps
// the original served values: the live view never moved. Like
// ReleaseMount, this is a two-phase walk — a candidate scan over the
// records, then a Load/modify/Save per candidate under the session's
// exclusive lock. Closed records and unmounted actions are left
// alone; an action already naming the new stack is a no-op. Callers
// pass a non-empty mountpoint and name: an empty query matches no
// action — a file-path mount records no identity — and the call is a
// silent no-op.
func (e *Engine) RebaseMountedStack(mountpoint, name, newFullID, newTipID string) error {
	return e.rebaseMountedStack(mountpoint, name, newFullID, newTipID, false)
}

// RebaseMountedStackLocked reuses a library lock held by a compound caller.
func (e *Engine) RebaseMountedStackLocked(mountpoint, name, newFullID, newTipID string) error {
	return e.rebaseMountedStack(mountpoint, name, newFullID, newTipID, true)
}

func (e *Engine) rebaseMountedStack(mountpoint, name, newFullID, newTipID string, libraryHeld bool) error {
	var names []string
	err := e.eachRecord(func(rec Record) bool {
		if rec.State != StateActive {
			return false
		}
		for _, action := range rec.Actions {
			if rebaseHit(action, mountpoint, name) {
				names = append(names, rec.Name)
				return false
			}
		}
		return false
	})
	if err != nil {
		return err
	}
	for _, sessionName := range names {
		run := func(local *Engine) error {
			return local.rebaseActions(sessionName, mountpoint, name, newFullID, newTipID)
		}
		var err error
		if libraryHeld {
			err = e.WithVersionSessionLocked(sessionName, run)
		} else {
			err = e.rebaseActions(sessionName, mountpoint, name, newFullID, newTipID)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// rebaseActions rewrites the matching actions of one session under
// its exclusive lock, re-checking the hit rule so a concurrent close
// or teardown between the scan and the lock keeps the record as it
// is. The record is saved only when an action actually changed.
func (e *Engine) rebaseActions(
	sessionName, mountpoint, name, newFullID, newTipID string,
) error {
	if e.needsVersionScope() {
		return e.withVersionSession(sessionName, func(local *Engine) error {
			return local.rebaseActions(sessionName, mountpoint, name, newFullID, newTipID)
		})
	}
	release, err := e.lockExclusive(sessionName)
	if err != nil {
		return err
	}
	defer release()
	rec, err := e.Load(sessionName)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if rec.State != StateActive {
		return nil
	}
	changed := false
	for i := range rec.Actions {
		action := &rec.Actions[i]
		if !rebaseHit(*action, mountpoint, name) ||
			(action.FullID == newFullID && action.TipID == newTipID) {
			continue
		}
		// The served fields keep their first value across chained
		// rebases: they name the stack the live view serves, and
		// that view never remounted in between.
		action.ServedID = firstNonEmpty(action.ServedID, action.FullID)
		action.ServedTip = firstNonEmpty(action.ServedTip, action.TipID, action.FullID)
		action.FullID = newFullID
		action.TipID = newTipID
		changed = true
	}
	if !changed {
		return nil
	}
	return e.Save(rec)
}

// rebaseHit reports whether one recorded action is a live mount of
// the library image name at mountpoint: the rewrite target of a
// build published from that very view. The mountpoint compares
// through SameMountPoint, so a symlinked spelling of the same
// directory still hits. An empty mountpoint or name never hits:
// SameMountPoint matches empty-for-empty and a file-path action
// records no name, so without this guard an empty query would stamp
// library stack ids onto a file-path mount. Identity.Lookup keeps
// the same contract.
func rebaseHit(action Action, mountpoint, name string) bool {
	return mountpoint != "" && name != "" &&
		action.Type == TypeMount &&
		action.Status != StatusUnmounted &&
		SameMountPoint(action.Mountpoint, mountpoint) &&
		action.Name == name
}

// firstNonEmpty returns the first non-empty value, or the empty
// string when every value is empty.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// Discard deletes one record that never came to serve a live view
// of its own. A mount that failed, or one that merely adopted a
// view another session already serves, leaves such a record behind;
// keeping it would pin the image and leak an active name that no
// view answers to. A record with a mounted action that was not an
// adoption describes a view this session serves, so it stays. Name
// shape does not gate discard. The return reports whether the
// record was deleted.
func (e *Engine) Discard(name string, reused bool) (bool, error) {
	if e.needsVersionScope() {
		var removed bool
		err := e.withVersionSession(name, func(local *Engine) (err error) {
			removed, err = local.Discard(name, reused)
			return err
		})
		return removed, err
	}
	release, err := e.lockExclusive(name)
	if err != nil {
		return false, err
	}
	defer release()
	rec, err := e.Load(name)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, action := range rec.Actions {
		if action.Type == TypeMount && action.Status == StatusMounted && !reused {
			return false, nil
		}
	}
	return true, e.deleteRecordFiles(name)
}

// removableCandidate scans for the active --rm record covering dir
// with no live view left. A record whose scan lock is busy is
// skipped this round.
func (e *Engine) removableCandidate(dir string, health HealthFunc) (string, error) {
	name := ""
	err := e.eachRecord(func(rec Record) bool {
		if removableIdle(rec, dir, health) {
			name = rec.Name
			return true
		}
		return false
	})
	return name, err
}

// closeRemovable flips one scanned candidate under the non-blocking
// exclusive lock, re-checking the rule under it so a mount that
// landed between the scan and the lock keeps the session open.
func (e *Engine) closeRemovable(
	name, dir string,
	health HealthFunc,
	umount UmountFunc,
) (string, error) {
	if e.needsVersionScope() {
		var closed string
		err := e.tryVersionSession(name, func(local *Engine) (err error) {
			closed, err = local.closeRemovable(name, dir, health, umount)
			return err
		})
		return closed, err
	}
	release, err := e.lockFor(name, 0)
	if err != nil {
		if errors.Is(err, ErrLockHeld) || errors.Is(err, ErrLockUnavailable) {
			return "", nil
		}
		return "", err
	}
	defer release()
	rec, err := e.Load(name)
	if errors.Is(err, ErrNotFound) {
		// A concurrent pass removed the record.
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !removableIdle(rec, dir, health) {
		return "", nil
	}
	_, err = e.closeLocked(name, umount)
	return name, err
}

// removableIdle reports whether one record is an active --rm
// session covering dir with no live view left — the auto-close
// rule, evaluated once by the scan and again under the mutating
// lock.
func removableIdle(rec Record, dir string, health HealthFunc) bool {
	return rec.State == StateActive && rec.RemoveOnClose &&
		recordsMountpoint(rec, dir) && !hasHealthyMount(rec, health)
}

// Remove deletes one closed record and its lock file. An active
// record is refused: close it first, so a session holding mounts is
// never orphaned.
func (e *Engine) Remove(name string) error {
	if e.needsVersionScope() {
		return e.withVersionSession(name, func(local *Engine) error { return local.Remove(name) })
	}
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
	return e.deleteRecordFiles(name)
}

// deleteRecordFiles removes the session's whole directory: its
// record, lock, and any runtime subtree beneath it. The caller holds
// the session's exclusive lock.
func (s Store) deleteRecordFiles(name string) error {
	if s.scope != nil {
		return s.scope.remove(name)
	}
	dir, err := s.sessionDir(name)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	return nil
}

// healthFunc resolves the health seam over one fresh snapshot. A
// host that reconciles through hdiutil info uses that listing. The
// Linux mount table is not available there; returning that error
// would skip a --rm close after detach had already succeeded.
func (e *Engine) healthFunc() (HealthFunc, error) {
	if e.useDiskImages() {
		images, err := e.diskImages()
		if err != nil {
			return nil, err
		}
		return diskListed(images), nil
	}
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
// single-point check — the injected seam or the shared two-condition
// check. Overview annotations, the open recovery decision, and the
// reverse lookup all share this one closure.
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

// lockExclusive takes the session's exclusive lock through the
// injected seam, defaulting to the real advisory flock.
func (e *Engine) lockExclusive(name string) (func(), error) {
	return e.lockFor(name, e.lockTimeout())
}

// lockFor takes the session's lock through the injected seam. A
// zero timeout makes the non-blocking attempt scans and cleanup
// mutations use.
func (e *Engine) lockFor(name string, timeout time.Duration) (func(), error) {
	if e.Lock != nil {
		return e.Lock(name, timeout)
	}
	return e.lockSession(name, timeout)
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
	if e.Versioned {
		return e.ListVersion()
	}
	return e.Store.List()
}

// Show returns one record for command rendering.
func (e *Engine) Show(name string) (Record, error) {
	if e.Versioned {
		return e.LoadVersion(name)
	}
	return e.Load(name)
}

// Summary is the short-list read model of one session.
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
	// RemoveOnClose is true for a --rm session.
	RemoveOnClose bool
}

// Summarize reduces one record to its list row.
func Summarize(rec Record) Summary {
	mounts := 0
	for _, action := range rec.Actions {
		if action.Type == TypeMount && action.Status == StatusMounted {
			mounts++
		}
	}
	return Summary{
		Name:          rec.Name,
		State:         rec.State,
		Mounts:        mounts,
		Created:       rec.CreatedAt,
		Closed:        rec.ClosedAt,
		RemoveOnClose: rec.RemoveOnClose,
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
