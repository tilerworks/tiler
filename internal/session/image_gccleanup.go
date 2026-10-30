package session

import (
	"errors"
	"fmt"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
	"github.com/jiangxin/tiler/internal/image"
)

// ImageGCPlan contains the complete displayed cleanup and expiration snapshot.
// A pending cleanup is a standalone batch; expiration requires a fresh run.
type ImageGCPlan struct {
	Cleanup  image.CleanupPlan
	Versions []image.VersionGCPlan
}

// PlanImageGC never replays deletion or recovers session transactions. Missing
// image activity may only be initialized when no pending cleanup exists.
func (c ReferenceCoordinator) PlanImageGC(cutoff time.Time, clock activity.Clock) (ImageGCPlan, error) {
	plan, _, err := c.PlanSelectedImageGC(cutoff, clock, image.NameSelector{})
	return plan, err
}

// PlanSelectedImageGC scopes every cleanup and expiry candidate to fixed names.
func (c ReferenceCoordinator) PlanSelectedImageGC(cutoff time.Time, clock activity.Clock, selector image.NameSelector) (plan ImageGCPlan, unmatched []string, err error) {
	if cutoff.IsZero() {
		return plan, nil, errors.New("image gc: zero cutoff")
	}
	err = c.withLibrary(func() error {
		var inspectErr error
		var names []string
		plan.Cleanup, names, unmatched, inspectErr = image.InspectSelectedCleanupLocked(c.ImagesDir, selector)
		if inspectErr != nil {
			return inspectErr
		}
		if err := c.checkCleanupReferencesLocked(plan.Cleanup); err != nil {
			return err
		}
		if !plan.Cleanup.Pending && len(names) > 0 {
			plan.Versions, inspectErr = c.planImageNamesLocked(cutoff, clock, names)
		}
		return inspectErr
	})
	if err != nil {
		return ImageGCPlan{}, unmatched, err
	}
	return plan, unmatched, nil
}

// ExecuteImageGC executes only the displayed sets, each revalidated under its
// library lock. Recovery does not add newly discovered or expired objects.
func (c ReferenceCoordinator) ExecuteImageGC(plan ImageGCPlan, clock activity.Clock) (outcomes []image.VersionGCOutcome, err error) {
	err = c.withAllImages(func(hold image.Hold) error {
		if err := c.checkCleanupReferencesLocked(plan.Cleanup); err != nil {
			return err
		}
		outcomes, err = image.ExecuteCleanupLocked(c.ImagesDir, c.LogsDir, plan.Cleanup)
		if err != nil {
			return err
		}
		for _, outcome := range outcomes {
			if outcome.Action != image.GCActionRemoved {
				continue
			}
			if discardErr := hold.DiscardIfRepositoryGone(c.ImagesDir, outcome.Name); discardErr != nil {
				return discardErr
			}
		}
		return nil
	})
	if err == nil && !plan.Cleanup.Pending {
		var events []image.VersionGCOutcome
		events, err = c.ExecuteImages(plan.Versions, clock)
		outcomes = append(outcomes, events...)
	}
	return outcomes, err
}

func (c ReferenceCoordinator) checkCleanupReferencesLocked(plan image.CleanupPlan) error {
	refs, err := c.gcReferencesLocked()
	if err != nil {
		return err
	}
	for _, item := range plan.Items {
		if plan.Pending && refs[item.Name][item.ID] {
			return fmt.Errorf("image gc: cleanup %s:%s is referenced", item.Name, item.ID)
		}
	}
	return nil
}
