package image

import (
	"bytes"
	"errors"
	"os"
	"strings"
)

// ExecuteCleanupLocked revalidates a displayed cleanup set under the library
// lock. logsDir deletes orphan version logs. A pending journal uses the
// logs directory it stored, not this argument. Callers must have checked
// current authoritative session references.
func ExecuteCleanupLocked(imagesDir, logsDir string, approved CleanupPlan) ([]VersionGCOutcome, error) {
	return executeCleanupLocked(imagesDir, logsDir, approved, hooks{})
}

func executeCleanupLocked(imagesDir, logsDir string, approved CleanupPlan, seam hooks) ([]VersionGCOutcome, error) {
	current, err := InspectCleanupLocked(imagesDir)
	if err != nil {
		return nil, err
	}
	if approved.Pending != current.Pending {
		return cleanupOutcomes(approved.Items, "changed", nil), nil
	}
	if approved.Pending {
		if approved.journal == nil || current.journal == nil ||
			!bytes.Equal(approved.journalBody, current.journalBody) ||
			!os.SameFile(approved.journalInfo, current.journalInfo) ||
			!sameCleanupTree(approved.repoTree, current.repoTree) {
			return cleanupOutcomes(approved.Items, "changed", nil), nil
		}
		err = replayVersionRemoval(imagesDir, *current.journal, seam)
		outcomes := cleanupOutcomes(approved.Items, "", err)
		if err != nil {
			for i, item := range approved.Items {
				repo, openErr := OpenRepo(imagesDir, item.Name)
				path, pathErr := repo.ObjectDirPath(item.ID)
				_, statErr := os.Lstat(path)
				if openErr == nil && pathErr == nil && errors.Is(statErr, os.ErrNotExist) && gcVersionAbsent(repo, item.ID) {
					outcomes[i].Action, outcomes[i].Reason, outcomes[i].Detail = GCActionRemoved, "", ""
				}
			}
		}
		return outcomes, err
	}
	var outcomes []VersionGCOutcome
	var failures error
	for _, item := range approved.Items {
		match := false
		for _, candidate := range current.Items {
			if item.Name == candidate.Name && item.ID == candidate.ID && item.Kind == candidate.Kind {
				match = sameCleanupTree(item.tree, candidate.tree)
			}
		}
		if !match {
			outcomes = append(outcomes, cleanupOutcomes([]CleanupItem{item}, "changed", nil)...)
			continue
		}
		repo, runErr := OpenRepo(imagesDir, item.Name)
		if runErr != nil {
			return outcomes, runErr
		}
		path, runErr := repo.ObjectDirPath(item.ID)
		if runErr == nil && seam.before != nil {
			runErr = seam.before(path, "")
		}
		if runErr == nil && strings.TrimSpace(logsDir) != "" {
			runErr = removeVersionLogs(logsDir, repo.Segments, []string{item.ID})
		}
		if runErr == nil {
			runErr = os.RemoveAll(path)
		}
		outcomes = append(outcomes, cleanupOutcomes([]CleanupItem{item}, "", runErr)...)
		failures = errors.Join(failures, runErr)
	}
	return outcomes, failures
}

func cleanupOutcomes(items []CleanupItem, reason string, failure error) []VersionGCOutcome {
	outcomes := make([]VersionGCOutcome, 0, len(items))
	for _, item := range items {
		event := VersionGCOutcome{Name: item.Name, ID: item.ID, Action: GCActionRemoved}
		if reason != "" {
			event.Action, event.Reason = "skipped", reason
		} else if failure != nil {
			event.Action, event.Reason, event.Detail = "skipped", "failed", failure.Error()
		}
		outcomes = append(outcomes, event)
	}
	return outcomes
}
