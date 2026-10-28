package session

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

// attachEngine builds an engine whose attachment guard reads one fake
// mount table. The sessions directory doubles as the store, the way a
// default layout points both at sessions_dir.
func attachEngine(t *testing.T, table []mountinfo.Mount) *Engine {
	t.Helper()
	sessions := t.TempDir()
	e := newEngine(sessions)
	e.SessionsDir = sessions
	e.LoadTable = func() ([]mountinfo.Mount, error) { return table, nil }
	return e
}

// attachRuntime creates one runtime directory so symlink resolution on
// both sides of the comparison sees the same spelling.
func attachRuntime(t *testing.T, e *Engine, name, seq string) string {
	t.Helper()
	runtime := filepath.Join(e.SessionsDir, name, "runtime", seq)
	if err := os.MkdirAll(filepath.Join(runtime, "upper"), 0o755); err != nil {
		t.Fatal(err)
	}
	return runtime
}

func TestAttachedReportsARecordedView(t *testing.T) {
	view := "/ws/view"
	e := attachEngine(t, []mountinfo.Mount{
		{Point: "/", FSType: "ext4"},
		overlayEntry(view, "/rt/up-1-2"),
		{Point: "/ws/other", FSType: "ext4"},
	})
	seedMounted(t, e, "dev", view)

	point, err := e.Attached(loadRecord(t, e, "dev"))
	if err != nil {
		t.Fatalf("Attached: %v", err)
	}
	if point != view {
		t.Fatalf("Attached = %q, want %q", point, view)
	}
}

