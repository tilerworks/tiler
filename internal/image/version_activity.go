package image

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
)

// AppendVersionActivityLocked records one logical activity event for
// sourceID and its complete parent chain. The caller must hold the
// library lock: the batch validates the chain and every target tail
// before the first write, and the writes only ever hold one activity
// log lock at a time.
//
// The source version records the event itself. Each ancestor records
// an image.dependency.activity line naming the source version and the
// source event, so a dependency use never reads like a direct
// creation, mount, or unmount of that ancestor. Ancestor records are
// written by this batch directly and never reenter propagation.
//
// One timestamp covers the batch: the later of the injected clock and
// every target's newest record, so the chain shares one event instant
// and no log's time moves back. The writes run root to source, so a
// successful source append implies every ancestor record is already
// synced. A failure stops the batch, keeps the records written so
// far, and names the version and the phase.
func AppendVersionActivityLocked(repo Repo, sourceID string, ev activity.Event, clock activity.Clock) error {
	return versionActivityBatch{repo: repo, clock: clock}.append(sourceID, ev)
}

// versionActivityBatch writes one logical event along a parent chain.
// Tests replace open to inject a failure in the middle of a batch.
type versionActivityBatch struct {
	repo  Repo
	clock activity.Clock
	open  func(repo Repo, id string, clock activity.Clock) (*activity.Writer, error)
}

func (b versionActivityBatch) append(sourceID string, ev activity.Event) error {
	if err := ev.Validate(); err != nil {
		return fmt.Errorf("image %s: activity %w", b.repo.Name(), err)
	}
	chain, err := b.chain(sourceID)
	if err != nil {
		return fmt.Errorf("image %s: activity %s: %w", b.repo.Name(), ev.Event, err)
	}
	at, err := b.batchTime(chain)
	if err != nil {
		return err
	}
	for _, node := range chain {
		if err := b.write(node.ID, at, versionActivityRecord(ev, b.repo.Name(), sourceID, node.ID)); err != nil {
			return err
		}
	}
	return nil
}

// write appends one record of the batch to one version log.
func (b versionActivityBatch) write(id string, at time.Time, ev activity.Event) error {
	w, err := b.writer(id, func() time.Time { return at })
	if err == nil {
		_, err = w.Append(ev)
	}
	if err != nil {
		return fmt.Errorf("image %s: activity %s %s at version %s: %w",
			b.repo.Name(), ev.Event, ev.Phase, id, err)
	}
	return nil
}

// chain returns the root-to-source stack of one version, which is the
// propagation set of a single event.
func (b versionActivityBatch) chain(sourceID string) ([]ImageMeta, error) {
	manifest, err := LoadVersionManifest(b.repo)
	if err != nil {
		return nil, err
	}
	forest, err := NewForest(manifest.Images)
	if err != nil {
		return nil, err
	}
	return forest.Stack(sourceID)
}

// batchTime returns the one timestamp every record of the batch
// carries. Reading the tails first proves every target can be read and
// keeps a future ancestor time from moving back.
func (b versionActivityBatch) batchTime(chain []ImageMeta) (time.Time, error) {
	clock := b.resolvedClock()
	at := clock().UTC()
	for _, node := range chain {
		w, err := b.writer(node.ID, clock)
		if err != nil {
			return time.Time{}, fmt.Errorf("image %s: activity tail of version %s: %w",
				b.repo.Name(), node.ID, err)
		}
		last, err := w.LastActivity()
		switch {
		case err == nil:
			if last.After(at) {
				at = last
			}
		case errors.Is(err, activity.ErrNoActivity):
		default:
			return time.Time{}, fmt.Errorf("image %s: activity tail of version %s: %w",
				b.repo.Name(), node.ID, err)
		}
	}
	return at, nil
}

// resolvedClock returns the batch clock; nil means the UTC wall clock.
func (b versionActivityBatch) resolvedClock() activity.Clock {
	if b.clock != nil {
		return b.clock
	}
	return func() time.Time { return time.Now().UTC() }
}

// writer opens the activity log of one version object directory.
func (b versionActivityBatch) writer(id string, clock activity.Clock) (*activity.Writer, error) {
	if b.open != nil {
		return b.open(b.repo, id, clock)
	}
	dir, err := b.repo.ObjectDirPath(id)
	if err != nil {
		return nil, err
	}
	return activity.OpenLog(libraryRoot(b.repo), dir, clock)
}

// versionActivityRecord renders the record one version receives.
func versionActivityRecord(ev activity.Event, image, source, target string) activity.Event {
	record := ev
	record.Version = target
	if record.Image == "" {
		record.Image = image
	}
	if target == source {
		return record
	}
	record.Event = activity.EventImageDependency
	record.SourceVersion = source
	record.SourceEvent = ev.Event
	return record
}

// libraryRoot returns the images root that holds repo.
func libraryRoot(repo Repo) string {
	root := filepath.Clean(repo.Dir)
	for range repo.Segments {
		root = filepath.Dir(root)
	}
	return root
}
