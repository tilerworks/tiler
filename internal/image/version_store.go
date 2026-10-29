package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/jiangxin/tiler/internal/activity"
)

// appendFullVersion commits metadata after its caller has placed the payload.
// The caller must hold the library lock; existing versions are never rewritten.
func appendFullVersion(repo Repo, node ImageMeta, descriptionSet bool, seam hooks) (ImageMeta, error) {
	if node.Kind != KindFull {
		return ImageMeta{}, errors.New("version publish: full image required")
	}
	return appendVersion(repo, node, descriptionSet, seam)
}

// appendVersion commits one immutable version at the shared publication
// boundary every payload path reaches: plain file, split, external
// link, DMG bake, and the caller-locked entry points. The caller must
// hold the library lock; published versions are never rewritten.
//
// Each publication phase is recorded through the parent-chain activity
// API. An incremental version cannot name itself before it exists, so
// the phase that reads its parent chain is sourced at the parent; the
// creation itself is recorded from the published version afterwards.
// A reuse of an identical version records the use instead, because no
// creation happened.
func appendVersion(repo Repo, node ImageMeta, descriptionSet bool, seam hooks) (ImageMeta, error) {
	var err error
	if node.Description, err = NormalizeDescription(node.Description); err != nil {
		return ImageMeta{}, err
	}
	manifest, previous, err := loadVersionMeta(repo)
	if err != nil {
		return ImageMeta{}, err
	}
	if node.placeAfter {
		node.CreatedAt = createdAfterExisting(manifest.Images, node.CreatedAt)
		node.placeAfter = false
	}
	if err := versionWithNode(manifest, node).Validate(); err != nil {
		return ImageMeta{}, err
	}
	for _, old := range manifest.Images {
		if old.ID != node.ID {
			continue
		}
		if !sameVersionIdentity(old, node) {
			return ImageMeta{}, fmt.Errorf("version publish: identity conflict for %s", node.ID)
		}
		if descriptionSet && old.Description != node.Description {
			return ImageMeta{}, fmt.Errorf("version publish: use images describe %s:%s to change description", repo.Name(), node.ID)
		}
		if err := recordVersionReuse(repo, old); err != nil {
			return ImageMeta{}, err
		}
		return old, nil
	}
	if node.Kind == KindIncremental {
		// The new version has no identity in the manifest yet, so the
		// consumed parent is the source of the phase that reads its
		// chain. Recording it first keeps the ancestry active even if
		// the publication stops after this point.
		if err := AppendVersionActivityLocked(repo, node.Parent, activity.Event{
			Event: activity.EventImageCreateIncremental,
			Phase: activity.PhaseStarted,
		}, nil); err != nil {
			return ImageMeta{}, err
		}
	}
	manifest.Images = append(manifest.Images, node)
	body, err := EncodeVersionManifest(manifest)
	if err != nil {
		return ImageMeta{}, err
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		return ImageMeta{}, err
	}
	if err := replaceVersionMeta(repo, previous, body, seam); err != nil {
		return ImageMeta{}, err
	}
	// The version is published at this point. A creation record that
	// cannot be written is reported with the published version: the
	// manifest stays authoritative, and a retried publication reuses the
	// version and records that use.
	if err := AppendVersionActivityLocked(repo, node.ID, activity.Event{
		Event: versionCreationEvent(node),
		Phase: activity.PhaseSucceeded,
	}, nil); err != nil {
		return node, err
	}
	return node, nil
}

func versionWithNode(manifest VersionManifest, node ImageMeta) VersionManifest {
	manifest.Images = slices.Clone(manifest.Images)
	for i, old := range manifest.Images {
		if old.ID == node.ID {
			manifest.Images[i] = node
			return manifest
		}
	}
	manifest.Images = append(manifest.Images, node)
	return manifest
}

// Builder, timestamps and description describe publication, not payload identity.
func sameVersionIdentity(a, b ImageMeta) bool {
	return a.Kind == b.Kind && a.Parent == b.Parent && a.Format == b.Format &&
		a.Size == b.Size && a.SHA256 == b.SHA256 && a.ContentSHA256 == b.ContentSHA256 &&
		slices.EqualFunc(a.Blocks, b.Blocks, func(a, b BlockEntry) bool {
			return a.Name == b.Name && a.Size == b.Size && a.SHA256 == b.SHA256
		})
}

func loadVersionMeta(repo Repo) (VersionManifest, []byte, error) {
	if err := repo.checkObjectComponents(repo.MetaPath(), false); err != nil {
		return VersionManifest{}, nil, err
	}
	for _, name := range []string{ImageFile, ImageDMG, dirIncr, dirBlocks} {
		if _, err := os.Lstat(filepath.Join(repo.Dir, name)); !errors.Is(err, os.ErrNotExist) {
			return VersionManifest{}, nil, fmt.Errorf("version publish: legacy layout or unreadable marker %s", name)
		}
	}
	body, err := os.ReadFile(repo.MetaPath())
	if errors.Is(err, os.ErrNotExist) {
		return VersionManifest{Version: VersionManifestVersion, Name: repo.Name()}, nil, nil
	}
	if err != nil {
		return VersionManifest{}, nil, err
	}
	manifest, err := DecodeVersionManifest(body)
	if err == nil && manifest.Name != repo.Name() {
		err = errors.New("version publish: metadata name does not match repository")
	}
	return manifest, body, err
}

func replaceVersionMeta(repo Repo, previous, body []byte, seam hooks) error {
	tmp, err := writeTemp(repo.Dir, fileMeta, body, hooks{})
	if err != nil {
		return err
	}
	defer removeFile(tmp)
	if err := seam.do(tmp, repo.MetaPath()); err != nil {
		if _, statErr := os.Lstat(tmp); errors.Is(statErr, os.ErrNotExist) {
			return errors.Join(err, restoreVersionMeta(repo, previous))
		}
		return err
	}
	return nil
}

func restoreVersionMeta(repo Repo, previous []byte) error {
	if previous == nil {
		return errors.Join(os.Remove(repo.MetaPath()), syncDir(repo.Dir, hooks{}))
	}
	tmp, err := writeTemp(repo.Dir, fileMeta, previous, hooks{})
	if err != nil {
		return err
	}
	defer removeFile(tmp)
	return (hooks{}).do(tmp, repo.MetaPath())
}
