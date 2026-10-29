package image

import (
	"errors"
	"fmt"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
)

// VersionGCCandidate binds an expired version to its complete metadata and
// activity revision. A later execution must revalidate both under the lock.
type VersionGCCandidate struct {
	Meta     ImageMeta
	Activity activity.Revision
}

// VersionGCSkip explains a version retained during initialization or inspection.
type VersionGCSkip struct {
	ID, Reason, Detail string
}

// VersionGCPlan lists a repository's expired versions child before parent.
// It never authorizes deletion without a fresh, locked revalidation.
type VersionGCPlan struct {
	Name       string
	Cutoff     time.Time
	Candidates []VersionGCCandidate
	Skips      []VersionGCSkip
}

// CheckVersionGCReadyLocked refuses pending cleanup without replaying it.
// The caller holds the image locks for this library; confirmed legacy
// cleanup is separate.
func CheckVersionGCReadyLocked(imagesDir string) error {
	paths, err := removalJournalPaths(imagesDir)
	if err != nil {
		return err
	}
	if len(paths) != 0 {
		return errors.New("pending image removal requires confirmed cleanup")
	}
	return nil
}

// PlanVersionGCLocked initializes missing activity before taking any expiry
// snapshots, then preserves the ancestors of every retained version. Pinned
// IDs come from authoritative session records under the same library lock.
func PlanVersionGCLocked(repo Repo, pinned map[string]bool, cutoff time.Time, clock activity.Clock) (VersionGCPlan, error) {
	clock = (versionActivityBatch{clock: clock}).resolvedClock()
	return versionGCPlanner{repo, cutoff.UTC(), clock, nil}.plan(pinned)
}

type versionGCPlanner struct {
	repo   Repo
	cutoff time.Time
	clock  activity.Clock
	ensure func(string) ([]string, error)
}

func (p versionGCPlanner) plan(pinned map[string]bool) (VersionGCPlan, error) {
	plan := VersionGCPlan{Name: p.repo.Name(), Cutoff: p.cutoff}
	if p.cutoff.IsZero() {
		return plan, errors.New("image: gc plan: zero cutoff")
	}
	manifest, err := LoadVersionManifest(p.repo)
	if err != nil {
		return plan, err
	}
	forest, err := NewForest(manifest.Images)
	if err != nil {
		return plan, err
	}
	for id, held := range pinned {
		if _, err := forest.Stack(id); held && err != nil {
			return plan, fmt.Errorf("referenced image version: %w", err)
		}
	}
	touched, failures := p.initialize(manifest.Images)
	keep := make(map[string]bool)
	revisions := make(map[string]activity.Revision)
	for _, node := range manifest.Images {
		w, err := (versionActivityBatch{repo: p.repo, clock: p.clock}).writer(node.ID, p.clock)
		if err == nil {
			revisions[node.ID], err = w.Revision()
		}
		if err != nil {
			failures[node.ID] = errors.Join(failures[node.ID], err)
		}
		keep[node.ID] = pinned[node.ID] || touched[node.ID] || failures[node.ID] != nil ||
			!revisions[node.ID].Last.Before(p.cutoff)
	}
	nodes := forest.Topology()
	for i := len(nodes) - 1; i >= 0; i-- {
		node := nodes[i]
		if keep[node.ID] {
			keep[node.Parent] = true
		} else {
			plan.Candidates = append(plan.Candidates, VersionGCCandidate{node, revisions[node.ID]})
		}
		if err := failures[node.ID]; err != nil {
			plan.Skips = append(plan.Skips, VersionGCSkip{node.ID, "unreadable", err.Error()})
		} else if touched[node.ID] {
			plan.Skips = append(plan.Skips, VersionGCSkip{node.ID, "initialized", "activity refreshed by this scan"})
		}
	}
	return plan, nil
}

// initialize finishes every attempted propagation before snapshots are read.
// Failed sources and partially refreshed ancestors remain conservatively kept.
func (p versionGCPlanner) initialize(nodes []ImageMeta) (map[string]bool, map[string]error) {
	touched, failures := make(map[string]bool), make(map[string]error)
	for _, node := range nodes {
		ensure := p.ensure
		if ensure == nil {
			ensure = func(id string) ([]string, error) { return EnsureVersionActivityLocked(p.repo, id, p.clock) }
		}
		ids, err := ensure(node.ID)
		for _, id := range ids {
			touched[id] = true
		}
		if err != nil {
			failures[node.ID] = fmt.Errorf("image %s:%s: %w", p.repo.Name(), node.ID, err)
		}
	}
	return touched, failures
}
