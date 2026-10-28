package image

import (
	"errors"
	"fmt"
	"os"
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

// AppendVersionStackActivityLocked records one logical activity event
// for a stack whose tip and complete root-to-tip chain the caller
// already resolved. The mount boundary uses it: the chain came from the
// manifest that resolved the view, so the batch reuses that chain
// instead of walking it again, writes one record per member in a single
// traversal, and never propagates a member on its own. The caller must
// hold the library lock.
//
// The tip records the event itself. Each ancestor records
// image.dependency.activity naming the tip and the source event, so a
// dependency use never reads like a direct mount or creation of that
// ancestor. The chain is checked against the manifest's own stack of
// tip, so a stale, reordered, or foreign chain records nothing. One
// timestamp covers the batch and the writes run root to tip, exactly as
// AppendVersionActivityLocked documents.
func AppendVersionStackActivityLocked(repo Repo, tip string, chain []ImageMeta, ev activity.Event, clock activity.Clock) error {
	return versionActivityBatch{repo: repo, clock: clock}.appendStack(tip, chain, ev)
}

// EnsureVersionActivityLocked writes the one initialization record of a
// version that has no usable log yet, and a dependency initialization
// record for every ancestor of its chain. The caller must hold the
// library lock. A version that already has a readable log causes no
// write and no propagation, so repeated scans, tail reads, and
// compaction never renew an existing log.
//
// The ancestor records land first, each as a dependency
// initialization, so an existing ancestor log is refreshed the one time
// this migration is allowed to. The source record is written last,
// through the activity helper that probes and appends under one log
// lock, so a version is never initialized twice even from a caller that
// skipped the library lock.
//
// The returned versions receive a record in this call — every ancestor
// of the chain, root first, then the source when it was written — so a
// caller can exclude them from this round's expiry decision. Versions
// written before a failure are returned with the error for the same
// reason.
func EnsureVersionActivityLocked(repo Repo, sourceID string, clock activity.Clock) ([]string, error) {
	return versionActivityBatch{repo: repo, clock: clock}.ensure(sourceID)
}

// recordVersionReuse records that a publication used an identical
// version that is already published. A repeated publication must not
// claim a second creation, so it reports this use instead. The caller
// must hold the library lock.
func recordVersionReuse(repo Repo, old ImageMeta) error {
	return AppendVersionActivityLocked(repo, old.ID, activity.Event{
		Event: activity.EventImageReuse,
		Phase: activity.PhaseSucceeded,
	}, nil)
}

// versionCreationEvent names the creation of one published version.
func versionCreationEvent(node ImageMeta) string {
	if node.Kind == KindIncremental {
		return activity.EventImageCreateIncremental
	}
	return activity.EventImageCreateFull
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
	if err := b.prepare(chain); err != nil {
		return fmt.Errorf("image %s: activity %s: %w", b.repo.Name(), ev.Event, err)
	}
	at, err := b.batchTime(chain)
	if err != nil {
		return err
	}
	return b.writeChain(chain, at, ev, sourceID)
}

// appendStack writes one event along a chain the caller resolved.
func (b versionActivityBatch) appendStack(tip string, chain []ImageMeta, ev activity.Event) error {
	if err := ev.Validate(); err != nil {
		return fmt.Errorf("image %s: activity %w", b.repo.Name(), err)
	}
	if err := b.checkResolvedStack(tip, chain); err != nil {
		return fmt.Errorf("image %s: activity %s: %w", b.repo.Name(), ev.Event, err)
	}
	if err := b.prepare(chain); err != nil {
		return fmt.Errorf("image %s: activity %s: %w", b.repo.Name(), ev.Event, err)
	}
	at, err := b.batchTime(chain)
	if err != nil {
		return err
	}
	return b.writeChain(chain, at, ev, tip)
}

// checkResolvedStack proves a caller-resolved chain is the manifest's
// own root-to-tip stack of tip. Reusing the chain saves the second walk
// over the forest; comparing it to the manifest keeps a chain that
// drifted from the published forest from recording activity against the
// wrong versions.
func (b versionActivityBatch) checkResolvedStack(tip string, chain []ImageMeta) error {
	if len(chain) == 0 {
		return errors.New("a resolved stack is required")
	}
	if last := chain[len(chain)-1].ID; last != tip {
		return fmt.Errorf("resolved stack tip is %s, not %s", last, tip)
	}
	published, err := b.chain(tip)
	if err != nil {
		return err
	}
	if len(published) != len(chain) {
		return fmt.Errorf("resolved stack of %s holds %d versions, not %d",
			tip, len(published), len(chain))
	}
	for i, node := range published {
		if node.ID != chain[i].ID {
			return fmt.Errorf("resolved stack of %s lists %s at position %d, not %s",
				tip, node.ID, i, chain[i].ID)
		}
	}
	return nil
}

// ensure initializes the chain of one legacy version exactly once.
func (b versionActivityBatch) ensure(sourceID string) ([]string, error) {
	chain, err := b.chain(sourceID)
	if err != nil {
		return nil, fmt.Errorf("image %s: activity %s: %w",
			b.repo.Name(), activity.EventInitialize, err)
	}
	if err := b.prepare(chain); err != nil {
		return nil, fmt.Errorf("image %s: activity %s: %w",
			b.repo.Name(), activity.EventInitialize, err)
	}
	missing, err := b.uninitialized(sourceID)
	if err != nil || !missing {
		return nil, err
	}
	at, err := b.batchTime(chain)
	if err != nil {
		return nil, err
	}
	ev := activity.Event{Event: activity.EventInitialize, Phase: activity.PhaseInitialized}
	written := make([]string, 0, len(chain))
	for _, node := range chain[:len(chain)-1] {
		if err := b.write(node.ID, at, versionActivityRecord(ev, b.repo.Name(), sourceID, node.ID)); err != nil {
			return written, err
		}
		written = append(written, node.ID)
	}
	wrote, err := b.initialize(sourceID, at, versionActivityRecord(ev, b.repo.Name(), sourceID, sourceID))
	if err != nil {
		return written, err
	}
	if wrote {
		written = append(written, sourceID)
	}
	return written, nil
}

// initialize writes the source record of an initialization and reports
// whether this call wrote it.
func (b versionActivityBatch) initialize(id string, at time.Time, ev activity.Event) (bool, error) {
	w, err := b.writer(id, func() time.Time { return at })
	var wrote bool
	if err == nil {
		wrote, _, err = w.EnsureInitialized(ev)
	}
	if err != nil {
		return false, fmt.Errorf("image %s: activity %s %s at version %s: %w",
			b.repo.Name(), ev.Event, ev.Phase, id, err)
	}
	return wrote, nil
}

// prepare creates the object directory of every chain member. The log
// lives inside that directory, and a version committed before this
// release carries no activity files yet, so the first record of a
// batch may be the one that needs the directory.
func (b versionActivityBatch) prepare(chain []ImageMeta) error {
	for _, node := range chain {
		dir, err := b.repo.ObjectDirPath(node.ID)
		if err == nil {
			err = os.MkdirAll(dir, 0o755)
		}
		if err != nil {
			return fmt.Errorf("activity directory of version %s: %w", node.ID, err)
		}
	}
	return nil
}

// uninitialized reports a source log that is missing or empty. An
// unreadable or damaged log is an error: a non-empty log is never
// treated as a legacy object and never rewritten.
func (b versionActivityBatch) uninitialized(id string) (bool, error) {
	w, err := b.writer(id, b.resolvedClock())
	if err != nil {
		return false, fmt.Errorf("image %s: activity tail of version %s: %w",
			b.repo.Name(), id, err)
	}
	_, err = w.LastActivity()
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, activity.ErrNoActivity):
		return true, nil
	default:
		return false, fmt.Errorf("image %s: activity tail of version %s: %w",
			b.repo.Name(), id, err)
	}
}

// writeChain writes one record per chain member, root to source.
func (b versionActivityBatch) writeChain(chain []ImageMeta, at time.Time, ev activity.Event, sourceID string) error {
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
