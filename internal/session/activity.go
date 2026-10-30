package session

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jiangxin/tiler/internal/activity"
)

// ErrActivityFailed reports a lifecycle change that already took
// effect while its activity record could not be written. The record
// change and any mount or teardown it performed stand; only the log
// entry is missing, so the operation must never be reported as a
// rollback.
var ErrActivityFailed = errors.New("lifecycle completed but activity log failed")

// activityLog opens the activity log of one session. The caller holds
// the session's exclusive lock, so the log follows the record it
// describes. A nil writer means no sessions root is configured, which
// unit tests that exercise the record store alone rely on. An empty
// logs directory fails the open instead of writing beside the record.
func (e *Engine) activityLog(name string) (*activity.Writer, error) {
	if e.Activity != nil {
		return e.Activity(name)
	}
	if e.Dir == "" {
		return nil, nil
	}
	if strings.TrimSpace(e.LogsDir) == "" {
		return nil, errors.New("session: activity log directory is not configured")
	}
	if _, err := e.sessionDir(name); err != nil {
		return nil, err
	}
	dir := filepath.Join(e.LogsDir, "sessions")
	if err := activity.PrepareDir(e.LogsDir, dir); err != nil {
		return nil, err
	}
	return activity.OpenLog(e.LogsDir, dir, name+".log", e.now)
}

// beginActivity writes the started record of one session-level
// lifecycle action and returns the closure that records its outcome.
func (e *Engine) beginActivity(name, event string) (func(error) error, error) {
	return e.beginEvent(name, activity.Event{Event: event, Session: name})
}

// beginActionActivity writes the started record of one mount or
// unmount action, carrying the action's sequence and mountpoint so a
// log line names the view the record lists. A failed started write
// returns before the action runs, so a log that cannot be written
// never leaves an unrecorded side effect behind.
func (e *Engine) beginActionActivity(name, event string, action Action) (func(error) error, error) {
	return e.beginEvent(name, activity.Event{
		Event:      event,
		Session:    name,
		Seq:        action.Seq,
		Mountpoint: action.Mountpoint,
	})
}

// beginEvent writes the started phase of one event and returns the
// closure that records its outcome: the success or the failure with
// the action's diagnostic text. The closure keeps the action error
// and only adds the log failure when the outcome record itself
// fails, so a completed action is never reported as a rollback.
func (e *Engine) beginEvent(name string, started activity.Event) (func(error) error, error) {
	w, err := e.activityLog(name)
	if err == nil {
		started.Phase = activity.PhaseStarted
		err = appendEvent(w, started)
	}
	if err != nil {
		return nil, fmt.Errorf("session: %s: activity %s started: %w",
			name, started.Event, err)
	}
	return func(opErr error) error {
		outcome := started
		outcome.Phase = activity.PhaseSucceeded
		if opErr != nil {
			outcome.Phase = activity.PhaseFailed
			outcome.Error = opErr.Error()
		}
		if err := appendEvent(w, outcome); err != nil {
			if opErr != nil {
				return fmt.Errorf("%w (activity %s failed: %v)",
					opErr, outcome.Event, err)
			}
			return fmt.Errorf("%w: session %s %s: %w",
				ErrActivityFailed, name, outcome.Event, err)
		}
		return opErr
	}, nil
}

// appendEvent appends one record. A nil writer means no sessions root
// is configured, so nothing is recorded.
func appendEvent(w *activity.Writer, ev activity.Event) error {
	if w == nil {
		return nil
	}
	_, err := w.Append(ev)
	return err
}

// recordCreated appends the one create event of a session whose
// record is already durable. A log failure reports a completed
// creation with a missing activity record instead of undoing the
// session.
func (e *Engine) recordCreated(name string) error {
	w, err := e.activityLog(name)
	if err == nil {
		err = appendEvent(w, activity.Event{
			Event:   activity.EventSessionCreate,
			Phase:   activity.PhaseSucceeded,
			Session: name,
		})
	}
	if err != nil {
		return fmt.Errorf("%w: session %s create: %w", ErrActivityFailed, name, err)
	}
	return nil
}
