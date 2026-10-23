package session

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

var _ mountinfo.IdentitySource = Identity{}
var _ mountinfo.IdentitySource = CombinedIdentity{}

func TestIdentityLookupFromActiveRecord(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	view := "/ws/view"
	seedMounted(t, e, "dev", view)
	ident := Identity{Store: Store{Dir: dir}}

	name, ok, err := ident.Lookup(view)
	if err != nil || !ok || name.String() != "alice/app" {
		t.Fatalf("lookup = %v %v %v", name, ok, err)
	}
	if _, ok, err := ident.Lookup("/ws/other"); ok || err != nil {
		t.Fatalf("unknown point = %v %v", ok, err)
	}
	// A torn-down action and a closed record resolve nothing.
	if err := e.Writeback("dev", 1, StatusUnmounted, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ident.Lookup(view); ok || err != nil {
		t.Fatalf("unmounted action = %v %v", ok, err)
	}
	if err := e.Writeback("dev", 1, StatusMounted, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Close("dev", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ident.Lookup(view); ok || err != nil {
		t.Fatalf("closed record = %v %v", ok, err)
	}
}

func TestIdentityLookupMatchesResolvedMountPoint(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	concrete := filepath.Join(dir, "concrete")
	if err := os.Mkdir(concrete, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(concrete, link); err != nil {
		t.Fatal(err)
	}
	recorded, err := filepath.Abs(link)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(recorded)
	if err != nil || resolved == recorded {
		t.Fatalf("resolved = %s, recorded = %s, %v", resolved, recorded, err)
	}
	seedMounted(t, e, "dev", recorded)
	name, ok, err := Identity{Store: Store{Dir: dir}}.Lookup(resolved)
	if err != nil || !ok || name.String() != "alice/app" {
		t.Fatalf("lookup = %v %v %v", name, ok, err)
	}
}

func TestIdentitySkipsFilePathImages(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	view := filepath.Join(dir, "view")
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	mount := func(a Action) (Result, error) { return Result{Mountpoint: a.Mountpoint}, nil }
	fileAction := Action{
		Type:       TypeMount,
		Image:      filepath.Join(dir, "bare.sqsh"),
		Mountpoint: view,
		Mode:       ModeRO,
	}
	if _, err := e.RunMount("dev", fileAction, mount); err != nil {
		t.Fatal(err)
	}
	name, ok, err := Identity{Store: Store{Dir: dir}}.Lookup(view)
	if ok || err != nil {
		t.Fatalf("file-path image resolved = %v %v", name, ok)
	}
}

func TestIdentitySkipsHeldRecords(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("advisory locking needs linux")
	}
	dir := t.TempDir()
	e := newEngine(dir)
	view := "/ws/view"
	seedMounted(t, e, "dev", view)
	release, err := e.lockSession("dev", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, ok, err := (Identity{Store: Store{Dir: dir}}).Lookup(view); ok || err != nil {
		t.Fatalf("held record resolved = %v %v", ok, err)
	}
}

// spyIdentity counts lookups to prove the combination order.
type spyIdentity struct {
	name  mountinfo.Name
	ok    bool
	err   error
	calls int
}

func (s *spyIdentity) Lookup(string) (mountinfo.Name, bool, error) {
	s.calls++
	return s.name, s.ok, s.err
}

func TestCombinedIdentityOrder(t *testing.T) {
	want := mountinfo.Name("alice/app")
	primary := &spyIdentity{name: want, ok: true}
	fallback := &spyIdentity{}
	name, ok, err := CombinedIdentity{Primary: primary, Fallback: fallback}.Lookup("/ws/view")
	if err != nil || !ok || name != want {
		t.Fatalf("primary hit = %v %v %v", name, ok, err)
	}
	if fallback.calls != 0 {
		t.Fatal("the fallback ran despite a primary hit")
	}

	primary = &spyIdentity{}
	name, ok, err = CombinedIdentity{Primary: primary, Fallback: fallback}.Lookup("/ws/view")
	if err != nil || ok {
		t.Fatalf("both missed = %v %v %v", name, ok, err)
	}
	if primary.calls != 1 || fallback.calls != 1 {
		t.Fatalf("calls = %d %d", primary.calls, fallback.calls)
	}

	fallback.ok, fallback.name = true, want
	name, ok, err = CombinedIdentity{Primary: primary, Fallback: fallback}.Lookup("/ws/view")
	if err != nil || !ok || name != want {
		t.Fatalf("fallback hit = %v %v %v", name, ok, err)
	}

	boom := errors.New("table unreadable")
	primary = &spyIdentity{err: boom}
	fallback.calls = 0
	_, _, err = CombinedIdentity{Primary: primary, Fallback: fallback}.Lookup("/ws/view")
	if !errors.Is(err, boom) {
		t.Fatalf("primary error = %v", err)
	}
	if fallback.calls != 0 {
		t.Fatal("the fallback ran despite a primary error")
	}
}

func TestCombinedIdentityCoversStaleViewInDecide(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	view := "/ws/view"
	seedMounted(t, e, "dev", view)
	// The killed daemon left an anonymous table entry: the path
	// identity resolves nothing and the session record answers.
	table := []mountinfo.Mount{{Point: view, FSType: "fuse.fuse-overlayfs", Source: "fuse-overlayfs"}}
	prefixes := mountinfo.Prefixes{RuntimeDir: "/rt", ImagesDir: "/images"}
	combined := CombinedIdentity{
		Primary:  mountinfo.PathIdentity{Table: table, ImagesDir: "/images"},
		Fallback: Identity{Store: Store{Dir: dir}},
	}
	if _, ok, err := (mountinfo.PathIdentity{Table: table, ImagesDir: "/images"}).Lookup(view); ok || err != nil {
		t.Fatalf("the fixture primary should miss: %v %v", ok, err)
	}

	decision, err := mountinfo.Decide(view+"/deep", table, prefixes,
		mountinfo.Explicit{Full: true, Identity: combined})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Mode != mountinfo.ModeFull || decision.Name.String() != "alice/app" ||
		decision.SrcDir != view {
		t.Fatalf("decision = %+v", decision)
	}

	diverted, err := mountinfo.Decide(view+"/deep", table, prefixes,
		mountinfo.Explicit{Full: true, Identity: combined, Image: "bob/other"})
	if err != nil {
		t.Fatal(err)
	}
	if diverted.Mode != mountinfo.ModeFull || diverted.Name.String() != "bob/other" ||
		diverted.SrcDir != view+"/deep" || diverted.MountPoint != "" {
		t.Fatalf("diverted decision = %+v", diverted)
	}
	_, err = mountinfo.Decide(view+"/deep", table, prefixes,
		mountinfo.Explicit{Incr: true, Identity: combined, Image: "bob/other"})
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("incr conflict error = %v", err)
	}
}
