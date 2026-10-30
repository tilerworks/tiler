package session

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
)

// ActivityView is derived from a session log, never persisted in its record.
type ActivityView struct {
	At      time.Time
	Unknown bool
}

// LastActivity reads one session's log without creating or updating files.
// A returned diagnostic leaves Unknown set so callers can keep other sessions.
func (e *Engine) LastActivity(name string) (ActivityView, error) {
	if err := ValidateName(name); err != nil {
		return ActivityView{Unknown: true}, err
	}
	if e.LogsDir == "" {
		return ActivityView{}, nil
	}
	at, err := activity.ReadLastActivity(e.LogsDir, filepath.Join(e.LogsDir, "sessions"), name+".log")
	if errors.Is(err, activity.ErrNoActivity) {
		return ActivityView{}, nil
	}
	if err != nil {
		return ActivityView{Unknown: true}, fmt.Errorf("session: %s: last activity: %w", name, err)
	}
	return ActivityView{At: at}, nil
}
