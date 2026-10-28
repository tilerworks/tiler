package session

import (
	"fmt"

	"github.com/jiangxin/tiler/internal/image"
)

// VersionRemovalPlan is a child-before-parent deletion set and every trusted
// reference bucket that blocks deleting it.
type VersionRemovalPlan struct {
	Images   []image.ImageMeta
	Blockers []ReferenceBucket
}

// PlanVersionRemoval verifies the reference inventory while holding the
// library lock. The returned snapshot cannot authorize a later deletion;
// deletion callers must use PlanVersionRemovalLocked inside their mutation.
func (c ReferenceCoordinator) PlanVersionRemoval(
	name string, images []image.ImageMeta, root string,
) (plan VersionRemovalPlan, err error) {
	err = c.withLibrary(func() error {
		plan, err = c.PlanVersionRemovalLocked(name, images, root)
		return err
	})
	return plan, err
}

// PlanVersionRemovalLocked requires the shared library lock to remain held
// through the deletion authorized by this plan.
func (c ReferenceCoordinator) PlanVersionRemovalLocked(
	name string, images []image.ImageMeta, root string,
) (VersionRemovalPlan, error) {
	forest, err := image.NewForest(images)
	if err != nil {
		return VersionRemovalPlan{}, err
	}
	nodes, err := forest.Descendants(root)
	if err != nil {
		return VersionRemovalPlan{}, err
	}
	targets := make([]ReferenceTarget, len(nodes))
	for i, node := range nodes {
		targets[i] = ReferenceTarget{Name: name, Tip: node.ID}
	}
	buckets, err := c.ReferencesLocked(targets...)
	if err != nil {
		return VersionRemovalPlan{}, err
	}
	blockers := buckets[:0]
	for _, bucket := range buckets {
		if len(bucket.Owners) != 0 {
			blockers = append(blockers, bucket)
		}
	}
	return VersionRemovalPlan{Images: nodes, Blockers: blockers}, nil
}

// RemoveVersionSubtree checks references and commits deletion while retaining
// the shared library lock, excluding concurrent reference creation.
func (c ReferenceCoordinator) RemoveVersionSubtree(name, root string) ([]image.ImageMeta, error) {
	var removed []image.ImageMeta
	err := c.withLibrary(func() error {
		repo, err := image.OpenRepo(c.ImagesDir, name)
		if err != nil {
			return err
		}
		manifest, err := image.LoadVersionManifest(repo)
		if err != nil {
			return err
		}
		plan, err := c.PlanVersionRemovalLocked(name, manifest.Images, root)
		if err != nil {
			return err
		}
		if len(plan.Blockers) != 0 {
			return fmt.Errorf("image %s: versions are referenced by sessions", name)
		}
		removed, err = image.RemoveVersionSubtreeLocked(c.ImagesDir, name, root)
		return err
	})
	return removed, err
}
