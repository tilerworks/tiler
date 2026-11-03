package cli

import (
	"io"
	"time"

	"github.com/jiangxin/tiler/internal/session"
)

type sessionActivityJSON struct {
	LastActivityAt      *string `json:"last_activity_at"`
	LastActivityUnknown bool    `json:"last_activity_unknown"`
}

func newSessionActivityJSON(value session.ActivityView) sessionActivityJSON {
	entry := sessionActivityJSON{LastActivityUnknown: value.Unknown}
	if !value.Unknown && !value.At.IsZero() {
		at := value.At.UTC().Format(time.RFC3339)
		entry.LastActivityAt = &at
	}
	return entry
}

func formatSessionActivity(value session.ActivityView, now time.Time) string {
	if value.Unknown {
		return "unknown"
	}
	return formatImageListTime(value.At, now)
}

func loadSessionActivity(engine *session.Engine, summary *session.Summary, diagnostics io.Writer) {
	value, err := engine.LastActivity(summary.Name)
	summary.Activity = value
	if err != nil {
		_, _ = writeWarning(diagnostics, "session", "%q: last activity is unknown: %s\n", summary.Name, err)
	}
}

func loadStatusActivity(engine *session.Engine, overview *session.Overview, diagnostics io.Writer) {
	for i := range overview.Sessions {
		view := &overview.Sessions[i]
		if view.Locked {
			view.Activity.Unknown = true
			continue
		}
		loadSessionActivity(engine, &view.Summary, diagnostics)
	}
}
