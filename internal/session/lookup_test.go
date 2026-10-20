package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

// lookupEngine prepares an engine over fake tables and health maps.
// Records live in a real temporary sessions directory; mountpoints
// are fake table paths the health map classifies.
func lookupEngine(t *testing.T, table []mountinfo.Mount, healthy map[string]bool) (*Engine, string) {
	t.Helper()
	dir := t.TempDir()
	e := newEngine(dir)
	e.LoadTable = func() ([]mountinfo.Mount, error) { return table, nil }
	e.Health = func(point string) bool { return healthy[point] }
	return e, dir
}

// overlayEntry is one writable merged view with its runtime layout.
func overlayEntry(point, runtimeDir string) mountinfo.Mount {
	return mountinfo.Mount{
		Point:  point,
		FSType: "fuse.fuse-overlayfs",
		Upper:  filepath.Join(runtimeDir, "upper"),
		Lowers: []string{filepath.Join(runtimeDir, "lower-0")},
	}
}

func TestSessionForPathMergedDeepPathAndRoot(t *testing.T) {
	view := "/ws/view"
	table := []mountinfo.Mount{overlayEntry(view, "/rt/up-1-2")}
	e, _ := lookupEngine(t, table, map[string]bool{view: true})
	seedMounted(t, e, "dev", view)

	for _, path := range []string{view, view + "/src/main.go", view + "/a/b/c"} {
		hit, err := e.SessionForPath(path)
		if err != nil {
			t.Fatalf("SessionForPath(%s): %v", path, err)
		}
		if hit.Record.Name != "dev" || hit.Mountpoint != view || hit.Stale {
			t.Fatalf("SessionForPath(%s) = %+v", path, hit)
		}
	}
}

func TestSessionForPathReadOnlyView(t *testing.T) {
	ro := "/ws/ro"
	table := []mountinfo.Mount{{Point: ro, FSType: "fuse", Source: "/img.sqsh"}}
	e, _ := lookupEngine(t, table, map[string]bool{ro: true})
	seedMounted(t, e, "reader", ro)

	hit, err := e.SessionForPath(ro + "/docs/guide.md")
	if err != nil {
		t.Fatal(err)
	}
	if hit.Record.Name != "reader" || hit.Mountpoint != ro || hit.Stale {
		t.Fatalf("hit = %+v", hit)
	}
}

func TestSessionForPathDeepestCoveringViewWins(t *testing.T) {
	outer, inner := "/ws", "/ws/view"
	table := []mountinfo.Mount{
		{Point: outer, FSType: "fuse", Source: "/a.sqsh"},
		overlayEntry(inner, "/rt/up-1-2"),
	}
	e, _ := lookupEngine(t, table, map[string]bool{outer: true, inner: true})
	seedMounted(t, e, "outer-session", outer)
	seedMounted(t, e, "inner-session", inner)

	hit, err := e.SessionForPath(inner + "/deep/file")
	if err != nil {
		t.Fatal(err)
	}
	if hit.Record.Name != "inner-session" {
		t.Fatalf("deep hit = %+v, want the inner session", hit)
	}
	hit, err = e.SessionForPath(outer + "/elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	if hit.Record.Name != "outer-session" {
		t.Fatalf("sibling hit = %+v, want the outer session", hit)
	}
}

func TestSessionForPathListedButDeadReportsStale(t *testing.T) {
	view := "/ws/view"
	table := []mountinfo.Mount{overlayEntry(view, "/rt/up-1-2")}
	e, _ := lookupEngine(t, table, map[string]bool{})
	seedMounted(t, e, "dev", view)

	hit, err := e.SessionForPath(view + "/deep")
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Stale || hit.Record.Name != "dev" {
		t.Fatalf("hit = %+v, want the stale dev session", hit)
	}
}

func TestSessionForPathRuntimeCombinationResolves(t *testing.T) {
	view := "/ws/view"
	roView := "/ws/ro-chain"
	devRT := "/sessions/dev/runtime/m1"
	readerRT := "/sessions/reader/runtime/m1"
	table := []mountinfo.Mount{
		overlayEntry(view, devRT),
		{
			Point:  roView,
			FSType: "fuse.fuse-overlayfs",
			Lowers: []string{readerRT + "/lower-0", readerRT + "/lower-1"},
		},
	}
	e, _ := lookupEngine(t, table, map[string]bool{view: true, roView: true})
	e.SessionsDir = "/sessions"
	seedMounted(t, e, "dev", view)
	seedMounted(t, e, "reader", roView)

	paths := []struct {
		path string
		want string
	}{
		{path: devRT + "/upper/new.txt", want: "dev"},
		{path: devRT + "/work", want: "dev"},
		{path: devRT + "/lower-0/deep", want: "dev"},
		{path: devRT, want: "dev"},
		{path: readerRT + "/lower-1/image.sqsh-content", want: "reader"},
	}
	for _, tc := range paths {
		hit, err := e.SessionForPath(tc.path)
		if err != nil {
			t.Fatalf("SessionForPath(%s): %v", tc.path, err)
		}
		if hit.Record.Name != tc.want {
			t.Errorf("SessionForPath(%s) = %+v, want %s", tc.path, hit, tc.want)
		}
	}
}

