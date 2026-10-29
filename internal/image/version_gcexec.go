package image

import (
	"errors"
	"os"
	"reflect"

	"github.com/jiangxin/tiler/internal/activity"
)

// VersionGCOutcome reports what happened to one approved complete version ID.
// Removed means metadata and the object are gone; skipped carries its reason.
type VersionGCOutcome struct {
	Name, ID, Action, Reason, Detail string
}

// ExecuteVersionGCLocked rechecks the approved snapshot and removes only its
// current safe subset. Every unapproved or changed node retains its ancestors.
// The caller holds the library lock and supplies current authoritative pins.
func ExecuteVersionGCLocked(repo Repo, approved VersionGCPlan, pins map[string]bool, clock activity.Clock) ([]VersionGCOutcome, error) {
	return executeVersionGC(repo, approved, pins, clock, hooks{})
}

func executeVersionGC(repo Repo, approved VersionGCPlan, pins map[string]bool, clock activity.Clock, seam hooks) ([]VersionGCOutcome, error) {
	if approved.Name != repo.Name() || approved.Cutoff.IsZero() {
		return nil, errors.New("image gc: invalid approval identity or cutoff")
	}
	if err := CheckVersionGCReadyLocked(libraryRoot(repo)); err != nil {
		return nil, err
	}
	manifest, err := LoadVersionManifest(repo)
	if errors.Is(err, os.ErrNotExist) {
		return gcOutcomes(repo, approved, nil, nil, nil), nil
	}
	if err != nil {
		return nil, err
	}
	current, err := PlanVersionGCLocked(repo, pins, approved.Cutoff, clock)
	if err != nil {
		return nil, err
	}
	original := make(map[string]VersionGCCandidate, len(approved.Candidates))
	for _, node := range approved.Candidates {
		if _, duplicate := original[node.Meta.ID]; duplicate {
			return nil, errors.New("image gc: duplicate approved version")
		}
		original[node.Meta.ID] = node
	}
	selected := make(map[string]bool)
	for _, node := range current.Candidates {
		old, ok := original[node.Meta.ID]
		selected[node.Meta.ID] = ok && reflect.DeepEqual(old.Meta, node.Meta) && old.Activity == node.Activity
	}
	forest, _ := NewForest(manifest.Images)
	nodes := forest.Topology()
	var ids []string
	for i := len(nodes) - 1; i >= 0; i-- {
		if node := nodes[i]; !selected[node.ID] {
			selected[node.Parent] = false
		} else {
			ids = append(ids, node.ID)
		}
	}
	if len(ids) != 0 {
		err = removeVersionSetLocked(repo, ids, seam)
	}
	return gcOutcomes(repo, approved, manifest.Images, selected, err), err
}

func gcOutcomes(repo Repo, approved VersionGCPlan, current []ImageMeta, selected map[string]bool, failure error) []VersionGCOutcome {
	present := make(map[string]bool, len(current))
	for _, node := range current {
		present[node.ID] = true
	}
	var outcomes []VersionGCOutcome
	for _, node := range approved.Candidates {
		id := node.Meta.ID
		outcome := VersionGCOutcome{Name: repo.Name(), ID: id, Action: "skipped", Reason: "changed"}
		switch {
		case !present[id]:
			outcome.Reason = "gone"
		case selected[id] && failure == nil:
			outcome.Action, outcome.Reason = GCActionRemoved, ""
		case selected[id]:
			outcome.Reason, outcome.Detail = "failed", failure.Error()
			// Cleanup may fail after removing earlier children. Report a
			// completed object only when its committed metadata is gone too.
			path, pathErr := repo.ObjectDirPath(id)
			_, statErr := os.Lstat(path)
			if pathErr == nil && errors.Is(statErr, os.ErrNotExist) && gcVersionAbsent(repo, id) {
				outcome.Action, outcome.Reason, outcome.Detail = GCActionRemoved, "", ""
			}
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}

func gcVersionAbsent(repo Repo, id string) bool {
	manifest, err := LoadVersionManifest(repo)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	for _, node := range manifest.Images {
		if node.ID == id {
			return false
		}
	}
	return true
}
