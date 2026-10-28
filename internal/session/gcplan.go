package session

import (
	"errors"
	"fmt"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
)

// Reasons a session GC plan keeps a session out of its removal list.
const (
	// GCSkipAttached keeps a session whose view is still mounted, even
	// when its FUSE daemon died.
	GCSkipAttached = "attached"
	// GCSkipBusy keeps a session another process holds right now.
	GCSkipBusy = "busy"
	// GCSkipInitialized keeps a session whose activity log this scan
	// created, so its expiry window starts now instead of ending today.
	GCSkipInitialized = "initialized"
	// GCSkipUnreadable keeps a session the scan could not inspect.
	GCSkipUnreadable = "unreadable"
)

// GCCandidate is one expired session a plan offers for removal. The
// unexported stamps pin the state the scan saw, so an execution can refuse a
// session that was recreated, rewritten, or used after the preview.
type GCCandidate struct {
	// Name is the session name.
	Name string
	// State is the record state the scan saw. Planning never changes it,
	// and neither an active nor a closed record is preferred: expiry alone
	// decides.
	State string
	// RemoveOnClose reports a --rm record. The flag describes the record;
	// it neither shortens nor skips the expiry rule.
	RemoveOnClose bool
	// LastActive is the newest activity the log held.
	LastActive time.Time

	record   sessionStamp
	activity activity.Revision
}

// sessionStamp pins one stored record file: its incarnation id, the digest of
// its bytes, and its size.
type sessionStamp struct {
	InstanceID string
	Digest     string
	Size       int64
}

// GCSkip is one session a plan kept, with the reason it did.
type GCSkip struct {
	// Name is the session name.
	Name string
	// Reason is one of the GCSkip values.
	Reason string
	// Detail explains the reason: the attachment point or the error.
	Detail string
}

// GCPlan is the read-only result of one expiry scan. It can be shown to a
// user and then handed to an execution, which revalidates every candidate
// against the live host before touching it.
type GCPlan struct {
	// Cutoff is the fixed boundary the scan compared against. A session
	// expires only when its last activity is strictly earlier.
	Cutoff time.Time
	// Candidates are the expired, unattached, idle sessions, ordered by
	// name.
	Candidates []GCCandidate
	// Skips are the sessions kept with a specific reason, ordered by
	// name. A session that is merely young is not listed.
	Skips []GCSkip
}

// PlanSessions scans the stored sessions for the ones that expired before
// cutoff, without changing a record, a mount, or existing activity: it may
// only initialize a missing activity log, and a session it initialized this
// way is never a candidate of the same scan.
//
// The scan holds each session's non-blocking lock while it reads the record,
// so a session another process works on is reported busy and kept. The
// attachment guard decides from one host snapshot, so a view that is still
// mounted is kept whatever the record says its actions did.
func (e *Engine) PlanSessions(cutoff time.Time) (GCPlan, error) {
	cutoff = cutoff.UTC()
	if cutoff.IsZero() {
		return GCPlan{}, errors.New("session: gc plan: zero cutoff")
	}
	names, err := e.names()
	if err != nil {
		return GCPlan{}, err
	}
	host, err := e.attachment()
	if err != nil {
		return GCPlan{}, err
	}
	plan := GCPlan{Cutoff: cutoff}
	for _, name := range names {
		e.planSession(&plan, name, host)
	}
	return plan, nil
}

// planSession classifies one session and records it in the plan. A session
// that cannot be inspected is reported and kept: an unknown state never
// authorizes removal, and only a lock another process holds is reported as
// contention. The whole classification of one session happens under its
// lock, so the record, the log revision, and the identity stamp describe
// one moment instead of three.
func (e *Engine) planSession(plan *GCPlan, name string, host attachment) {
	release, err := e.lockFor(name, 0)
	switch {
	case err == nil:
		defer release()
	case errors.Is(err, ErrLockHeld):
		plan.keep(GCSkip{Name: name, Reason: GCSkipBusy,
			Detail: "another process holds the session"})
		return
	case errors.Is(err, ErrLockUnavailable):
		// The platform has no advisory lock. The record write is atomic,
		// and the shared scan lock reads unlocked for the same reason.
	default:
		plan.keep(GCSkip{Name: name, Reason: GCSkipUnreadable, Detail: err.Error()})
		return
	}
	rec, err := e.Load(name)
	if errors.Is(err, ErrNotFound) {
		return
	}
	if err != nil {
		plan.keep(GCSkip{Name: name, Reason: GCSkipUnreadable, Detail: err.Error()})
		return
	}
	rev, initialized, err := e.inspectActivity(rec)
	if err != nil {
		plan.keep(GCSkip{Name: name, Reason: GCSkipUnreadable, Detail: err.Error()})
		return
	}
	if initialized {
		plan.keep(GCSkip{Name: name, Reason: GCSkipInitialized,
			Detail: "activity log created by this scan"})
		return
	}
	if !rev.Last.Before(plan.Cutoff) {
		return
	}
	point, err := host.point(rec, e.SessionsDir)
	if err != nil {
		plan.keep(GCSkip{Name: name, Reason: GCSkipUnreadable, Detail: err.Error()})
		return
	}
	if point != "" {
		plan.keep(GCSkip{Name: name, Reason: GCSkipAttached, Detail: point})
		return
	}
	stamp, err := e.recordStamp(rec)
	if err != nil {
		plan.keep(GCSkip{Name: name, Reason: GCSkipUnreadable, Detail: err.Error()})
		return
	}
	plan.Candidates = append(plan.Candidates, GCCandidate{
		Name:          rec.Name,
		State:         rec.State,
		RemoveOnClose: rec.RemoveOnClose,
		LastActive:    rev.Last,
		record:        stamp,
		activity:      rev,
	})
}

// keep records one kept session.
func (p *GCPlan) keep(skip GCSkip) {
	p.Skips = append(p.Skips, skip)
}

// inspectActivity initializes a missing session log and returns the revision
// the scan saw. A damaged log, an unsafe file, and a log that cannot be
// written are errors, so the session is kept instead of expired. A session
// that already has a valid log is never renewed by this call.
func (e *Engine) inspectActivity(rec Record) (activity.Revision, bool, error) {
	w, err := e.activityLog(rec.Name)
	if err != nil {
		return activity.Revision{}, false, err
	}
	if w == nil {
		return activity.Revision{}, false, errors.New("no sessions root is configured")
	}
	initialized, rev, err := w.Inspect(activity.Event{
		Event:      activity.EventInitialize,
		Phase:      activity.PhaseInitialized,
		Session:    rec.Name,
		InstanceID: rec.InstanceID,
	})
	return rev, initialized, err
}

// recordStamp pins one stored record through the digest the reference
// inventory keeps, so a plan and an execution compare one spelling of a
// session's identity.
func (s Store) recordStamp(rec Record) (sessionStamp, error) {
	rev, err := s.referenceRevision(rec)
	if err != nil {
		return sessionStamp{}, fmt.Errorf("session: %w", err)
	}
	return sessionStamp{InstanceID: rev.InstanceID, Digest: rev.SHA256, Size: rev.Size}, nil
}
