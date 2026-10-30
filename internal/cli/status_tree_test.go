package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/session"
)

func TestStatusDetailsCompactTree(t *testing.T) {
	now := goldenSessionTime
	action := session.Action{Image: "alice/app", Name: "alice/app", TipID: strings.Repeat("a", 64), Mode: "rw", Status: "mounted", Mountpoint: "/ws/app"}
	mount := session.MountView{ActionView: session.ActionView{Action: action, Health: "healthy"}, OverlayUpper: "/rt/upper", OverlayLowers: []string{"/rt/lower-0", "/rt/lower-1"}}
	mount.Layers = []session.LayerView{{Version: strings.Repeat("1", 64), Kind: "full", Mountpoint: "/rt/lower-1", Available: true}, {Version: action.TipID, Kind: "incremental", Mountpoint: "/rt/lower-0", Available: true}}
	readonly := mount
	readonly.Mode, readonly.Mountpoint, readonly.OverlayUpper, readonly.OverlayLowers = "ro", "/ws/tools", "", nil
	readonly.TipID = strings.Repeat("2", 64)
	readonly.Layers = []session.LayerView{{Version: strings.Repeat("2", 64), Kind: "full", Mountpoint: "/ws/tools", Available: true}}
	shadow := readonly
	shadow.Mode, shadow.Shadow, shadow.Health = "rw", "/rt/upper.shadow", "stale"
	shadow.TipID = action.TipID
	shadow.Layers = []session.LayerView{{Version: action.TipID, Kind: "full"}}
	shadow.Diagnostics = []string{"missing\nmetadata"}
	view := session.View{Summary: session.Summary{Name: "dev", State: "active", Created: now, Activity: session.ActivityView{At: now}, RemoveOnClose: true}, Current: true, Mounts: []session.MountView{mount, readonly, shadow}}
	overview := session.Overview{Sessions: []session.View{view, {Summary: session.Summary{Name: "old", State: "closed", Created: now, Closed: now}}, {Summary: session.Summary{Name: "busy"}, Locked: true}}, Orphans: []session.Orphan{{Point: "/ws/orphan", FSType: "overlay", RuntimeDir: "/rt/orphan"}}}
	var out bytes.Buffer
	if err := writeStatusDetails(&out, overview, now, "/ws"); err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "status_tree", out.Bytes())
	mount.ServedTip = strings.Repeat("b", 64)
	mount.Layers[1].Version = strings.Repeat("1", 63) + "2"
	overview.Sessions[0].Mounts = []session.MountView{mount}
	out.Reset()
	if err := writeStatusDetails(&out, overview, now, "/ws"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"alice/app:bbbbbbbbbb", "Desired: aaaaaaaaaa · Served: bbbbbbbbbb", "Short IDs collide; use status --json"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, &out)
		}
	}
}

func TestStatusDetailsEmptyAndWriterFailure(t *testing.T) {
	var out bytes.Buffer
	if err := writeStatusDetails(&out, session.Overview{}, goldenSessionTime, "/"); err != nil || out.String() != "no sessions recorded\n" {
		t.Fatalf("empty output %q, %v", &out, err)
	}
	failure := errors.New("broken pipe")
	if err := writeStatusDetails(failWriter{failure}, session.Overview{}, goldenSessionTime, "/"); !errors.Is(err, failure) {
		t.Fatalf("writer error = %v", err)
	}
}
