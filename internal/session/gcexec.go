package session

import (
	"errors"
	"fmt"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
	"github.com/jiangxin/tiler/internal/filelock"
)

// GCReport is what one execution did with an approved plan. Removed lists
// the deleted session names in plan order; Kept explains every candidate
// that survived.
type GCReport struct {
	// Removed is the deleted session names, in plan order.
	Removed []string
	// Kept is the candidates that survived, each with its reason.
	Kept []GCSkip
}

// keep records one kept candidate.
func (r *GCReport) keep(skip GCSkip) {
	r.Kept = append(r.Kept, skip)
}

// ExecSessions deletes the candidates of one approved plan, and only those:
// a session the plan did not offer is never revisited, because the preview a
// user answered described exactly that list.
//
// Each candidate is removed through the coordinated reference path, which
// holds the library lock and then the session lock and revokes the session's
// references before its directory goes away. Inside those locks the execution
// rechecks the snapshot the plan pinned: a recreated or rewritten record, a log
// that gained or lost its newest record, an activity that moved past the plan's
// fixed cutoff, and a view that is mounted now are each reported and kept. The
// attachment guard reads the host again while the locks are held, so a mount
// that appeared after the preview is never deleted; that costs one host query
// per approved session, which is why an execution only asks about the list a
// user already approved. Only a session that still looks exactly as planned is
// removed, and an eligible active record is deleted as it stands, without being
// flipped to closed first.
//
// One kept candidate never stops the rest. Every failure is joined into the
// returned error and is also reported with GCSkipFailed, so a caller can print
// the outcome of the whole plan.
func (e *Engine) ExecSessions(plan GCPlan) (GCReport, error) {
	if plan.Cutoff.IsZero() {
		return GCReport{}, errors.New("session: gc exec: zero cutoff")
	}
	report := GCReport{}
	var failures []error
	for _, candidate := range plan.Candidates {
		if err := e.execCandidate(&report, candidate, plan.Cutoff); err != nil {
			failures = append(failures, err)
		}
	}
	return report, errors.Join(failures...)
}

// execCandidate deletes one planned candidate, entering the coordinated
// reference scope first when the engine keeps a versioned library.
func (e *Engine) execCandidate(report *GCReport, candidate GCCandidate, cutoff time.Time) error {
	if e.needsVersionScope() {
		return e.execVersioned(report, candidate, cutoff)
	}
	return e.execLocked(report, candidate, cutoff)
}

// execVersioned runs one planned deletion under the library then session locks
// of the reference scope. A lock another process holds is reported busy, a
// platform without locks is reported unlocked, and a scope that cannot be
// opened is reported failed; the callback only runs once both locks are held,
// so an error it returns has already been classified by execLocked.
func (e *Engine) execVersioned(report *GCReport, candidate GCCandidate, cutoff time.Time) error {
	name := candidate.Name
	entered := false
	err := e.withVersionSessionImages(name, nil, -1, func(local *Engine) error {
		entered = true
		return local.execLocked(report, candidate, cutoff)
	})
	if entered {
		// The callback only runs once the library lock and then the
		// session lock are held, so it recorded the outcome of every
		// candidate it saw.
		return err
	}
	// The callback never ran: the coordinated locks were not taken and
	// err carries the cause.
	switch {
	case errors.Is(err, ErrLockHeld), errors.Is(err, filelock.ErrHeld),
		errors.Is(err, filelock.ErrTimeout):
		report.keep(GCSkip{Name: name, Reason: GCSkipBusy,
			Detail: "another process holds the session"})
		return nil
	case errors.Is(err, ErrLockUnavailable), errors.Is(err, filelock.ErrUnavailable):
		report.keep(GCSkip{Name: name, Reason: GCSkipUnlocked,
			Detail: "the host cannot lock a session"})
		return nil
	}
	report.keep(GCSkip{Name: name, Reason: GCSkipFailed, Detail: err.Error()})
	return fmt.Errorf("session: gc: remove %s: %w", name, err)
}

// execLocked deletes one planned candidate under its session lock. Every
// recheck failure keeps the session and reports why; a deletion that fails
// keeps it and reports both the outcome and the error.
func (e *Engine) execLocked(report *GCReport, candidate GCCandidate, cutoff time.Time) error {
	name := candidate.Name
	keep := func(reason, detail string) {
		report.keep(GCSkip{Name: name, Reason: reason, Detail: detail})
	}
	release, err := e.lockFor(name, 0)
	switch {
	case err == nil:
		defer release()
	case errors.Is(err, ErrLockHeld):
		keep(GCSkipBusy, "another process holds the session")
		return nil
	case errors.Is(err, ErrLockUnavailable):
		keep(GCSkipUnlocked, "the host cannot lock a session")
		return nil
	default:
		keep(GCSkipUnreadable, err.Error())
		return nil
	}
	rec, err := e.Load(name)
	if errors.Is(err, ErrNotFound) {
		keep(GCSkipGone, "the session is no longer stored")
		return nil
	}
	if err != nil {
		keep(GCSkipUnreadable, err.Error())
		return nil
	}
	if reason, detail := e.recheckCandidate(rec, candidate, cutoff); reason != "" {
		keep(reason, detail)
		return nil
	}
	point, err := e.Attached(rec)
	if err != nil {
		keep(GCSkipUnreadable, err.Error())
		return nil
	}
	if point != "" {
		keep(GCSkipAttached, point)
		return nil
	}
	if err := e.deleteRecordFiles(name); err != nil {
		keep(GCSkipFailed, err.Error())
		return fmt.Errorf("session: gc: remove %s: %w", name, err)
	}
	report.Removed = append(report.Removed, name)
	return nil
}

// recheckCandidate reports why one stored record no longer matches the
// snapshot the plan pinned, or an empty reason when it still does. The record
// stamp covers a recreation and a rewrite; the activity revision covers a
// gained or lost newest record; the cutoff is the plan's fixed boundary, not a
// freshly computed one.
func (e *Engine) recheckCandidate(rec Record, candidate GCCandidate, cutoff time.Time) (string, string) {
	stamp, err := e.recordStamp(rec)
	if err != nil {
		return GCSkipUnreadable, err.Error()
	}
	if stamp != candidate.record {
		return GCSkipChanged, "the record changed since the preview"
	}
	rev, err := e.sessionRevision(rec)
	if errors.Is(err, activity.ErrNoActivity) {
		// The plan pinned a newest record, so an empty log now is a
		// change rather than a session that was never initialized.
		return GCSkipChanged, "the activity log is gone since the preview"
	}
	if err != nil {
		return GCSkipUnreadable, err.Error()
	}
	if rev != candidate.activity {
		return GCSkipChanged, "the activity log changed since the preview"
	}
	if !rev.Last.Before(cutoff) {
		return GCSkipChanged, "the last activity is no longer before the cutoff"
	}
	return "", ""
}

// sessionRevision reads the newest activity record of one session without
// initializing anything: an execution rechecks a log the plan already
// inspected, so a missing log is a change and never a migration.
func (e *Engine) sessionRevision(rec Record) (activity.Revision, error) {
	w, err := e.activityWriter(rec.Name)
	if err != nil {
		return activity.Revision{}, err
	}
	return w.Revision()
}
