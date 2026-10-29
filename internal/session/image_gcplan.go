package session

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
	"github.com/jiangxin/tiler/internal/image"
)

// PlanImages inspects image expiry without replaying image or session deletion.
// Only missing activity initialization may change a version; all locks are
// released before the caller presents this snapshot for confirmation.
func (c ReferenceCoordinator) PlanImages(cutoff time.Time, clock activity.Clock) (plans []image.VersionGCPlan, err error) {
	if cutoff.IsZero() {
		return nil, errors.New("image gc: zero cutoff")
	}
	err = c.withLibrary(func() error {
		if err := image.CheckVersionGCReadyLocked(c.ImagesDir); err != nil {
			return err
		}
		refs, err := c.gcReferencesLocked()
		if err != nil {
			return err
		}
		repos, err := image.ListRepos(c.ImagesDir)
		if err != nil {
			return err
		}
		for _, repo := range repos {
			plan, err := image.PlanVersionGCLocked(repo, refs[repo.Name()], cutoff, clock)
			if err != nil {
				return fmt.Errorf("image gc: %s: %w", repo.Name(), err)
			}
			plans = append(plans, plan)
		}
		return nil
	})
	return plans, err
}

// gcReferencesLocked derives pins from validated sources without trusting or
// rebuilding indexes. Pending or inconsistent recovery must be handled by its
// owning lifecycle command, never as a hidden side effect of a GC preview.
func (c ReferenceCoordinator) gcReferencesLocked() (map[string]map[string]bool, error) {
	if _, err := c.LoadReferenceJournal(); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			err = errors.New("session reference transaction requires recovery")
		}
		return nil, err
	}
	state, err := c.LoadReferenceState()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if state.Dirty {
		return nil, errors.New("session reference state requires recovery")
	}
	sources, err := c.referenceSources()
	if err != nil {
		return nil, err
	}
	listed, err := c.names()
	if err != nil {
		return nil, err
	}
	for _, name := range listed {
		if ValidateName(name) == nil && sources[name] == nil {
			return nil, fmt.Errorf("session reference source is missing: %s", name)
		}
	}
	for name := range state.Inventory {
		if sources[name] == nil {
			return nil, fmt.Errorf("session reference source is missing: %s", name)
		}
	}
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	slices.Sort(names)
	refs := make(map[string]map[string]bool)
	for _, name := range names {
		if err := c.withSession(name, func() error {
			rec, err := c.loadReferenceSource(name)
			if err != nil {
				return err
			}
			if rec == nil {
				return fmt.Errorf("session reference source disappeared: %s", name)
			}
			if err := c.validateGCVersionStacks(rec); err != nil {
				return err
			}
			for key := range recordReferences(rec) {
				if refs[key.name] == nil {
					refs[key.name] = make(map[string]bool)
				}
				refs[key.name][key.tip] = true
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

// validateGCVersionStacks checks both recorded identities before their tips
// can protect versions or an expiry scan can initialize any image activity.
func (c ReferenceCoordinator) validateGCVersionStacks(rec *Record) error {
	for _, action := range rec.Actions {
		if action.Type != TypeMount || action.Name == "" {
			continue
		}
		for _, served := range []bool{false, true} {
			if served && action.ServedTip == "" {
				continue
			}
			if _, err := action.VersionStack(c.ImagesDir, served); err != nil {
				return fmt.Errorf("session %s: image gc reference: %w", rec.Name, err)
			}
		}
	}
	return nil
}
