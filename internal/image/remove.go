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

// Attached reports whether dir holds a disk image that is currently
// attached. dir is the repository directory or one archive directory.
// A nil Attached does not consult a disk-image table.
type Attached func(dir string) (bool, error)

// RemoveTarget runs the two-phase rm and reports which phase ran.
// The first call flags the target deprecated; the second physically
// removes it. Both calls refuse while a reference pins the target.
func RemoveTarget(cfg config.Config, target string, refs Refs) (string, error) {
	return RemoveChecked(cfg, target, refs, nil)
}

// RemoveChecked is RemoveTarget plus an attach check. A dmg target
// is refused while attached reports the target directory. Other
// formats do not call attached. Image files and archives are the
// only paths this removes.
func RemoveChecked(cfg config.Config, target string, refs Refs, attached Attached) (string, error) {
	rt, meta, err := resolveRemoveTarget(cfg, target)
	if err != nil {
		return "", err
	}
	if err := rt.rejectReferenced(refs); err != nil {
		return "", err
	}
	if err := rt.rejectAttached(meta, attached); err != nil {
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

// resolveRemoveTarget parses the two accepted target shapes: a
// 1-to-5 segment library name, or <name>/_archives/<id> where id is
// a full sha256 or a unique prefix, and loads the meta that carries
// the deprecated flag.
func resolveRemoveTarget(cfg config.Config, target string) (rmTarget, Meta, error) {
	parts := strings.Split(cleanImageName(target), "/")
	archIdx := -1
	for i, p := range parts {
		if p == dirArchives {
			archIdx = i
			break
		}
	}
	var (
		rt  rmTarget
		err error
	)
	switch {
	case archIdx < 0:
		rt.repo, err = OpenRepo(cfg.ImagesDir, target)
		rt.dir = rt.repo.Dir
	case archIdx == len(parts)-2:
		ref := parts[len(parts)-1]
		nameParts := parts[:archIdx]
		rt.repo, err = OpenRepo(cfg.ImagesDir, strings.Join(nameParts, "/"))
		if err != nil {
			break
		}
		var id string
		id, err = resolveArchiveRef(rt.repo, ref)
		if errors.Is(err, ErrInvalidPath) {
			err = invalidTarget(target)
			break
		}
		if err != nil {
			err = fmt.Errorf("remove: %w", err)
			break
		}
		rt.archive = id
		rt.dir, err = rt.repo.ArchivePath(id)
	default:
		err = invalidTarget(target)
	}
	if err != nil {
		if errors.Is(err, ErrInvalidName) || errors.Is(err, ErrInvalidPath) {
			return rmTarget{}, Meta{}, invalidTarget(target)
		}
		return rmTarget{}, Meta{}, err
	}
	meta, err := Load(filepath.Join(rt.dir, fileMeta))
	if err != nil {
		return rmTarget{}, Meta{}, fmt.Errorf("remove: %w", err)
	}
	return rt, meta, nil
}

// resolveArchiveRef matches ref against archive directory names and
// returns the stored id. ref may be the full sha256 or a unique
// prefix. A ref that is not lowercase hex is ErrInvalidPath.
func resolveArchiveRef(repo Repo, ref string) (string, error) {
	ids, err := chainDirIDs(filepath.Join(repo.Dir, dirArchives))
	if err != nil {
		return "", err
	}
	return resolveID(ids, ref)
}

// invalidTarget reports a target that is neither a library name nor
// <name>/_archives/<id>. It wraps ErrInvalidName so command entries
// classify the refusal as a usage error.
func invalidTarget(target string) error {
	return fmt.Errorf("remove: invalid target %q: %w", target, ErrInvalidName)
}

// rejectReferenced refuses both rm stages while any reference pins
// the target. A session pin names the session and the command that
// releases it; a chain parent link names the shape instead, since
// no session owns it.
func (t rmTarget) rejectReferenced(refs Refs) error {
	referenced, err := t.referenced(refs)
	if err != nil {
		return err
	}
	if !referenced {
		return nil
	}
	name, held, err := t.sessionHolder(refs)
	if err != nil {
		return err
	}
	if held {
		return fmt.Errorf(
			"remove: target is still referenced by session %s; "+
				"run 'tiler session close %s' to release it "+
				"(or 'tiler images gc --session-max-age ...' to age the session out)",
			name, name)
	}
	return errors.New(
		"remove: target is still referenced by another live chain; " +
			"remove that chain first")
}

// rejectAttached refuses both rm stages while a dmg in the target
// directory is attached. A nil checker or a non-dmg meta skips the
// question. The checker sees the repository or archive directory,
// never a session runtime.
func (t rmTarget) rejectAttached(meta Meta, attached Attached) error {
	if attached == nil || meta.Format.Type != FormatDMG {
		return nil
	}
	hit, err := attached(t.dir)
	if err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	if !hit {
		return nil
	}
	return errors.New("remove: target is still attached; detach it before removing")
}

// sessionHolder names one active session pinning any full image the
// target would take with it, mirroring the ids referenced checks.
func (t rmTarget) sessionHolder(refs Refs) (string, bool, error) {
	if refs == nil {
		return "", false, nil
	}
	ids := []string{t.archive}
	if t.archive == "" {
		var err error
		if ids, err = t.repoFullIDs(); err != nil {
			return "", false, err
		}
	}
	for _, id := range ids {
		name, held, err := refs.Holder(refFor(t.repo, id))
		if err != nil || held {
			return name, held, err
		}
	}
	return "", false, nil
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
	archIDs, err := chainDirIDs(filepath.Join(t.repo.Dir, dirArchives))
	if err != nil {
		return nil, err
	}
	ids = append(ids, archIDs...)
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
// takes _incr/, _hold/, and _archives/ with it; the archive target
// only takes that one archive directory. After a whole-repository
// delete, empty ancestors under the library root are pruned, but
// the library root itself is never removed.
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
	imagesDir := t.repo.Dir
	for range t.repo.Segments {
		imagesDir = filepath.Dir(imagesDir)
	}
	removeEmptyAncestors(filepath.Dir(t.repo.Dir), imagesDir)
	return nil
}

// removeEmptyAncestors removes empty directories walking from dir up
// toward, but not including, stop (typically the library root).
func removeEmptyAncestors(dir, stop string) {
	dir = filepath.Clean(dir)
	stop = filepath.Clean(stop)
	for dir != stop {
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = parent
	}
}
