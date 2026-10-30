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
	var events []GCEvent
	err := WithListedImageLocks(cfg.ImagesDir, DefaultLibraryLockTimeout, func(hold Hold) error {
		plan, err := InspectCleanupLocked(cfg.ImagesDir)
		if err != nil {
			return err
		}
		outcomes, err := ExecuteCleanupLocked(cfg.ImagesDir, cfg.LogsDir, plan)
		events = make([]GCEvent, 0, len(outcomes))
		for _, outcome := range outcomes {
			if outcome.Action != GCActionRemoved {
				continue
			}
			events = append(events, GCEvent{Name: outcome.Name, Action: outcome.Action, FullID: outcome.ID})
			if err == nil {
				if discardErr := hold.DiscardIfRepositoryGone(cfg.ImagesDir, outcome.Name); discardErr != nil {
					err = discardErr
				}
			}
		}
		return err
	})
	if err != nil {
		return events, fmt.Errorf("gc: %w", err)
	}
	return events, nil
}
