package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jiangxin/tiler/internal/config"
)

const (
	// OutcomeDeprecated is the first rm stage: the target meta now
	// carries the deprecated flag.
	OutcomeDeprecated = "deprecated"
	// OutcomeRemoved is the second rm stage: the target is gone.
	OutcomeRemoved = "removed"
)

// rmTarget is one parsed rm target. The archive field is empty for
// the whole-repository target.
type rmTarget struct {
	repo    Repo
	archive string
	dir     string
}

// RemoveTarget runs the two-phase rm and reports which phase ran.
// The first call flags the target deprecated; the second physically
// removes it. Both calls refuse while a reference pins the target.
func RemoveTarget(cfg config.Config, target string, refs Refs) (string, error) {
	rt, meta, err := resolveRemoveTarget(cfg, target)
	if err != nil {
		return "", err
	}
	if err := rt.rejectReferenced(refs); err != nil {
		return "", err
	}
	if !meta.Deprecated {
		if err := rt.markDeprecated(meta); err != nil {
			return "", err
		}
		return OutcomeDeprecated, nil
	}
	if err := rt.delete(); err != nil {
		return "", err
	}
	return OutcomeRemoved, nil
}

// resolveRemoveTarget parses the two accepted target shapes,
// owner/repo and owner/repo/archives/<full-id>, and loads the meta
// that carries the deprecated flag.
func resolveRemoveTarget(cfg config.Config, target string) (rmTarget, Meta, error) {
	parts := strings.Split(target, "/")
	var (
		rt  rmTarget
		err error
	)
	switch len(parts) {
	case 2:
		rt.repo, err = OpenRepo(cfg.ImagesDir, target)
		rt.dir = rt.repo.Dir
	case 4:
		if parts[2] != dirArchives {
			return rmTarget{}, Meta{}, fmt.Errorf("remove: invalid target %q", target)
		}
		if err = checkID(parts[3]); err != nil {
			return rmTarget{}, Meta{}, fmt.Errorf("remove: invalid target %q", target)
		}
		rt.repo, err = OpenRepo(cfg.ImagesDir, parts[0]+"/"+parts[1])
		if err == nil {
			rt.archive = parts[3]
			rt.dir = filepath.Join(rt.repo.Dir, dirArchives, parts[3])
		}
	default:
		err = fmt.Errorf("remove: invalid target %q", target)
	}
	if err != nil {
		return rmTarget{}, Meta{}, err
	}
	meta, err := Load(filepath.Join(rt.dir, fileMeta))
	if err != nil {
		return rmTarget{}, Meta{}, fmt.Errorf("remove: %w", err)
	}
	return rt, meta, nil
}

// rejectReferenced refuses both rm stages while any reference pins
// the target.
func (t rmTarget) rejectReferenced(refs Refs) error {
	referenced, err := t.referenced(refs)
	if err != nil {
		return err
	}
	if referenced {
		return errors.New("remove: target is still referenced; unmount it or wait for garbage collection")
	}
	return nil
}

// referenced reports the pins for the target shape. An archive is
// held by a session reference or by the parent link of another live
// chain. The whole repository is held when a reference pins its
// active image, one of its holds, or one of its archives.
func (t rmTarget) referenced(refs Refs) (bool, error) {
	if t.archive != "" {
		return chainReferenced(t.repo, t.dir, t.archive, refs, "")
	}
	ids, err := t.repoFullIDs()
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		held, err := heldBy(refs, refFor(t.repo, id))
		if err != nil || held {
			return held, err
		}
	}
	return false, nil
}

// repoFullIDs lists every full id the whole-repository delete would
// take with it: the active image, the holds, and the archives.
func (t rmTarget) repoFullIDs() ([]string, error) {
	var ids []string
	active, exists, err := loadActive(t.repo)
	if err != nil {
		return nil, err
	}
	if exists {
		ids = append(ids, active.ID)
	}
	holdIDs, err := listHoldIDs(t.repo)
	if err != nil {
		return nil, err
	}
	ids = append(ids, holdIDs...)
	entries, err := os.ReadDir(filepath.Join(t.repo.Dir, dirArchives))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() && checkID(entry.Name()) == nil {
			ids = append(ids, entry.Name())
		}
	}
	return ids, nil
}

// markDeprecated writes the flag back through a temporary file and
// a rename, so a reader never sees a half-written meta.
func (t rmTarget) markDeprecated(meta Meta) error {
	meta.Deprecated = true
	body, err := Encode(meta)
	if err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	tmp, err := writeTemp(t.dir, fileMeta, body, hooks{})
	if err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	seam := hooks{}
	if err := seam.do(tmp, filepath.Join(t.dir, fileMeta)); err != nil {
		removeFile(tmp)
		return fmt.Errorf("remove: %w", err)
	}
	return nil
}

// delete removes the target scope. The whole-repository target
// takes incr/, hold/, and archives/ with it; the archive target
// only takes that one archive directory.
func (t rmTarget) delete() error {
	if t.archive != "" {
		if err := os.RemoveAll(t.dir); err != nil {
			return fmt.Errorf("remove: %w", err)
		}
		removeEmpty(filepath.Dir(t.dir))
		return nil
	}
	if err := os.RemoveAll(t.repo.Dir); err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	removeEmpty(filepath.Dir(t.repo.Dir))
	return nil
}
