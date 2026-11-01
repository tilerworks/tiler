package session

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

func TestQueryDiskActivityUsesOneAttachmentSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, state, kind, reason string
		want                      ActivityClass
	}{
		{"missing", StateActive, "none", ActivityMissingMounts, ActivityActive},
		{"closed empty", StateClosed, "none", "", ActivityInactive},
		{"attached", StateActive, "user", "", ActivityActive},
		{"closed attached", StateClosed, "user", ActivityResidualMounts, ActivityActive},
		{"internal remnants", StateClosed, "internal", ActivityResidualMounts, ActivityActive},
		{"foreign image", StateClosed, "foreign", "", ActivityInactive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, root := lookupEngine(t, nil, nil)
			e.SessionsDir, e.Health = root, nil
			point := filepath.Join(t.TempDir(), "view")
			seedMounted(t, e, "dev", point)
			rec, err := e.Load("dev")
			if err != nil {
				t.Fatal(err)
			}
			action := &rec.Actions[0]
			action.RuntimeID, action.Name, action.Image = "m1", "", "/images/base.dmg"
			if tc.state == StateClosed {
				rec.State, rec.ClosedAt, action.Status = StateClosed, testNow, StatusUnmounted
			}
			if err := e.Save(rec); err != nil {
				t.Fatal(err)
			}
			attached := DiskImage{Point: point, Image: action.Image, Shadow: recordedShadow("dev", *action, root)}
			switch tc.kind {
			case "internal":
				attached.Point = filepath.Join(root, "dev", "runtime", "m1", "lower-0")
			case "foreign":
				attached.Image, attached.Shadow = "/other.dmg", ""
			}
			loads := 0
			e.LoadDiskImages = func() ([]DiskImage, error) {
				loads++
				if tc.kind == "none" {
					return nil, nil
				}
				return []DiskImage{attached}, nil
			}
			got, err := e.Query("", Query{})
			if err != nil {
				t.Fatal(err)
			}
			view := got.Sessions[0]
			if loads != 1 || view.ActivityClass != tc.want || fmt.Sprint(view.ActivityReasons) != fmt.Sprint(reasonSlice(tc.reason)) {
				t.Fatalf("loads=%d class=%s reasons=%v", loads, view.ActivityClass, view.ActivityReasons)
			}
		})
	}
}

func reasonSlice(reason string) []string {
	if reason == "" {
		return nil
	}
	return []string{reason}
}

func TestQueryUnavailableTableDoesNotInventMissingMounts(t *testing.T) {
	e, _ := lookupEngine(t, nil, nil)
	seedMounted(t, e, "dev", "/view")
	seedMounted(t, e, "old", "/old")
	rec, err := e.Load("old")
	if err != nil {
		t.Fatal(err)
	}
	rec.State, rec.ClosedAt = StateClosed, testNow
	if err := e.Save(rec); err != nil {
		t.Fatal(err)
	}
	loads := 0
	e.LoadTable = func() ([]mountinfo.Mount, error) { loads++; return nil, mountinfo.ErrUnavailable }
	got, err := e.Query("", Query{})
	if err != nil {
		t.Fatal(err)
	}
	if loads != 1 || got.Sessions[0].ActivityClass != ActivityActive || len(got.Sessions[0].ActivityReasons) != 0 || got.Sessions[1].ActivityClass != ActivityUnknown {
		t.Fatalf("loads=%d sessions=%+v", loads, got.Sessions)
	}
}
