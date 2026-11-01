package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

func TestQueryClassifiesMountEvidenceWithoutHealth(t *testing.T) {
	for _, tc := range []struct {
		name, state, kind, reason string
		want                      ActivityClass
	}{
		{"missing active", StateActive, "none", ActivityMissingMounts, ActivityActive},
		{"closed empty", StateClosed, "none", "", ActivityInactive},
		{"internal active", StateActive, "internal", ActivityResidualMounts, ActivityActive},
		{"internal closed", StateClosed, "internal", ActivityResidualMounts, ActivityActive},
		{"merged unreadable", StateActive, "merged", "", ActivityActive},
		{"closed merged", StateClosed, "merged", ActivityResidualMounts, ActivityActive},
		{"direct unreadable", StateActive, "direct", "", ActivityActive},
		{"unrelated source", StateClosed, "foreign", "", ActivityInactive},
		{"unrelated filesystem", StateClosed, "foreign-fs", "", ActivityInactive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, root := lookupEngine(t, nil, nil)
			e.SessionsDir = root
			point := filepath.Join(t.TempDir(), "view")
			seedMounted(t, e, "dev", point)
			rec, err := e.Load("dev")
			if err != nil {
				t.Fatal(err)
			}
			rec.Actions[0].RuntimeID = "m1"
			if tc.kind == "direct" || tc.kind == "foreign" {
				rec.Actions[0].Name, rec.Actions[0].Image, rec.Actions[0].Mode = "", "/images/payload", ModeRO
			}
			if tc.state == StateClosed {
				rec.State, rec.ClosedAt = StateClosed, testNow
				rec.Actions[0].Status = StatusUnmounted
			}
			if err := e.Save(rec); err != nil {
				t.Fatal(err)
			}
			runtime := filepath.Join(root, "dev", "runtime", "m1")
			var table []mountinfo.Mount
			switch tc.kind {
			case "internal":
				table = []mountinfo.Mount{{Point: filepath.Join(runtime, "lower-0"), FSType: "fuse", Source: rec.Actions[0].Image}}
			case "merged":
				table = []mountinfo.Mount{overlayEntry(point, runtime)}
			case "direct":
				table = []mountinfo.Mount{{Point: point, FSType: "fuse", Source: rec.Actions[0].Image}}
			case "foreign":
				table = []mountinfo.Mount{{Point: point, FSType: "fuse", Source: "/other/image"}}
			case "foreign-fs":
				table = []mountinfo.Mount{{Point: filepath.Join(runtime, "lower-0"), FSType: "ext4"}}
			}
			loads := 0
			e.LoadTable = func() ([]mountinfo.Mount, error) { loads++; return table, nil }
			got, err := e.Query("", Query{})
			if err != nil {
				t.Fatal(err)
			}
			view := got.Sessions[0]
			if loads != 1 || view.ActivityClass != tc.want {
				t.Fatalf("loads=%d, activity=%s; want 1, %s", loads, view.ActivityClass, tc.want)
			}
			if tc.reason == "" && len(view.ActivityReasons) != 0 || tc.reason != "" && (len(view.ActivityReasons) != 1 || view.ActivityReasons[0] != tc.reason) {
				t.Fatalf("reasons=%v; want %q", view.ActivityReasons, tc.reason)
			}
		})
	}
}

func TestActivityRuntimeOwnerResolvesSymlinksAndBoundaries(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	e := &Engine{SessionsDir: alias}
	if got := e.activityRuntimeOwner(filepath.Join(root, "dev", "runtime", "m1", "lower-0")); got != "dev" {
		t.Fatalf("owner = %q", got)
	}
	if got := e.activityRuntimeOwner(filepath.Join(root+"-other", "dev", "runtime", "m1")); got != "" {
		t.Fatalf("unrelated owner = %q", got)
	}
}
