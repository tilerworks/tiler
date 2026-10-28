package session

import (
	"errors"
	"fmt"

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
// unit tests that exercise the record store alone rely on.
func (e *Engine) activityLog(name string) (*activity.Writer, error) {
	if e.Activity != nil {
		return e.Activity(name)
	}
	if e.Dir == "" {
		return nil, nil
	}
	dir, err := e.sessionDir(name)
	if err != nil {
		return nil, err
	}
	return activity.OpenLog(e.Dir, dir, e.now)
}

// beginActivity writes the started record of one lifecycle action and
// returns the closure that records its outcome. A started write
// failure returns before the action runs, so a session whose log
// cannot be written is never opened or closed without the activity
// that protects it. The closure keeps the action error and only adds
// the log failure when the outcome record itself fails.
func (e *Engine) beginActivity(name, event string) (func(error) error, error) {
	w, err := e.activityLog(name)
	if err == nil {
		err = startActivity(w, name, event)
	}
	if err != nil {
		return nil, fmt.Errorf("session: %s: activity %s started: %w", name, event, err)
	}
	return func(opErr error) error {
		if err := finishActivity(w, name, event, opErr); err != nil {
			if opErr != nil {
				return fmt.Errorf("%w (activity %s failed: %v)", opErr, event, err)
			}
			return fmt.Errorf("%w: session %s %s: %w", ErrActivityFailed, name, event, err)
		}
		return opErr
	}, nil
}

// startActivity appends the started phase of one event. A nil writer
// records nothing.
func startActivity(w *activity.Writer, name, event string) error {
	if w == nil {
		return nil
	}
	_, err := w.Append(activity.Event{
		Event:   event,
		Phase:   activity.PhaseStarted,
		Session: name,
	})
	return err
}

// finishActivity appends the succeeded or failed phase of one event
// and keeps the diagnostic text of a failed action.
func finishActivity(w *activity.Writer, name, event string, opErr error) error {
	if w == nil {
		return nil
	}
	ev := activity.Event{
		Event:   event,
		Phase:   activity.PhaseSucceeded,
		Session: name,
	}
	if opErr != nil {
		ev.Phase = activity.PhaseFailed
		ev.Error = opErr.Error()
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
	if err != nil {
		return fmt.Errorf("%w: session %s create: %w", ErrActivityFailed, name, err)
	}
	if w == nil {
		return nil
	}
	if _, err := w.Append(activity.Event{
		Event:   activity.EventSessionCreate,
		Phase:   activity.PhaseSucceeded,
		Session: name,
	}); err != nil {
		return fmt.Errorf("%w: session %s create: %w", ErrActivityFailed, name, err)
	}
	return nil
}
