package image

import (
	"errors"
	"os"
	"path/filepath"
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

// makeArchive lays one archive directory out with a real meta whose
// created_at controls retention ranking.
func makeArchive(t *testing.T, repo Repo, data []byte, createdAt time.Time) Meta {
	t.Helper()
	meta := metaFor(data)
	meta.CreatedAt = createdAt.UTC()
	dir, err := repo.ArchivePath(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileImage), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Store(filepath.Join(dir, fileMeta), meta); err != nil {
		t.Fatal(err)
	}
	return meta
}

// holdViaPublish replaces the active image while a reference pins
// it, leaving the old chain in hold.
func holdViaPublish(t *testing.T, repo Repo) Meta {
	t.Helper()
	old := seedActive(t, repo, []byte("retired-full"), true)
	refs := holdSet{ids: map[string]bool{old.ID: true}}
	next := []byte("replacement-full")
	if err := repo.Publish(next, metaFor(next), refs); err != nil {
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
	if len(events) != 1 || events[0].Action != GCActionArchived || events[0].FullID != old.ID {
		t.Fatalf("events = %+v", events)
	}
	arch, err := repo.ArchivePath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(arch, fileImage), []byte("retired-full"))
	incr, err := chainIncr(arch, old.Chain[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, incr, []byte("layer"))
	if _, statErr := os.Stat(filepath.Join(repo.Dir, dirHold, old.ID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("hold still present: %v", statErr)
	}
}

func TestGCLeavesReferencedHold(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	old := holdViaPublish(t, repo)
	refs := holdSet{ids: map[string]bool{old.ID: true}}
	events, err := GC(cfg, refs, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %+v", events)
	}
	hold, err := repo.HoldPath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(hold, fileImage), []byte("retired-full"))
	if _, statErr := os.Stat(filepath.Join(repo.Dir, dirArchives, old.ID)); !errors.Is(statErr, os.ErrNotExist) {
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
	if len(events) != 1 || events[0].Action != GCActionRemoved || events[0].FullID != oldest.ID {
		t.Fatalf("events = %+v", events)
	}
	if _, statErr := os.Stat(archPath(t, repo, oldest.ID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("oldest archive survived: %v", statErr)
	}
	assertBytes(t, filepath.Join(archPath(t, repo, middle.ID), fileImage),
		[]byte("archive-middle"))
	assertBytes(t, filepath.Join(archPath(t, repo, newest.ID), fileImage),
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
	if len(events) != 1 || events[0].FullID != aged.ID {
		t.Fatalf("events = %+v", events)
	}
	if _, statErr := os.Stat(archPath(t, repo, fresh.ID)); statErr != nil {
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
	if !gone[old1.ID] || !gone[old2.ID] || gone[fresh.ID] {
		t.Fatalf("wrong archives deleted: %+v", events)
	}
}

func TestGCSkipsReferencedArchive(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	cfg.MaxAge = time.Hour
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	pinned := makeArchive(t, repo, []byte("archive-pinned"), now.Add(-48*time.Hour))
	refs := holdSet{ids: map[string]bool{pinned.ID: true}}
	events, err := GC(cfg, refs, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %+v", events)
	}
	if _, statErr := os.Stat(archPath(t, repo, pinned.ID)); statErr != nil {
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
	active.Chain = []ChainEntry{
		validEntry(active.ID, base.ID),
		validEntry(base.ID, "fedcba9876543210"),
	}
	if err := Store(repo.MetaPath(), active); err != nil {
		t.Fatal(err)
	}
	events, err := GC(cfg, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("chain-referenced archive deleted: %+v", events)
	}
	if _, statErr := os.Stat(archPath(t, repo, base.ID)); statErr != nil {
		t.Fatalf("archive deleted: %v", statErr)
	}
}

func TestGCProtectsMemberParentOfLiveChain(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	cfg.MaxAge = time.Hour
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	member := "fedcba9876543210"
	base := makeArchive(t, repo, []byte("archive-with-member"), now.Add(-48*time.Hour))
	// Rewrite the archive meta so its chain holds the member id.
	base.Chain = []ChainEntry{validEntry(base.ID, member)}
	if err := Store(filepath.Join(archPath(t, repo, base.ID), fileMeta), base); err != nil {
		t.Fatal(err)
	}
	active := seedActive(t, repo, []byte("active-full"), false)
	active.Chain = []ChainEntry{
		validEntry(active.ID, member),
		validEntry(member, "0000000000000001"),
	}
	if err := Store(repo.MetaPath(), active); err != nil {
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
	dir := archPath(t, repo, broken.ID)
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
	`"type":"mount","image":"alice/app","owner":"alice","repo":"app",` +
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
	refs := pinRefs{pin: Ref{Owner: "alice", Repo: "app", FullID: "0000000000000000"}}
	events, err := GC(cfg, refs, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != GCActionRemoved {
		t.Fatalf("events = %+v", events)
	}
	assertBytes(t, record, []byte(sessionRecordBody))
	assertBytes(t, repo.ImagePath(), []byte("active-full"))
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != active.ID {
		t.Fatalf("active meta changed: %+v", loaded)
	}
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
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Deprecated || loaded.ID != active.ID {
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
	refs := holdSet{ids: map[string]bool{active.ID: true}}
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
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Deprecated {
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
	target := "alice/app/" + dirArchives + "/" + victim.ID
	outcome, err := RemoveTarget(cfg, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeDeprecated {
		t.Fatalf("outcome = %s", outcome)
	}
	archived, err := Load(filepath.Join(archPath(t, repo, victim.ID), fileMeta))
	if err != nil {
		t.Fatal(err)
	}
	if !archived.Deprecated {
		t.Fatal("archive meta not deprecated")
	}
	activeMeta, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if activeMeta.Deprecated {
		t.Fatal("archive rm deprecated the active meta")
	}
	outcome, err = RemoveTarget(cfg, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeRemoved {
		t.Fatalf("outcome = %s", outcome)
	}
	if _, statErr := os.Stat(archPath(t, repo, victim.ID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("archive dir still present: %v", statErr)
	}
	if _, statErr := os.Stat(archPath(t, repo, survivor.ID)); statErr != nil {
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
	active.Chain = []ChainEntry{
		validEntry(active.ID, base.ID),
		validEntry(base.ID, "fedcba9876543210"),
	}
	if err := Store(repo.MetaPath(), active); err != nil {
		t.Fatal(err)
	}
	target := "alice/app/" + dirArchives + "/" + base.ID
	_, err := RemoveTarget(cfg, target, nil)
	if err == nil || !strings.Contains(err.Error(), "referenced") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(archPath(t, repo, base.ID)); statErr != nil {
		t.Fatalf("referenced archive deleted: %v", statErr)
	}
}

func TestRemoveTargetWholeRepoRefusedByHoldReference(t *testing.T) {
	cfg, repo := gcRepoFixture(t)
	old := holdViaPublish(t, repo)
	refs := holdSet{ids: map[string]bool{old.ID: true}}
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
		"plain",
		"a/b/c",
		"alice/app/archive/0123456789abcdef",
		"alice/app/archives/not-hex",
		"alice/app/_archives/not-hex",
		"alice/app/archives/0123456789abcdef/extra",
		"alice/app/_archives/0123456789abcdef/extra",
		"bad owner/app",
	} {
		if _, err := RemoveTarget(cfg, target, nil); err == nil {
			t.Errorf("target %q was accepted", target)
		}
	}
	if _, statErr := os.Stat(repo.ImagePath()); statErr != nil {
		t.Fatalf("active image deleted by a bad target: %v", statErr)
	}
}