func TestSessionForPathNoCoverage(t *testing.T) {
	e, _ := lookupEngine(t, nil, nil)
	seedMounted(t, e, "dev", "/ws/view")

	_, err := e.SessionForPath("/elsewhere/deep")
	if err == nil || !strings.Contains(err.Error(), "no session covers") {
		t.Fatalf("error = %v, want the no-coverage report", err)
	}
}

func TestSessionForPathForeignMountHasNoSession(t *testing.T) {
	table := []mountinfo.Mount{{Point: "/mnt/other", FSType: "fuse", Source: "x"}}
	e, _ := lookupEngine(t, table, map[string]bool{"/mnt/other": true})

	_, err := e.SessionForPath("/mnt/other/deep")
	if err == nil || !strings.Contains(err.Error(), "no session covers") {
		t.Fatalf("error = %v, want the no-coverage report", err)
	}
}

func TestSessionForPathStaleFallbackAfterEntryVanished(t *testing.T) {
	view := "/ws/view"
	e, _ := lookupEngine(t, nil, map[string]bool{})
	seedMounted(t, e, "dev", view)

	// The table entry is gone and the point reads as dead: the
	// record scan still finds the covering session and reports it
	// stale.
	hit, err := e.SessionForPath(view + "/deep/file")
	if err != nil {
		t.Fatal(err)
	}
	if hit.Record.Name != "dev" || hit.Mountpoint != view || !hit.Stale {
		t.Fatalf("hit = %+v, want the stale dev session", hit)
	}
}

func TestSessionForPathStaleFallbackDeepestRecordWins(t *testing.T) {
	e, _ := lookupEngine(t, nil, map[string]bool{})
	seedMounted(t, e, "outer-session", "/ws")
	seedMounted(t, e, "inner-session", "/ws/view")

	hit, err := e.SessionForPath("/ws/view/deep")
	if err != nil {
		t.Fatal(err)
	}
	if hit.Record.Name != "inner-session" {
		t.Fatalf("hit = %+v, want the deepest recorded mountpoint", hit)
	}
}

