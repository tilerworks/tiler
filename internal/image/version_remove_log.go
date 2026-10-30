package image

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jiangxin/tiler/internal/activity"
)

// recordedLogsDir is the journal value for logsDir. An empty root
// stays empty so an old journal, which has no logs_dir, is distinct
// from a path.
func recordedLogsDir(logsDir string) string {
	if strings.TrimSpace(logsDir) == "" {
		return ""
	}
	return filepath.Clean(logsDir)
}

// imageActivityDir is logsDir/images/<segments>. Segments use the
// same checks as an image name, and the result is what removal
// deletes.
func imageActivityDir(logsDir string, segments []string) (string, error) {
	if strings.TrimSpace(logsDir) == "" {
		return "", fmt.Errorf("%w: empty logs dir", ErrInvalidPath)
	}
	if len(segments) == 0 {
		return "", fmt.Errorf("%w: empty image name", ErrInvalidPath)
	}
	for _, seg := range segments {
		if err := checkSegment(seg); err != nil {
			return "", err
		}
	}
	parts := append([]string{logsDir, "images"}, segments...)
	return filepath.Join(parts...), nil
}

// cleanupRemovalLogs deletes the logs named by the journal and wraps
// a failure so the journal stays for another replay.
func cleanupRemovalLogs(repo Repo, j removalJournal) error {
	if err := removeJournalLogs(repo, j); err != nil {
		return fmt.Errorf("%w: %w", ErrCleanupPending, err)
	}
	return nil
}

// removeJournalLogs deletes logs from the directory stored in the
// journal. An empty logs_dir is an old journal: those logs lived in
// the object directories and disappear with them.
func removeJournalLogs(repo Repo, j removalJournal) error {
	if strings.TrimSpace(j.LogsDir) == "" {
		return nil
	}
	logsDir := filepath.Clean(j.LogsDir)
	if j.Delete {
		return removeImageLogDirectory(logsDir, repo.Segments)
	}
	return removeVersionLogs(logsDir, repo.Segments, j.IDs)
}

// removeImageLogDirectory deletes one image name's whole log
// directory, then empty parent segments under logs_dir/images.
func removeImageLogDirectory(logsDir string, segments []string) error {
	dir, err := imageActivityDir(logsDir, segments)
	if err != nil {
		return err
	}
	if err := activity.RemoveLogDir(logsDir, dir); err != nil {
		return err
	}
	stop := filepath.Join(logsDir, "images")
	return activity.PruneEmptyDirs(filepath.Dir(dir), stop)
}

// removeVersionLogs deletes each version log and its lock, then
// removes the image directory when that was the last log.
func removeVersionLogs(logsDir string, segments []string, ids []string) error {
	dir, err := imageActivityDir(logsDir, segments)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := checkID(id); err != nil {
			return err
		}
		if err := activity.RemoveLog(logsDir, filepath.Join(dir, id+".log")); err != nil {
			return err
		}
	}
	return activity.PruneEmptyDirs(dir, filepath.Join(logsDir, "images"))
}
