package session

import (
	"errors"
	"fmt"

	"github.com/jiangxin/tiler/internal/activity"
	"github.com/jiangxin/tiler/internal/image"
)

// ExecuteImages rechecks approved image plans under the library lock without
// replaying a pending image or session cleanup. Outcomes cover approved IDs
// only; independent repositories may continue after an ordinary failure.
func (c ReferenceCoordinator) ExecuteImages(plans []image.VersionGCPlan, clock activity.Clock) (outcomes []image.VersionGCOutcome, err error) {
	err = c.withLibrary(func() error {
		if err := image.CheckVersionGCReadyLocked(c.ImagesDir); err != nil {
			return err
		}
		refs, err := c.gcReferencesLocked()
		if err != nil {
			return err
		}
		var failures error
		for _, plan := range plans {
			repo, runErr := image.OpenRepo(c.ImagesDir, plan.Name)
			if runErr != nil {
				return errors.Join(failures, runErr)
			}
			events, runErr := image.ExecuteVersionGCLocked(repo, plan, refs[plan.Name], clock)
			outcomes = append(outcomes, events...)
			if runErr != nil {
				failures = errors.Join(failures, fmt.Errorf("image gc: %s: %w", plan.Name, runErr))
				if errors.Is(runErr, image.ErrCleanupPending) {
					break
				}
			}
		}
		return failures
	})
	return outcomes, err
}