func TestSessionForPathIgnoresClosedAndUnmounted(t *testing.T) {
	view := "/ws/view"
	e, _ := lookupEngine(t, nil, map[string]bool{})
	seedMounted(t, e, "dev", view)
	if _, err := e.Close("dev", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SessionForPath(view + "/deep"); err == nil {
		t.Fatal("a closed session still covers paths")
	}

	seedMounted(t, e, "second", view)
	if err := e.Writeback("second", 1, StatusUnmounted, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SessionForPath(view + "/deep"); err == nil {
		t.Fatal("an unmounted action still covers paths")
	}
}

func TestSessionForPathNormalizationFailureStillResolves(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(filepath.Join(dir, "sessions"))
	e.LoadTable = func() ([]mountinfo.Mount, error) { return nil, nil }
	e.Health = func(string) bool { return false }
	view := filepath.Join(dir, "view")
	if err := os.MkdirAll(view, 0o755); err != nil {
		t.Fatal(err)
	}
	seedMounted(t, e, "dev", view)

	// The path walks through directories that do not exist, so
	// readlink -f semantics fail and the literal fallback resolves.
	dead := filepath.Join(view, "ghost", "deeper", "file.txt")
	if _, err := filepath.EvalSymlinks(dead); err == nil {
		t.Fatal("the fixture path unexpectedly resolves")
	}
	hit, err := e.SessionForPath(dead)
	if err != nil {
		t.Fatal(err)
	}
	if hit.Record.Name != "dev" || hit.Mountpoint != view {
		t.Fatalf("hit = %+v", hit)
	}
}

func TestSessionForPathReportsCorruptRecords(t *testing.T) {
	e, dir := lookupEngine(t, nil, nil)
	seedMounted(t, e, "dev", "/ws/view")
	if err := os.MkdirAll(filepath.Join(dir, "broken"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken", "session.json"), []byte(":::"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := e.SessionForPath("/ws/view/deep")
	if err == nil || !strings.Contains(err.Error(), "session.json") {
		t.Fatalf("error = %v, want the corrupt record reported", err)
	}
}

func TestSessionForPathSkipsLockedRecords(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("advisory locking needs linux")
	}
	e, _ := lookupEngine(t, nil, map[string]bool{})
	seedMounted(t, e, "dev", "/ws/view")
	release, err := e.lockSession("dev", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := e.SessionForPath("/ws/view/deep"); err == nil {
		t.Fatal("a record held by another writer was still scanned")
	}
}

func TestListShowAndSummariesReadModel(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	seedMounted(t, e, "dev", "/ws/one", "/ws/two")
	seedMounted(t, e, "done", "/ws/three")
	if _, err := e.Close("done", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}

	records, err := e.List()
	if err != nil {
		t.Fatal(err)
	}
	summaries := Summaries(records)
	if len(summaries) != 2 {
		t.Fatalf("summaries = %+v", summaries)
	}
	if summaries[0].Name != "dev" || summaries[0].State != StateActive || summaries[0].Mounts != 2 {
		t.Errorf("dev summary = %+v", summaries[0])
	}
	if !summaries[0].Created.Equal(testNow) {
		t.Errorf("dev created = %v", summaries[0].Created)
	}
	if summaries[1].Name != "done" || summaries[1].State != StateClosed || summaries[1].Mounts != 0 {
		t.Errorf("done summary = %+v", summaries[1])
	}
	if !summaries[1].Closed.Equal(testNow) {
		t.Errorf("done closed = %v", summaries[1].Closed)
	}

	rec, err := e.Show("dev")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Name != "dev" || len(rec.Actions) != 2 {
		t.Fatalf("show = %+v", rec)
	}
}

func TestViewRuntimeDirLayout(t *testing.T) {
	writable := overlayEntry("/ws/view", "/sessions/dev/runtime/m1")
	if got := viewRuntimeDir(writable); got != "/sessions/dev/runtime/m1" {
		t.Errorf("writable runtime = %q", got)
	}
	readOnly := mountinfo.Mount{
		Point:  "/ws/ro",
		FSType: "fuse.fuse-overlayfs",
		Lowers: []string{"/sessions/reader/runtime/m1/lower-0", "/sessions/reader/runtime/m1/lower-1"},
	}
	if got := viewRuntimeDir(readOnly); got != "/sessions/reader/runtime/m1" {
		t.Errorf("read-only runtime = %q", got)
	}
	plain := mountinfo.Mount{Point: "/ws/x", FSType: "fuse", Source: "x"}
	if got := viewRuntimeDir(plain); got != "" {
		t.Errorf("plain mount runtime = %q, want empty", got)
	}
}

func TestSessionRuntimeOf(t *testing.T) {
	tests := []struct {
		path string
		name string
		dir  string
		ok   bool
	}{
		{path: "/sessions/dev/runtime/m1/upper/x", name: "dev", dir: "/sessions/dev/runtime/m1", ok: true},
		{path: "/sessions/dev/runtime/m1", name: "dev", dir: "/sessions/dev/runtime/m1", ok: true},
		{path: "/sessions/dev/session.json", ok: false},
		{path: "/sessions/dev", ok: false},
		{path: "/other/dev/runtime/m1", ok: false},
	}
	for _, tc := range tests {
		name, dir, ok := sessionRuntimeOf("/sessions", tc.path)
		if ok != tc.ok || name != tc.name || dir != tc.dir {
			t.Errorf("sessionRuntimeOf(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.path, name, dir, ok, tc.name, tc.dir, tc.ok)
		}
	}
}

func TestSessionForRuntimePathAfterEntryVanished(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	view := filepath.Join(dir, "view")
	if err := os.MkdirAll(view, 0o755); err != nil {
		t.Fatal(err)
	}
	e := newEngine(sessions)
	e.SessionsDir = sessions
	e.LoadTable = nil
	seedMounted(t, e, "dev", view)
	runtimeDir := filepath.Join(sessions, "dev", "runtime", "m1")
	if err := os.MkdirAll(filepath.Join(runtimeDir, "upper"), 0o755); err != nil {
		t.Fatal(err)
	}

	hit, err := e.SessionForPath(filepath.Join(runtimeDir, "upper", "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if hit.Record.Name != "dev" || hit.Mountpoint != view || !hit.Stale {
		t.Fatalf("hit = %+v", hit)
	}
	if _, err := e.SessionForPath(filepath.Join(sessions, "other", "runtime", "m1", "upper")); err == nil {
		t.Fatal("a foreign runtime directory resolved")
	}
}
