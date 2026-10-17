package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

// forestImages is the fake library root the forest fixtures resolve
// member sources against.
const forestImages = "/images"

// forestFixture prepares an engine over a two-member forest table.
// The parent directory is a real plain directory so the live probe
// never sees a mount there; the member points are fake table paths
// the health map classifies. The returned map is the health seam's
// backing store, so a test can flip a member's health before or
// during a call.
func forestFixture(t *testing.T) (*Engine, string, map[string]bool) {
	t.Helper()
	dir := t.TempDir()
	parent := filepath.Join(dir, "forest")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(parent, "src")
	docs := filepath.Join(parent, "docs")
	table := []mountinfo.Mount{
		{Point: src, FSType: "fuse", Source: forestImages + "/alice/app/members/src.sqsh"},
		{Point: docs, FSType: "fuse", Source: forestImages + "/alice/app/members/docs.sqsh"},
	}
	healthy := map[string]bool{}
	e := newEngine(t.TempDir())
	e.ImagesDir = forestImages
	e.LoadTable = func() ([]mountinfo.Mount, error) { return table, nil }
	e.Health = func(point string) bool { return healthy[point] }
	return e, parent, healthy
}

// markForestHealthy flips both member points of the fixture healthy.
func markForestHealthy(parent string, healthy map[string]bool) {
	healthy[filepath.Join(parent, "src")] = true
	healthy[filepath.Join(parent, "docs")] = true
}

