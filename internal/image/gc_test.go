package image

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
)

func gcRepoFixture(t *testing.T) (config.Config, Repo) {
	t.Helper()
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	return cfg, repo
}

func TestGCV3KeepsHistoryAndRemovesOnlyOrphanObjects(t *testing.T) {
	imagesDir, repo, nodes := removalFixture(t)
	orphan := strings.Repeat("f", 64)
	orphanDir, err := repo.ObjectDirPath(orphan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(orphanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		ImagesDir: imagesDir, KeepVersions: 1, MaxAge: time.Nanosecond,
	}
	events, err := GC(cfg, nil, time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != GCActionRemoved ||
		events[0].FullID != orphan {
		t.Fatalf("events = %#v", events)
	}
	for _, node := range nodes {
		dir, pathErr := repo.ObjectDirPath(node.ID)
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		if _, statErr := os.Stat(dir); statErr != nil {
			t.Fatalf("historical object %s was removed: %v", node.ID, statErr)
		}
	}
	if _, err := os.Stat(orphanDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan remains: %v", err)
	}
}

func TestGCV3CorruptMetadataFailsWithoutRemovingObjects(t *testing.T) {
	imagesDir, repo, nodes := removalFixture(t)
	orphan := strings.Repeat("f", 64)
	orphanDir, err := repo.ObjectDirPath(orphan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(orphanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo.MetaPath(), []byte("version: '3.0'\nimages: ["), 0o644); err != nil {
		t.Fatal(err)
	}

	if events, err := GC(config.Config{ImagesDir: imagesDir}, nil, time.Now()); err == nil {
		t.Fatalf("GC succeeded with events %#v", events)
	}
	for _, id := range append([]string{orphan}, nodes[0].ID, nodes[1].ID) {
		dir, pathErr := repo.ObjectDirPath(id)
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		if _, statErr := os.Stat(dir); statErr != nil {
			t.Fatalf("object %s was removed: %v", id, statErr)
		}
	}
}

// makeArchive lays one archive directory out with a real manifest
// whose root created_at controls retention ranking.
func makeArchive(t *testing.T, repo Repo, data []byte, createdAt time.Time) Manifest {
	t.Helper()
	manifest := manifestFor(data)
	manifest.Name = repo.Name()
	manifest.Images[0].CreatedAt = createdAt.UTC()
	dir, err := repo.ArchivePath(rootID(manifest))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileImage), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := StoreManifest(filepath.Join(dir, fileMeta), manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

// holdViaPublish replaces the active image while a reference pins
// it, leaving the old chain in hold.
func holdViaPublish(t *testing.T, repo Repo) Manifest {
	t.Helper()
	old := seedActive(t, repo, []byte("retired-full"), true)
	refs := holdSet{ids: map[string]bool{rootID(old): true}}
	next := []byte("replacement-full")
	if err := repo.Publish(next, manifestFor(next), refs); err != nil {
		t.Fatal(err)
	}
	return old
}

func TestGCPromotesUnreferencedHold(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	old := holdViaPublish(t, repo)
	events, err := GC(cfg, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != GCActionArchived || events[0].FullID != rootID(old) {
		t.Fatalf("events = %+v", events)
	}
	arch, err := repo.ArchivePath(rootID(old))
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(arch, fileImage), []byte("retired-full"))
	incr, err := chainIncr(arch, old.Images[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, incr, []byte("layer"))
	if _, statErr := os.Stat(filepath.Join(repo.Dir, dirHold, rootID(old))); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("hold still present: %v", statErr)
	}
}

func TestGCLeavesReferencedHold(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	old := holdViaPublish(t, repo)
	refs := holdSet{ids: map[string]bool{rootID(old): true}}
	events, err := GC(cfg, refs, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %+v", events)
	}
	hold, err := repo.HoldPath(rootID(old))
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(hold, fileImage), []byte("retired-full"))
	if _, statErr := os.Stat(filepath.Join(repo.Dir, dirArchives, rootID(old))); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("referenced hold was archived: %v", statErr)
	}
}

func TestGCKeepsNewestVersions(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	cfg.KeepVersions = 2
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	oldest := makeArchive(t, repo, []byte("archive-oldest"), now.Add(-72*time.Hour))
	middle := makeArchive(t, repo, []byte("archive-middle"), now.Add(-48*time.Hour))
	newest := makeArchive(t, repo, []byte("archive-newest"), now.Add(-1*time.Hour))
	events, err := GC(cfg, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != GCActionRemoved || events[0].FullID != rootID(oldest) {
		t.Fatalf("events = %+v", events)
	}
	if _, statErr := os.Stat(archPath(t, repo, rootID(oldest))); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("oldest archive survived: %v", statErr)
	}
	assertBytes(t, filepath.Join(archPath(t, repo, rootID(middle)), fileImage),
		[]byte("archive-middle"))
	assertBytes(t, filepath.Join(archPath(t, repo, rootID(newest)), fileImage),
		[]byte("archive-newest"))
}

func archPath(t *testing.T, repo Repo, id string) string {
	t.Helper()
	path, err := repo.ArchivePath(id)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// makeLegacyHold lays a pre-sha256 _hold/<16-hex> directory. The
// directory name is the legacy id used for pins; the manifest inside
// still carries a full sha256 so LoadManifest succeeds under current
// validation.
func makeLegacyHold(t *testing.T, repo Repo, data []byte) string {
	t.Helper()
	manifest := manifestFor(data)
	manifest.Name = repo.Name()
	legacy := rootID(manifest)[:idHexLen]
	dir, err := repo.HoldPath(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileImage), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := StoreManifest(filepath.Join(dir, fileMeta), manifest); err != nil {
		t.Fatal(err)
	}
	return legacy
}

// makeLegacyArchive lays _archives/<legacy> with a Validate-passing
// manifest. legacy must be a 16-hex name.
func makeLegacyArchive(t *testing.T, repo Repo, legacy string, data []byte, createdAt time.Time) Manifest {
	t.Helper()
	if !legacyID(legacy) {
		t.Fatalf("legacy id %q", legacy)
	}
	manifest := manifestFor(data)
	manifest.Name = repo.Name()
	manifest.Images[0].CreatedAt = createdAt.UTC()
	dir, err := repo.ArchivePath(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileImage), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := StoreManifest(filepath.Join(dir, fileMeta), manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

// plantArchive writes _archives/<id> with a Validate-passing
// manifest whose root id equals sha256. The payload bytes need not
// hash to id.
func plantArchive(t *testing.T, repo Repo, id string, data []byte, createdAt time.Time) Manifest {
	t.Helper()
	manifest := validManifest()
	manifest.Images = manifest.Images[:1]
	manifest.Name = repo.Name()
	manifest.Images[0].ID = id
	manifest.Images[0].SHA256 = id
	manifest.Images[0].Size = int64(len(data))
	manifest.Images[0].CreatedAt = createdAt.UTC()
	manifest.Head = id
	dir, err := repo.ArchivePath(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileImage), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := StoreManifest(filepath.Join(dir, fileMeta), manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestGCDeletesExpiredByAge(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	cfg.MaxAge = 24 * time.Hour
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	aged := makeArchive(t, repo, []byte("archive-aged"), now.Add(-48*time.Hour))
	fresh := makeArchive(t, repo, []byte("archive-fresh"), now.Add(-1*time.Hour))
	events, err := GC(cfg, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].FullID != rootID(aged) {
		t.Fatalf("events = %+v", events)
	}
	if _, statErr := os.Stat(archPath(t, repo, rootID(fresh))); statErr != nil {
		t.Fatalf("fresh archive deleted: %v", statErr)
	}
}

func TestGCZeroMeansUnlimited(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	makeArchive(t, repo, []byte("archive-ancient"), now.Add(-100*24*time.Hour))
	makeArchive(t, repo, []byte("archive-recent"), now.Add(-time.Hour))
	events, err := GC(cfg, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("zero rules deleted archives: %+v", events)
	}
}

func TestGCUnionsCountAndAgeRules(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	cfg.KeepVersions = 2
	cfg.MaxAge = 24 * time.Hour
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	old1 := makeArchive(t, repo, []byte("archive-old-1"), now.Add(-96*time.Hour))
	old2 := makeArchive(t, repo, []byte("archive-old-2"), now.Add(-72*time.Hour))
	fresh := makeArchive(t, repo, []byte("archive-fresh"), now.Add(-time.Hour))
	events, err := GC(cfg, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	// old-2 ranks inside the kept count but is too old; old-1 is
	// beyond the count. Both go, the fresh one stays.
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	gone := map[string]bool{events[0].FullID: true, events[1].FullID: true}
	if !gone[rootID(old1)] || !gone[rootID(old2)] || gone[rootID(fresh)] {
		t.Fatalf("wrong archives deleted: %+v", events)
	}
}

func TestGCSkipsReferencedArchive(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	cfg.MaxAge = time.Hour
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	pinned := makeArchive(t, repo, []byte("archive-pinned"), now.Add(-48*time.Hour))
	refs := holdSet{ids: map[string]bool{rootID(pinned): true}}
	events, err := GC(cfg, refs, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %+v", events)
	}
	if _, statErr := os.Stat(archPath(t, repo, rootID(pinned))); statErr != nil {
		t.Fatalf("referenced archive deleted: %v", statErr)
	}
}

func TestGCChainParentProtectsArchive(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	cfg.MaxAge = time.Hour
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	base := makeArchive(t, repo, []byte("archive-base"), now.Add(-48*time.Hour))
	// The active chain was built on the archived full image: its
	// second member parent still points at that id.
	active := seedActive(t, repo, []byte("active-full"), false)
	active = withNode(active, validNode(rootID(active), rootID(base)))
	active = withNode(active, validNode(rootID(base), "fedcba9876543210"))
	if err := StoreManifest(repo.MetaPath(), active); err != nil {
		t.Fatal(err)
	}
	events, err := GC(cfg, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("chain-referenced archive deleted: %+v", events)
	}
	if _, statErr := os.Stat(archPath(t, repo, rootID(base))); statErr != nil {
		t.Fatalf("archive deleted: %v", statErr)
	}
}

func TestGCProtectsMemberParentOfLiveChain(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	cfg.MaxAge = time.Hour
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	member := "fedcba9876543210"
	base := makeArchive(t, repo, []byte("archive-with-member"), now.Add(-48*time.Hour))
	// Rewrite the archive manifest so its DAG holds the member id.
	base = withNode(base, validNode(rootID(base), member))
	if err := StoreManifest(filepath.Join(archPath(t, repo, rootID(base)), fileMeta), base); err != nil {
		t.Fatal(err)
	}
	active := seedActive(t, repo, []byte("active-full"), false)
	active = withNode(active, validNode(rootID(active), member))
	active = withNode(active, validNode(member, "0000000000000001"))
	if err := StoreManifest(repo.MetaPath(), active); err != nil {
		t.Fatal(err)
	}
	events, err := GC(cfg, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("member-referenced archive deleted: %+v", events)
	}
}

func TestGCKeepsUnreadableArchive(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	cfg.MaxAge = time.Hour
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	broken := makeArchive(t, repo, []byte("archive-broken"), now.Add(-48*time.Hour))
	dir := archPath(t, repo, rootID(broken))
	if err := os.WriteFile(filepath.Join(dir, fileMeta), []byte("::: not yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	events, err := GC(cfg, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("unreadable archive deleted: %+v", events)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("archive dir gone: %v", statErr)
	}
}

// pinRefs is a test Refs pinning exactly one full image.
type pinRefs struct {
	pin Ref
}

func (p pinRefs) Holds(ref Ref) (bool, error) {
	return ref == p.pin, nil
}

func (p pinRefs) Holder(ref Ref) (string, bool, error) {
	if ref == p.pin {
		return "pinning-session", true, nil
	}
	return "", false, nil
}

// sessionRecordBody is one JSON session record in the shape the
// session package writes, used to prove GC never touches the
// sessions directory.
const sessionRecordBody = `{"name":"s1","state":"active","actions":[{"seq":1,` +
	`"type":"mount","image":"alice/app","name":"alice/app",` +
	`"full_id":"0000000000000000","mountpoint":"/ws/view","mode":"rw",` +
	`"status":"mounted","timestamp":"2026-10-16T09:00:00Z"}],` +
	`"created_at":"2026-10-16T09:00:00Z","closed_at":"0001-01-01T00:00:00Z"}`

func TestGCNeverTouchesSessionsOrActive(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	sessions := cfg.SessionsDir
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(sessions, "s1.json")
	if err := os.WriteFile(record, []byte(sessionRecordBody), 0o600); err != nil {
		t.Fatal(err)
	}
	active := seedActive(t, repo, []byte("active-full"), true)
	cfg.MaxAge = time.Hour
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	makeArchive(t, repo, []byte("archive-old"), now.Add(-48*time.Hour))
	refs := pinRefs{pin: Ref{Name: "alice/app", FullID: "0000000000000000"}}
	events, err := GC(cfg, refs, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != GCActionRemoved {
		t.Fatalf("events = %+v", events)
	}
	assertBytes(t, record, []byte(sessionRecordBody))
	assertBytes(t, repo.ImagePath(), []byte("active-full"))
	loaded := mustLoadManifest(t, repo.MetaPath())
	if rootID(loaded) != rootID(active) {
		t.Fatalf("active meta changed: %+v", loaded)
	}
}

// tipRefs is a test Refs pinning one (full, tip) stack: it matches
// the full id and the stack tip together, the shape a stack-aware
// holder uses. A query with another tip — or none — is not held.
type tipRefs struct {
	name   string
	fullID string
	tipID  string
}

func (p tipRefs) Holds(ref Ref) (bool, error) {
	return ref.Name == p.name && ref.FullID == p.fullID && ref.TipID == p.tipID, nil
}

func (p tipRefs) Holder(ref Ref) (string, bool, error) {
	held, err := p.Holds(ref)
	if held {
		return "pinning-session", true, err
	}
	return "", false, err
}

// TestGCKeepsHeadReachableLayers proves the reclaim step leaves a
// linear DAG alone: every layer sits on the head stack.
func TestGCKeepsHeadReachableLayers(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	manifest := seedActive(t, repo, []byte("linear-full"), true)
	layer := manifest.Images[1].ID
	events, err := GC(cfg, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("head-reachable layers reclaimed: %+v", events)
	}
	assertBytes(t, mustIncrPath(t, repo, layer), []byte("layer"))
	loaded := mustLoadManifest(t, repo.MetaPath())
	if len(loaded.Images) != 2 || !loaded.Has(layer) {
		t.Fatalf("manifest = %+v", loaded)
	}
}

// TestGCReclaimsUnreferencedBranch proves the reclaim step prunes a
// branch node no pinned tip reaches: the node leaves the manifest
// and its layer file is deleted, while the head stack survives.
func TestGCReclaimsUnreferencedBranch(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	manifest, layer, branch := seedBranchingActive(t, repo, []byte("branching-full"))
	events, err := GC(cfg, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != GCActionRemoved ||
		events[0].Name != "alice/app" || events[0].FullID != layer {
		t.Fatalf("events = %+v, want the off-head layer %s removed", events, layer)
	}
	assertGone(t, mustIncrPath(t, repo, layer))
	assertBytes(t, mustIncrPath(t, repo, branch), []byte("branch"))
	loaded := mustLoadManifest(t, repo.MetaPath())
	if loaded.Has(layer) || !loaded.Has(branch) || loaded.Head != manifest.Head {
		t.Fatalf("manifest = %+v", loaded)
	}
	if got := loaded.Branches(); got != 0 {
		t.Fatalf("branches = %d, want the pruned DAG linear", got)
	}
}

// TestGCKeepsBranchPinnedByTip proves a branch layer survives while
// a reference pins a tip whose reachable set covers it.
func TestGCKeepsBranchPinnedByTip(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	manifest, layer, branch := seedBranchingActive(t, repo, []byte("branching-full"))
	refs := tipRefs{name: repo.Name(), fullID: rootID(manifest), tipID: layer}
	events, err := GC(cfg, refs, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("pinned branch reclaimed: %+v", events)
	}
	assertBytes(t, mustIncrPath(t, repo, layer), []byte("layer"))
	assertBytes(t, mustIncrPath(t, repo, branch), []byte("branch"))
	loaded := mustLoadManifest(t, repo.MetaPath())
	if len(loaded.Images) != 3 || loaded.Head != manifest.Head {
		t.Fatalf("manifest = %+v", loaded)
	}
}

// TestGCFullIDPinCoversBranchTips documents the conservative
// matching of a holder that predates stack tips: its pin answers
// held for every tip query on the full id, so branches survive
// while the full image is pinned.
func TestGCFullIDPinCoversBranchTips(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	manifest, layer, _ := seedBranchingActive(t, repo, []byte("branching-full"))
	refs := holdSet{ids: map[string]bool{rootID(manifest): true}}
	events, err := GC(cfg, refs, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("full-id pin let the branch reclaim: %+v", events)
	}
	assertBytes(t, mustIncrPath(t, repo, layer), []byte("layer"))
}

// TestGCKeepsAncestorsOfPinnedDescendantTip seeds a two-node
// off-head chain root→A→B beside a head branch root→C and pins only
// the descendant tip B. The covering relation is ancestor-closed, so
// A must survive too: pruning it would corrupt the stack a session
// pinned at B still resolves through.
func TestGCKeepsAncestorsOfPinnedDescendantTip(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	data := []byte("descendant-full")
	manifest := manifestFor(data)
	ancestor := validNode(manifest.Head, "aaaaaaaaaaaaaaaa")
	descendant := validNode(ancestor.ID, "bbbbbbbbbbbbbbbb")
	headLayer := validNode(manifest.Head, "cccccccccccccccc")
	manifest = withNode(manifest, ancestor)
	manifest = withNode(manifest, descendant)
	manifest = withNode(manifest, headLayer)
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo.ImagePath(), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := StoreManifest(repo.MetaPath(), manifest); err != nil {
		t.Fatal(err)
	}
	writeLayer(t, repo, ancestor.ID, []byte("ancestor"))
	writeLayer(t, repo, descendant.ID, []byte("descendant"))
	writeLayer(t, repo, headLayer.ID, []byte("head-layer"))

	refs := tipRefs{name: repo.Name(), fullID: rootID(manifest), tipID: descendant.ID}
	events, err := GC(cfg, refs, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("pinned tip's ancestor chain reclaimed: %+v", events)
	}
	assertBytes(t, mustIncrPath(t, repo, ancestor.ID), []byte("ancestor"))
	assertBytes(t, mustIncrPath(t, repo, descendant.ID), []byte("descendant"))
	assertBytes(t, mustIncrPath(t, repo, headLayer.ID), []byte("head-layer"))
	loaded := mustLoadManifest(t, repo.MetaPath())
	if len(loaded.Images) != 4 || loaded.Head != manifest.Head ||
		!loaded.Has(ancestor.ID) || !loaded.Has(descendant.ID) {
		t.Fatalf("manifest = %+v", loaded)
	}
}

// TestGCReclaimsBlocksBranch is the blocks-form twin of
// TestGCReclaimsUnreferencedBranch: the pruned node's _blocks/<id>/
// directory is reclaimed, while the head branch's block directory
// and the full image payload survive.
func TestGCReclaimsBlocksBranch(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	manifest, layer, branch := seedBlocksWithBranch(t, repo)
	events, err := GC(cfg, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != GCActionRemoved ||
		events[0].Name != "alice/app" || events[0].FullID != layer {
		t.Fatalf("events = %+v, want the off-head blocks layer %s removed", events, layer)
	}
	assertGone(t, mustBlocksDir(t, repo, layer))
	assertBytes(t, filepath.Join(mustBlocksDir(t, repo, branch), "blk.000"),
		[]byte("branch-body"))
	assertBytes(t, filepath.Join(finalPayloadDir(t, repo, rootID(manifest)), "blk.000"),
		[]byte("full-a"))
	loaded := mustLoadManifest(t, repo.MetaPath())
	if loaded.Has(layer) || !loaded.Has(branch) || loaded.Head != manifest.Head {
		t.Fatalf("manifest = %+v", loaded)
	}
}

// publishDuringPin is a test Refs whose first pin query lands a
// concurrent publish: it rewrites the active meta.yaml before
// answering, simulating a build finishing between the reclaim
// snapshot and the meta swap.
type publishDuringPin struct {
	publish func()
	done    bool
}

func (p *publishDuringPin) Holds(Ref) (bool, error) {
	if !p.done {
		p.done = true
		p.publish()
	}
	return false, nil
}

func (p *publishDuringPin) Holder(Ref) (string, bool, error) {
	return "", false, nil
}

// TestGCSkipsReclaimAfterConcurrentPublish proves the reclaim step
// abandons its snapshot when the active meta.yaml changed between
// the load and the swap: the concurrently published manifest
// survives intact and no layer file is deleted.
func TestGCSkipsReclaimAfterConcurrentPublish(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	manifest, layer, branch := seedBranchingActive(t, repo, []byte("branching-full"))
	published := withNode(manifest, validNode(rootID(manifest), "0011223344556678"))
	refs := &publishDuringPin{publish: func() {
		if err := StoreManifest(repo.MetaPath(), published); err != nil {
			t.Fatal(err)
		}
	}}
	events, err := GC(cfg, refs, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("reclaim ran on a changed manifest: %+v", events)
	}
	loaded := mustLoadManifest(t, repo.MetaPath())
	if !loaded.Has(published.Head) || !loaded.Has(layer) || !loaded.Has(branch) {
		t.Fatalf("concurrent publish lost: %+v", loaded)
	}
	assertBytes(t, mustIncrPath(t, repo, layer), []byte("layer"))
	assertBytes(t, mustIncrPath(t, repo, branch), []byte("branch"))
}

func TestGCEmptyLibrary(t *testing.T) {
	cfg := buildConfig(t)
	events, err := GC(cfg, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %+v", events)
	}
}

func TestGCSkipsForeignDirectories(t *testing.T) {
	cfg, _ := gcRepoFixture(t)
	if err := os.MkdirAll(filepath.Join(cfg.ImagesDir, "stray dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ImagesDir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := GC(cfg, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(cfg.ImagesDir, "stray dir")); statErr != nil {
		t.Fatalf("stray directory touched: %v", statErr)
	}
}

func TestRemoveTargetWholeRepoTwoPhases(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	active := seedActive(t, repo, []byte("doomed-full"), true)
	makeArchive(t, repo, []byte("old-archive"), time.Now())
	outcome, err := RemoveTarget(cfg, "alice/app", nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeDeprecated {
		t.Fatalf("outcome = %s", outcome)
	}
	loaded := mustLoadManifest(t, repo.MetaPath())
	if !rootOf(loaded).Deprecated || rootID(loaded) != rootID(active) {
		t.Fatalf("meta = %+v", loaded)
	}
	if _, statErr := os.Stat(repo.ImagePath()); statErr != nil {
		t.Fatalf("first phase deleted files: %v", statErr)
	}
	outcome, err = RemoveTarget(cfg, "alice/app", nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeRemoved {
		t.Fatalf("outcome = %s", outcome)
	}
	if _, statErr := os.Stat(repo.Dir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("repo dir still present: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Dir(repo.Dir)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("empty owner dir left behind: %v", statErr)
	}
}

func TestRemoveTargetRefusesWhileReferenced(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	active := seedActive(t, repo, []byte("pinned-full"), false)
	refs := holdSet{ids: map[string]bool{rootID(active): true}}
	for i := 0; i < 2; i++ {
		outcome, err := RemoveTarget(cfg, "alice/app", refs)
		if err == nil || !strings.Contains(err.Error(), "referenced") {
			t.Fatalf("call %d: outcome = %s err = %v", i, outcome, err)
		}
		// The refusal names the pinning session and the command
		// that releases it.
		for _, want := range []string{"pinning-session", "session close"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("call %d: err %q missing %q", i, err, want)
			}
		}
	}
	loaded := mustLoadManifest(t, repo.MetaPath())
	if rootOf(loaded).Deprecated {
		t.Fatal("referenced target was deprecated")
	}
	if _, statErr := os.Stat(repo.ImagePath()); statErr != nil {
		t.Fatalf("files deleted while referenced: %v", statErr)
	}
}

func TestRemoveTargetArchiveTwoPhases(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	seedActive(t, repo, []byte("active-full"), false)
	victim := makeArchive(t, repo, []byte("archive-victim"), time.Now())
	survivor := makeArchive(t, repo, []byte("archive-survivor"), time.Now())
	target := "alice/app/" + dirArchives + "/" + rootID(victim)
	outcome, err := RemoveTarget(cfg, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeDeprecated {
		t.Fatalf("outcome = %s", outcome)
	}
	archived := mustLoadManifest(t, filepath.Join(archPath(t, repo, rootID(victim)), fileMeta))
	if !rootOf(archived).Deprecated {
		t.Fatal("archive meta not deprecated")
	}
	activeMeta := mustLoadManifest(t, repo.MetaPath())
	if rootOf(activeMeta).Deprecated {
		t.Fatal("archive rm deprecated the active meta")
	}
	outcome, err = RemoveTarget(cfg, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeRemoved {
		t.Fatalf("outcome = %s", outcome)
	}
	if _, statErr := os.Stat(archPath(t, repo, rootID(victim))); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("archive dir still present: %v", statErr)
	}
	if _, statErr := os.Stat(archPath(t, repo, rootID(survivor))); statErr != nil {
		t.Fatalf("surviving archive deleted: %v", statErr)
	}
	if _, statErr := os.Stat(repo.ImagePath()); statErr != nil {
		t.Fatalf("active image deleted: %v", statErr)
	}
}

func TestRemoveTargetArchiveRefusedByChainParent(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	base := makeArchive(t, repo, []byte("archive-base"), time.Now())
	active := seedActive(t, repo, []byte("active-full"), false)
	active = withNode(active, validNode(rootID(active), rootID(base)))
	active = withNode(active, validNode(rootID(base), "fedcba9876543210"))
	if err := StoreManifest(repo.MetaPath(), active); err != nil {
		t.Fatal(err)
	}
	target := "alice/app/" + dirArchives + "/" + rootID(base)
	_, err := RemoveTarget(cfg, target, nil)
	if err == nil || !strings.Contains(err.Error(), "referenced") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(archPath(t, repo, rootID(base))); statErr != nil {
		t.Fatalf("referenced archive deleted: %v", statErr)
	}
}

func TestRemoveTargetWholeRepoRefusedByHoldReference(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	old := holdViaPublish(t, repo)
	refs := holdSet{ids: map[string]bool{rootID(old): true}}
	_, err := RemoveTarget(cfg, "alice/app", refs)
	if err == nil || !strings.Contains(err.Error(), "referenced") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "pinning-session") {
		t.Fatalf("whole-repo refusal %q missing the holder name", err)
	}
	if _, statErr := os.Stat(repo.Dir); statErr != nil {
		t.Fatalf("repo deleted while a hold is referenced: %v", statErr)
	}
}

func TestRemoveTargetWholeRepoRefusedByLegacyHoldReference(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	seedActive(t, repo, []byte("active-beside-legacy"), false)
	legacy := makeLegacyHold(t, repo, []byte("legacy-hold-payload"))
	refs := holdSet{ids: map[string]bool{legacy: true}}
	_, err := RemoveTarget(cfg, "alice/app", refs)
	if err == nil || !strings.Contains(err.Error(), "referenced") {
		t.Fatalf("err = %v", err)
	}
	hold, err := repo.HoldPath(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(hold); statErr != nil {
		t.Fatalf("legacy hold deleted while referenced: %v", statErr)
	}
	if _, statErr := os.Stat(repo.Dir); statErr != nil {
		t.Fatalf("repo deleted while a legacy hold is referenced: %v", statErr)
	}
}

func TestRemoveTargetArchiveLegacyAmbiguousWithFullPrefix(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	seedActive(t, repo, []byte("active-full"), false)
	full := makeArchive(t, repo, []byte("shared-archive-payload"), time.Now())
	legacy := rootID(full)[:idHexLen]
	makeLegacyArchive(t, repo, legacy, []byte("legacy-archive-sibling"), time.Now())
	target := "alice/app/" + dirArchives + "/" + legacy
	_, err := RemoveTarget(cfg, target, nil)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), strconv.Quote(legacy)) {
		t.Fatalf("error %q does not quote %q", err, legacy)
	}
	if _, statErr := os.Stat(archPath(t, repo, rootID(full))); statErr != nil {
		t.Fatalf("full archive touched: %v", statErr)
	}
	legacyDir, err := repo.ArchivePath(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(legacyDir); statErr != nil {
		t.Fatalf("legacy archive touched: %v", statErr)
	}
}

func TestGCLeavesLegacyHoldUnpromoted(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	seedActive(t, repo, []byte("active-full"), false)
	legacy := makeLegacyHold(t, repo, []byte("legacy-hold-payload"))
	events, err := GC(cfg, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.FullID == legacy {
			t.Fatalf("legacy hold was promoted: %+v", events)
		}
	}
	hold, err := repo.HoldPath(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(hold); statErr != nil {
		t.Fatalf("legacy hold missing after gc: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(repo.Dir, dirArchives, legacy)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("legacy hold was renamed into archives: %v", statErr)
	}
}

func TestRemoveTargetCleansName(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	seedActive(t, repo, []byte("clean-name"), true)
	outcome, err := RemoveTarget(cfg, "alice/./app", nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeDeprecated {
		t.Fatalf("outcome = %s", outcome)
	}
}

func TestRemoveTargetRejectsBadTargets(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	seedActive(t, repo, []byte("active-full"), false)
	for _, target := range []string{
		"alice/app/archive/0123456789abcdef",
		"alice/app/archives/not-hex",
		"alice/app/_archives/not-hex",
		"alice/app/archives/0123456789abcdef/extra",
		"alice/app/_archives/0123456789abcdef/extra",
		"bad owner/app",
		"_foo",
		"a/b/c/d/e/f",
	} {
		if _, err := RemoveTarget(cfg, target, nil); err == nil {
			t.Errorf("target %q was accepted", target)
		}
	}
	if _, statErr := os.Stat(repo.ImagePath()); statErr != nil {
		t.Fatalf("active image deleted by a bad target: %v", statErr)
	}
}

func TestRemoveTargetArchivePrefixResolution(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	seedActive(t, repo, []byte("active-full"), false)
	const (
		victimID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		otherID  = "aaaaaaaabbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		prefix   = "aaaaaaaaaaaa"
		shared   = "aaaaaaaa"
	)
	plantArchive(t, repo, victimID, []byte("archive-prefix-victim"), time.Now())
	plantArchive(t, repo, otherID, []byte("archive-prefix-other"), time.Now())

	target := "alice/app/" + dirArchives + "/" + prefix
	outcome, err := RemoveTarget(cfg, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeDeprecated {
		t.Fatalf("outcome = %s", outcome)
	}
	archived := mustLoadManifest(t, filepath.Join(archPath(t, repo, victimID), fileMeta))
	if !rootOf(archived).Deprecated {
		t.Fatal("unique prefix did not deprecate the matching archive")
	}
	otherMeta := mustLoadManifest(t, filepath.Join(archPath(t, repo, otherID), fileMeta))
	if rootOf(otherMeta).Deprecated {
		t.Fatal("unique prefix deprecated a different archive")
	}

	_, err = RemoveTarget(cfg, "alice/app/"+dirArchives+"/"+shared, nil)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous err = %v", err)
	}
	if !strings.Contains(err.Error(), strconv.Quote(shared)) {
		t.Fatalf("ambiguous error %q does not quote %q", err, shared)
	}
	loaded := mustLoadManifest(t, filepath.Join(archPath(t, repo, otherID), fileMeta))
	if rootOf(loaded).Deprecated {
		t.Fatal("ambiguous prefix marked another archive")
	}

	const unknown = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	_, err = RemoveTarget(cfg, "alice/app/"+dirArchives+"/"+unknown, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown err = %v", err)
	}
	if !strings.Contains(err.Error(), strconv.Quote(unknown)) {
		t.Fatalf("unknown error %q does not quote the ref", err)
	}
	if errors.Is(err, ErrInvalidName) {
		t.Fatalf("well-formed unknown id classified as usage error: %v", err)
	}

	bad := "alice/app/" + dirArchives + "/not-hex"
	_, err = RemoveTarget(cfg, bad, nil)
	if err == nil || !errors.Is(err, ErrInvalidName) {
		t.Fatalf("non-hex err = %v", err)
	}
	if !strings.Contains(err.Error(), strconv.Quote(bad)) {
		t.Fatalf("non-hex error %q does not quote the target", err)
	}
}

func TestRemoveTargetArchiveLegacyExactName(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	seedActive(t, repo, []byte("active-full"), false)
	const legacy = "cccccccccccccccc"
	makeLegacyArchive(t, repo, legacy, []byte("legacy-only-archive"), time.Now())
	target := "alice/app/" + dirArchives + "/" + legacy
	outcome, err := RemoveTarget(cfg, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeDeprecated {
		t.Fatalf("outcome = %s", outcome)
	}
	dir, err := repo.ArchivePath(legacy)
	if err != nil {
		t.Fatal(err)
	}
	meta := mustLoadManifest(t, filepath.Join(dir, fileMeta))
	if !rootOf(meta).Deprecated {
		t.Fatal("exact legacy archive name was not selected")
	}
}

func TestRemoveTargetOneAndThreeSegmentNames(t *testing.T) {
	for _, name := range []string{"solo", "a/b/c"} {
		t.Run(name, func(t *testing.T) {
			cfg := buildConfig(t)
			repo, err := OpenRepo(cfg.ImagesDir, name)
			if err != nil {
				t.Fatal(err)
			}
			seedActive(t, repo, []byte("doomed-"+name), false)
			if _, err := RemoveTarget(cfg, name, nil); err != nil {
				t.Fatal(err)
			}
			outcome, err := RemoveTarget(cfg, name, nil)
			if err != nil {
				t.Fatal(err)
			}
			if outcome != OutcomeRemoved {
				t.Fatalf("outcome = %s", outcome)
			}
			if _, err := os.Stat(cfg.ImagesDir); err != nil {
				t.Fatalf("library root deleted: %v", err)
			}
			if _, err := os.Stat(repo.Dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("repo dir survived: %v", err)
			}
			if name == "a/b/c" {
				if _, err := os.Stat(filepath.Join(cfg.ImagesDir, "a")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("empty namespace left behind: %v", err)
				}
			}
		})
	}
}
