package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/jiangxin/tiler/internal/activity"
	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
)

// collectVersionActivity enriches text views without renewing their logs.
func collectVersionActivity(views []versionImageView, cfg config.Config, warnings io.Writer) error {
	for i := range views {
		repo, err := image.OpenRepo(cfg.ImagesDir, views[i].Name)
		if err != nil {
			return err
		}
		repo.LogsDir = cfg.LogsDir
		views[i].Activity = make(map[string]imageListActivity, len(views[i].Images))
		for _, node := range views[i].Images {
			at, err := image.ReadVersionLastActivity(repo, node.ID)
			switch {
			case err == nil:
				views[i].Activity[node.ID] = imageListActivity{At: at}
			case errors.Is(err, activity.ErrNoActivity):
				views[i].Activity[node.ID] = imageListActivity{}
			default:
				views[i].Activity[node.ID] = imageListActivity{Unknown: true}
				if _, writeErr := fmt.Fprintf(warnings, "Warning: images list: %s:%s: %v\n", views[i].Name, node.ID, err); writeErr != nil {
					return writeErr
				}
			}
		}
	}
	return nil
}
