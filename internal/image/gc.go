package image

import (
	"fmt"
	"time"

	"github.com/jiangxin/tiler/internal/config"
)

// GCActionRemoved reports an immutable object deletion.
const GCActionRemoved = "removed"

// GCEvent is one garbage collection action a command can report.
type GCEvent struct {
	Name   string
	Action string
	FullID string
}

// GC is the legacy authorized cleanup entry point. Interactive callers must
// inspect and confirm CleanupPlan through the reference coordinator instead.
func GC(cfg config.Config, _ Refs, _ time.Time) ([]GCEvent, error) {
	release, err := LockLibrary(cfg.ImagesDir, DefaultLibraryLockTimeout)
	if err != nil {
		return nil, fmt.Errorf("gc: %w", err)
	}
	defer release()
	plan, err := InspectCleanupLocked(cfg.ImagesDir)
	if err != nil {
		return nil, fmt.Errorf("gc: %w", err)
	}
	outcomes, err := ExecuteCleanupLocked(cfg.ImagesDir, plan)
	events := make([]GCEvent, 0, len(outcomes))
	for _, outcome := range outcomes {
		if outcome.Action == GCActionRemoved {
			events = append(events, GCEvent{Name: outcome.Name, Action: outcome.Action, FullID: outcome.ID})
		}
	}
	return events, err
}
