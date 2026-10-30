package image

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
)

// ReadVersionLastActivity reads the newest activity of one version without
// initializing its log or creating missing directories. It may create the
// stable log lock file. Missing and empty logs report activity.ErrNoActivity.
func ReadVersionLastActivity(repo Repo, id string) (time.Time, error) {
	dir, name, err := repo.activityLogFile(id)
	if err != nil {
		return time.Time{}, err
	}
	w, err := activity.OpenLog(repo.LogsDir, dir, name, func() time.Time { return time.Now().UTC() })
	var at time.Time
	if err == nil {
		at, err = w.LastActivity()
	}
	if errors.Is(err, os.ErrNotExist) {
		err = activity.ErrNoActivity
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("image %s: activity tail of version %s: %w", repo.Name(), id, err)
	}
	return at, nil
}
