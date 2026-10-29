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
func (c ReferenceCoordinator) PlanImageGC(cutoff time.Time, clock activity.Clock) (plan ImageGCPlan, err error) {
	if cutoff.IsZero() {
		return plan, errors.New("image gc: zero cutoff")
	}
	err = c.withLibrary(func() error {
		var inspectErr error
		plan.Cleanup, inspectErr = image.InspectCleanupLocked(c.ImagesDir)
		if inspectErr != nil {
			return inspectErr
		}
		return c.checkCleanupReferencesLocked(plan.Cleanup)
	})
	if err == nil && !plan.Cleanup.Pending {
		plan.Versions, err = c.PlanImages(cutoff, clock)
	}
	if err != nil {
		return ImageGCPlan{}, err
	}
	return plan, nil
}

// ExecuteImageGC executes only the displayed sets, each revalidated under its
// library lock. Recovery does not add newly discovered or expired objects.
func (c ReferenceCoordinator) ExecuteImageGC(plan ImageGCPlan, clock activity.Clock) (outcomes []image.VersionGCOutcome, err error) {
	err = c.withLibrary(func() error {
		if err := c.checkCleanupReferencesLocked(plan.Cleanup); err != nil {
			return err
		}
		outcomes, err = image.ExecuteCleanupLocked(c.ImagesDir, plan.Cleanup)
		return err
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
