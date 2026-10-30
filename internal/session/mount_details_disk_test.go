package session

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

func TestQueryDiskDetailsShareSnapshotAndValidateAttachment(t *testing.T) {
	e, dir := lookupEngine(t, nil, nil)
	e.Health, e.SessionsDir = nil, dir
	seedMounted(t, e, "dev", "/app")
	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	a := &rec.Actions[0]
	a.Image, a.Name, a.FullID, a.TipID, a.RuntimeID = "/app.dmg", "", "", "digest", "m1"
	if err := e.Save(rec); err != nil {
		t.Fatal(err)
	}
	shadow := filepath.Join(dir, "dev", "runtime", "m1", "upper.shadow")
	images := []DiskImage{{Point: "/app", Image: a.Image, Shadow: shadow}}
	calls := 0
	e.LoadDiskImages = func() ([]DiskImage, error) { calls++; return images, nil }
	e.LoadTable = func() ([]mountinfo.Mount, error) {
		t.Fatal("disk query loaded a Linux mount table")
		return nil, nil
	}
	for _, tc := range []struct {
		mode, image, shadow, health string
		available                   bool
	}{
		{ModeRW, a.Image, shadow, HealthHealthy, true},
		{ModeRO, a.Image, "", HealthHealthy, true},
		{ModeRW, "/foreign.dmg", shadow, HealthStale, false},
		{ModeRW, a.Image, "/foreign.shadow", HealthStale, false},
	} {
		a.Mode = tc.mode
		if err := e.Save(rec); err != nil {
			t.Fatal(err)
		}
		images[0].Image, images[0].Shadow = tc.image, tc.shadow
		calls = 0
		got, err := e.Query("dev", Query{Session: "dev"})
		if err != nil || calls != 1 {
			t.Fatalf("snapshot calls=%d, err=%v", calls, err)
		}
		mount := got.Sessions[0].Mounts[0]
		if mount.Health != tc.health || mount.Layers[0].Available != tc.available || mount.OverlayUpper != "" || len(mount.OverlayLowers) != 0 {
			t.Fatalf("disk details = %+v", mount)
		}
		if tc.available && (mount.DiskImage != tc.image || mount.Shadow != tc.shadow || mount.Layers[0].Mountpoint != "/app") {
			t.Fatalf("attached details = %+v", mount)
		}
		if !tc.available && (mount.DiskImage != "" || mount.Shadow != "") {
			t.Fatalf("foreign attachment reported as owned: %+v", mount)
		}
	}
	images = nil
	got, err := e.Query("", Query{})
	if err != nil || got.Sessions[0].Mounts[0].Layers[0].Available {
		t.Fatalf("vanished disk details = %+v, %v", got, err)
	}
	boom := errors.New("disk snapshot failed")
	e.LoadDiskImages = func() ([]DiskImage, error) { return nil, boom }
	if _, err := e.Query("", Query{}); !errors.Is(err, boom) {
		t.Fatalf("snapshot error = %v", err)
	}
}