func TestForestHealthyShapes(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "forest")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(parent, "src")
	docs := filepath.Join(parent, "docs")
	tools := filepath.Join(parent, "tools")
	librarySrc := mountinfo.Mount{
		Point: src, FSType: "fuse",
		Source: forestImages + "/alice/app/members/src.sqsh",
	}
	libraryDocs := mountinfo.Mount{
		Point: docs, FSType: "fuse",
		Source: forestImages + "/alice/app/members/docs.sqsh",
	}
	foreign := mountinfo.Mount{Point: tools, FSType: "fuse", Source: "/mnt/usb/tools"}
	parentEntry := mountinfo.Mount{Point: parent, FSType: "fuse", Source: "/x.sqsh"}

	tests := []struct {
		name      string
		imagesDir string
		table     []mountinfo.Mount
		healthy   map[string]bool
		want      bool
	}{
		{
			name:      "every library child healthy",
			imagesDir: forestImages,
			table:     []mountinfo.Mount{librarySrc, libraryDocs},
			healthy:   map[string]bool{src: true, docs: true},
			want:      true,
		},
		{
			name:      "a foreign child never affects the combination",
			imagesDir: forestImages,
			table:     []mountinfo.Mount{librarySrc, libraryDocs, foreign},
			healthy:   map[string]bool{src: true, docs: true},
			want:      true,
		},
		{
			name:      "one member dead",
			imagesDir: forestImages,
			table:     []mountinfo.Mount{librarySrc, libraryDocs},
			healthy:   map[string]bool{src: true},
			want:      false,
		},
		{
			// Known edge: a member whose table entry vanished
			// entirely is invisible to this table-driven
			// approximation; per-member reuse detection on the
			// next open catches it instead.
			name:      "a vanished member entry keeps the approximation",
			imagesDir: forestImages,
			table:     []mountinfo.Mount{librarySrc},
			healthy:   map[string]bool{src: true},
			want:      true,
		},
		{
			name:      "no library child",
			imagesDir: forestImages,
			table:     []mountinfo.Mount{foreign},
			healthy:   map[string]bool{tools: true},
			want:      false,
		},
		{
			name:      "the parent itself is a mount",
			imagesDir: forestImages,
			table:     []mountinfo.Mount{parentEntry, librarySrc, libraryDocs},
			healthy:   map[string]bool{src: true, docs: true},
			want:      false,
		},
		{
			name:      "no library root configured",
			imagesDir: "",
			table:     []mountinfo.Mount{librarySrc, libraryDocs},
			healthy:   map[string]bool{src: true, docs: true},
			want:      false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEngine(t.TempDir())
			e.ImagesDir = tc.imagesDir
			single := func(point string) bool { return tc.healthy[point] }
			if got := e.forestHealthy(tc.table, parent, single); got != tc.want {
				t.Fatalf("forestHealthy = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOpenSkipsHealthyForestCombination(t *testing.T) {
	e, parent, healthy := forestFixture(t)
	markForestHealthy(parent, healthy)
	seedMounted(t, e, "dev", parent)
	mounts := 0
	mount := func(a Action) (Result, error) {
		mounts++
		return Result{Mountpoint: a.Mountpoint}, nil
	}
	if err := e.Open("dev", mount); err != nil {
		t.Fatal(err)
	}
	if mounts != 0 {
		t.Fatalf("a healthy forest combination was replayed: %d mounts", mounts)
	}
	// A repeated open stays a pure no-op.
	if err := e.Open("dev", mount); err != nil {
		t.Fatal(err)
	}
	if mounts != 0 {
		t.Fatalf("a repeated open replayed the healthy forest: %d mounts", mounts)
	}
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Actions[0].Status != StatusMounted {
		t.Errorf("status = %q, want it kept mounted", rec.Actions[0].Status)
	}
}

func TestOpenRepairsPartiallyDeadForest(t *testing.T) {
	e, parent, healthy := forestFixture(t)
	src := filepath.Join(parent, "src")
	docs := filepath.Join(parent, "docs")
	healthy[src] = true
	// docs stays dead: the combination is unhealthy and must replay.
	seedMounted(t, e, "dev", parent)
	var cleaned []string
	e.Clean = func(point string) error {
		cleaned = append(cleaned, point)
		return nil
	}
	mounts := 0
	// The idempotent mount seam repairs the dead member, which the
	// health map reflects.
	mount := func(a Action) (Result, error) {
		mounts++
		healthy[docs] = true
		return Result{Mountpoint: a.Mountpoint}, nil
	}
	if err := e.Open("dev", mount); err != nil {
		t.Fatal(err)
	}
	if mounts != 1 {
		t.Fatalf("mounts = %d, want one replay of the forest action", mounts)
	}
	if len(cleaned) != 1 || cleaned[0] != parent {
		t.Fatalf("cleaned = %v, want the recorded parent point", cleaned)
	}
	// With the combination healthy again, a repeated open is a
	// no-op.
	if err := e.Open("dev", mount); err != nil {
		t.Fatal(err)
	}
	if mounts != 1 {
		t.Fatalf("the repaired forest was replayed again: %d mounts", mounts)
	}
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Actions[0].Status != StatusMounted {
		t.Errorf("status = %q, want it written back mounted", rec.Actions[0].Status)
	}
}

func TestSessionForPathForestMemberResolvesParentRecord(t *testing.T) {
	e, parent, healthy := forestFixture(t)
	markForestHealthy(parent, healthy)
	seedMounted(t, e, "dev", parent)
	src := filepath.Join(parent, "src")

	queries := []string{
		src,
		filepath.Join(src, "main.go"),
		filepath.Join(parent, "docs", "guide", "intro.md"),
		// The parent itself resolves through the recorded
		// mountpoint with the combination health annotation.
		parent,
	}
	for _, query := range queries {
		hit, err := e.SessionForPath(query)
		if err != nil {
			t.Fatalf("SessionForPath(%s): %v", query, err)
		}
		if hit.Record.Name != "dev" || hit.Mountpoint != parent || hit.Stale {
			t.Fatalf("SessionForPath(%s) = %+v", query, hit)
		}
	}
}

func TestSessionForPathForestMemberReportsStaleCombination(t *testing.T) {
	e, parent, healthy := forestFixture(t)
	healthy[filepath.Join(parent, "src")] = true
	// docs is dead: the whole combination reports stale.
	seedMounted(t, e, "dev", parent)
	hit, err := e.SessionForPath(filepath.Join(parent, "src", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if hit.Record.Name != "dev" || hit.Mountpoint != parent {
		t.Fatalf("hit = %+v", hit)
	}
	if !hit.Stale {
		t.Error("a forest with a dead member must report stale")
	}
}

func TestSessionForPathForestIdentityMismatchMisses(t *testing.T) {
	e, parent, healthy := forestFixture(t)
	markForestHealthy(parent, healthy)
	// The record at the parent belongs to a different image than
	// the member sources in the table.
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	action := mountAction(parent)
	action.Image = "bob/other"
	action.Owner = "bob"
	action.Repo = "other"
	mount := func(a Action) (Result, error) {
		return Result{Mountpoint: a.Mountpoint}, nil
	}
	if _, err := e.RunMount("dev", action, mount); err != nil {
		t.Fatal(err)
	}
	_, err := e.SessionForPath(filepath.Join(parent, "src", "main.go"))
	if err == nil || !strings.Contains(err.Error(), "no session covers") {
		t.Fatalf("err = %v, want a miss on the identity mismatch", err)
	}
}

func TestOverviewAnnotatesForestCombination(t *testing.T) {
	e, parent, healthy := forestFixture(t)
	markForestHealthy(parent, healthy)
	seedMounted(t, e, "dev", parent)
	overview, err := e.Overview("")
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Sessions) != 1 {
		t.Fatalf("sessions = %+v", overview.Sessions)
	}
	actions := overview.Sessions[0].Actions
	if len(actions) != 1 {
		t.Fatalf("actions = %+v", actions)
	}
	if actions[0].Health != HealthHealthy {
		t.Errorf("health = %q, want the healthy forest combination", actions[0].Health)
	}
	// A dead member flips the combination annotation to stale.
	healthy[filepath.Join(parent, "docs")] = false
	overview, err = e.Overview("")
	if err != nil {
		t.Fatal(err)
	}
	if got := overview.Sessions[0].Actions[0].Health; got != HealthStale {
		t.Errorf("health = %q, want the stale forest combination", got)
	}
}