// loadRecord reads the stored record, so the guard sees what a restart
// would see instead of the in-memory copy a caller happens to hold.
func loadRecord(t *testing.T, e *Engine, name string) Record {
	t.Helper()
	rec, err := e.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestAttachedCountsUnmountedAndUnhealthyViews(t *testing.T) {
	view := "/ws/view"
	e := attachEngine(t, []mountinfo.Mount{overlayEntry(view, "/rt/up-1-2")})
	// A dead FUSE daemon leaves the kernel mount behind, so a failing
	// health check must not turn an attached view into an unattached one.
	e.Health = func(string) bool { return false }
	seedMounted(t, e, "dev", view)
	if err := e.Writeback("dev", 1, StatusUnmounted, ""); err != nil {
		t.Fatal(err)
	}

	point, err := e.Attached(loadRecord(t, e, "dev"))
	if err != nil {
		t.Fatalf("Attached: %v", err)
	}
	if point != view {
		t.Fatalf("Attached = %q, want %q", point, view)
	}
}

// TestAttachedReportsAClosedRecordWithALiveView keeps the guard honest about
// the stored status: the kernel mount outlives the close, so the record an
// age-based planner would delete first is exactly the one that must stay.
func TestAttachedReportsAClosedRecordWithALiveView(t *testing.T) {
	view := "/ws/view"
	e := attachEngine(t, []mountinfo.Mount{overlayEntry(view, "/rt/up-1-2")})
	seedMounted(t, e, "dev", view)
	if _, err := e.Close("dev", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	rec := loadRecord(t, e, "dev")
	if rec.State != StateClosed {
		t.Fatalf("state = %q, want the closed record to be stored", rec.State)
	}

	point, err := e.Attached(rec)
	if err != nil {
		t.Fatalf("Attached: %v", err)
	}
	if point != view {
		t.Fatalf("Attached = %q, want %q for a closed record", point, view)
	}
}

func TestAttachedPrefersTheViewOverANestedMount(t *testing.T) {
	view := "/ws/view"
	e := attachEngine(t, []mountinfo.Mount{
		{Point: filepath.Join(view, "src"), FSType: "ext4"},
		overlayEntry(view, "/rt/up-1-2"),
	})
	seedMounted(t, e, "dev", view)

	point, err := e.Attached(loadRecord(t, e, "dev"))
	if err != nil {
		t.Fatalf("Attached: %v", err)
	}
	if point != view {
		t.Fatalf("Attached = %q, want the view %q, not a mount below it", point, view)
	}
}

// TestAttachedResolvesASymlinkedViewSpelling covers the prefix hdiutil
// resolves: the record keeps the link it wrote and the host reports the
// concrete path, so only the resolved comparison matches them.
func TestAttachedResolvesASymlinkedViewSpelling(t *testing.T) {
	concrete := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(concrete, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	e := attachEngine(t, []mountinfo.Mount{{Point: concrete, FSType: "fuse"}})
	rec := Record{Name: "dev", State: StateActive, Actions: []Action{mountAction(link)}}

	point, err := e.Attached(rec)
	if err != nil {
		t.Fatalf("Attached: %v", err)
	}
	if point != concrete {
		t.Fatalf("Attached = %q, want the mounted spelling %q", point, concrete)
	}
}

func TestAttachedCountsNestedMountsBelowAView(t *testing.T) {
	view := "/ws/view"
	nested := filepath.Join(view, "src")
	e := attachEngine(t, []mountinfo.Mount{{Point: nested, FSType: "ext4"}})
	seedMounted(t, e, "dev", view)

	point, err := e.Attached(loadRecord(t, e, "dev"))
	if err != nil {
		t.Fatalf("Attached: %v", err)
	}
	if point != nested {
		t.Fatalf("Attached = %q, want the nested mount %q", point, nested)
	}
}

func TestAttachedCountsRuntimeIntermediateLayers(t *testing.T) {
	view := "/ws/view"
	e := attachEngine(t, nil)
	runtime := attachRuntime(t, e, "dev", "m2")
	layer := filepath.Join(runtime, "upper")
	e.LoadTable = func() ([]mountinfo.Mount, error) {
		return []mountinfo.Mount{{Point: layer, FSType: "tmpfs"}}, nil
	}
	seedMounted(t, e, "dev", view)

	point, err := e.Attached(loadRecord(t, e, "dev"))
	if err != nil {
		t.Fatalf("Attached: %v", err)
	}
	if point != layer {
		t.Fatalf("Attached = %q, want the runtime layer %q", point, layer)
	}
}

func TestAttachedIgnoresForeignSessionsAndUnrelatedMounts(t *testing.T) {
	view := "/ws/view"
	e := attachEngine(t, nil)
	foreign := attachRuntime(t, e, "other", "m1")
	e.LoadTable = func() ([]mountinfo.Mount, error) {
		return []mountinfo.Mount{
			{Point: foreign, FSType: "tmpfs"},
			{Point: "/ws/other", FSType: "fuse"},
		}, nil
	}
	seedMounted(t, e, "dev", view)

	point, err := e.Attached(loadRecord(t, e, "dev"))
	if err != nil {
		t.Fatalf("Attached: %v", err)
	}
	if point != "" {
		t.Fatalf("Attached = %q, want no attachment", point)
	}
}

func TestAttachedKeepsTheDirectoryBoundary(t *testing.T) {
	view := "/ws/view"
	e := attachEngine(t, []mountinfo.Mount{
		{Point: view + "-old", FSType: "fuse"},
		{Point: filepath.Join(filepath.Dir(view), "viewer"), FSType: "fuse"},
	})
	seedMounted(t, e, "dev", view)

	point, err := e.Attached(loadRecord(t, e, "dev"))
	if err != nil {
		t.Fatalf("Attached: %v", err)
	}
	if point != "" {
		t.Fatalf("Attached = %q, want no attachment for a look-alike path", point)
	}
}

func TestAttachedReportsAnUnreadableTable(t *testing.T) {
	view := "/ws/view"
	e := attachEngine(t, nil)
	e.LoadTable = func() ([]mountinfo.Mount, error) { return nil, mountinfo.ErrUnavailable }
	seedMounted(t, e, "dev", view)

	point, err := e.Attached(loadRecord(t, e, "dev"))
	if !errors.Is(err, mountinfo.ErrUnavailable) {
		t.Fatalf("Attached error = %v, want the inspection failure", err)
	}
	if point != "" {
		t.Fatalf("Attached = %q, want no point with an unreadable table", point)
	}
}

func TestAttachedRejectsAnInvalidSessionName(t *testing.T) {
	e := attachEngine(t, nil)
	if _, err := e.Attached(Record{Name: "bad/name"}); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("Attached error = %v, want ErrInvalidName", err)
	}
}

func TestAttachedReportsDiskImageVolumes(t *testing.T) {
	view := "/Volumes/view"
	sessions := t.TempDir()
	e := newEngine(sessions)
	e.SessionsDir = sessions
	e.LoadDiskImages = func() ([]DiskImage, error) {
		return []DiskImage{{Point: view, Image: "/lib/app.dmg", Shadow: ""}}, nil
	}
	if !e.useDiskImages() {
		t.Fatal("the disk listing seam did not take over")
	}
	seedMounted(t, e, "dev", view)

	point, err := e.Attached(loadRecord(t, e, "dev"))
	if err != nil {
		t.Fatalf("Attached: %v", err)
	}
	if point != view {
		t.Fatalf("Attached = %q, want %q", point, view)
	}
}

func TestAttachedDerivesTheDiskRuntimeFromAShadow(t *testing.T) {
	e := attachEngine(t, nil)
	e.LoadTable = nil
	runtime := attachRuntime(t, e, "dev", "m1")
	shadow := filepath.Join(runtime, "upper.shadow")
	e.LoadDiskImages = func() ([]DiskImage, error) {
		return []DiskImage{
			{Point: "/Volumes/other", Image: "/lib/other.dmg", Shadow: ""},
			{Point: "", Image: "/lib/app.dmg", Shadow: shadow},
		}, nil
	}
	seedMounted(t, e, "dev", "/Volumes/view")

	point, err := e.Attached(loadRecord(t, e, "dev"))
	if err != nil {
		t.Fatalf("Attached: %v", err)
	}
	if point != runtime {
		t.Fatalf("Attached = %q, want the runtime %q of the shadow", point, runtime)
	}
}

func TestAttachedReportsAnUnreadableDiskListing(t *testing.T) {
	e := attachEngine(t, nil)
	e.LoadTable = nil
	boom := errors.New("hdiutil info failed")
	e.LoadDiskImages = func() ([]DiskImage, error) { return nil, boom }
	seedMounted(t, e, "dev", "/Volumes/view")

	if _, err := e.Attached(loadRecord(t, e, "dev")); !errors.Is(err, boom) {
		t.Fatalf("Attached error = %v, want the listing failure", err)
	}
}

// TestAttachedReadsTheHostTableWithoutASeam keeps the fallback honest:
// a missing table seam loads the real host table instead of reporting
// every session unattached.
func TestAttachedReadsTheHostTableWithoutASeam(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only Linux exposes the host mount table")
	}
	e := newEngine(t.TempDir())
	e.SessionsDir = ""
	e.LoadTable = nil
	missing := filepath.Join(t.TempDir(), "unmounted", "view")
	rec := Record{Name: "probe", State: StateActive, Actions: []Action{mountAction(missing)}}

	point, err := e.Attached(rec)
	if err != nil {
		t.Fatalf("Attached: %v", err)
	}
	if point != "" {
		t.Fatalf("Attached = %q, want no attachment for %q", point, missing)
	}
}
