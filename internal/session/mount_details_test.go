package session

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

func TestQueryMountDetailsUseOneSnapshotAndPreserveLayerOrders(t *testing.T) {
	e, dir := lookupEngine(t, nil, map[string]bool{"/app": true})
	e.SessionsDir, e.ImagesDir = dir, t.TempDir()
	m := sessionVersionManifest()
	saveSessionVersionManifest(t, e.ImagesDir, m)
	rec := versionRecord(t)
	a := &rec.Actions[0]
	a.Mountpoint, a.FullID, a.TipID = "/app", m.Images[0].ID, m.Images[2].ID
	a.ServedID, a.ServedTip = "", ""
	if err := e.Save(rec); err != nil {
		t.Fatal(err)
	}
	members, err := a.VersionStack(e.ImagesDir, false)
	if err != nil {
		t.Fatal(err)
	}
	runtime := filepath.Join(dir, rec.Name, "runtime", a.RuntimeID)
	lowers := []string{filepath.Join(runtime, "lower-0"), filepath.Join(runtime, "lower-1"), filepath.Join(runtime, "lower-2")}
	table := []mountinfo.Mount{{Point: "/app", FSType: "overlay", Upper: filepath.Join(runtime, "upper"), Lowers: lowers}}
	for i, member := range members {
		table = append(table, mountinfo.Mount{Point: lowers[2-i], Source: member.Path, FSType: "fuse"})
	}
	calls := 0
	e.LoadTable = func() ([]mountinfo.Mount, error) { calls++; return table, nil }
	got, err := e.Query("", Query{})
	if err != nil || calls != 1 {
		t.Fatalf("snapshot calls=%d, err=%v", calls, err)
	}
	mount := got.Sessions[0].Mounts[0]
	if mount.Seq != a.Seq || mount.OverlayUpper != table[0].Upper || !reflect.DeepEqual(mount.OverlayLowers, lowers) {
		t.Fatalf("overlay = %+v", mount)
	}
	for i, layer := range mount.Layers {
		if layer.Version != m.Images[i].ID || layer.Kind != m.Images[i].Kind || layer.Mountpoint != lowers[2-i] || !layer.Available {
			t.Fatalf("layer %d = %+v", i, layer)
		}
	}
	table[1].Source = "/foreign.sqsh"
	got, err = e.Query("", Query{})
	if err != nil || got.Sessions[0].Mounts[0].Layers[0].Available {
		t.Fatalf("foreign lower source = %+v, %v", got, err)
	}
	table = table[:1]
	got, err = e.Query("", Query{})
	if err != nil || got.Sessions[0].Mounts[0].Layers[0].Available {
		t.Fatalf("vanished internal mount = %+v, %v", got, err)
	}
}

func TestFileMountDetailsRejectForeignSourcesAndKeepMissingMetadata(t *testing.T) {
	e, dir := lookupEngine(t, nil, nil)
	e.SessionsDir = dir
	a := Action{Type: TypeMount, Image: "/image.sqsh", TipID: "digest", Mountpoint: "/app", Mode: ModeRO, RuntimeID: "m1"}
	view := View{Summary: Summary{Name: "dev"}, Actions: []ActionView{{Action: a}}}
	for _, source := range []string{a.Image, "/foreign.sqsh"} {
		got := e.mountDetails(view, []mountinfo.Mount{{Point: a.Mountpoint, Source: source}})[0]
		if got.Layers[0].Available != (source == a.Image) || got.Layers[0].Version != a.TipID {
			t.Fatalf("source %s: %+v", source, got)
		}
	}
	view.Actions[0].Name = "alice/missing"
	got := e.mountDetails(view, nil)[0]
	if len(got.Diagnostics) == 0 || got.Mountpoint != "/app" {
		t.Fatalf("missing metadata lost mount: %+v", got)
	}
}
