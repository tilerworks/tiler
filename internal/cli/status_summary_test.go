package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/session"
)

func TestStatusSummaryPreservesDiagnosticsWithoutInternalDetails(t *testing.T) {
	mount := session.MountView{
		ActionView:   session.ActionView{Action: session.Action{Name: "alice/app", TipID: strings.Repeat("a", 64), Mountpoint: "/ws/app", Mode: "rw", Status: session.StatusMounted}, Health: session.HealthHealthy},
		OverlayUpper: "/runtime/upper", Shadow: "/runtime/upper.shadow",
		Layers: []session.LayerView{{Version: strings.Repeat("a", 64), Mountpoint: "/runtime/lower", Available: true}},
	}
	stale := mount
	stale.Health, stale.Mountpoint, stale.Diagnostics = session.HealthStale, "/ws/stale", []string{"missing metadata"}
	unmounted := mount
	unmounted.Status, unmounted.Health, unmounted.Mountpoint = session.StatusUnmounted, "", "/ws/old"
	overview := session.Overview{
		Sessions: []session.View{
			{Summary: session.Summary{Name: "dev", State: session.StateActive}, Current: true, Mounts: []session.MountView{mount, stale}},
			{Summary: session.Summary{Name: "rebooted", State: session.StateActive}, ActivityReasons: []string{session.ActivityMissingMounts}},
			{Summary: session.Summary{Name: "leftover", State: session.StateClosed}, ActivityReasons: []string{session.ActivityResidualMounts}, Mounts: []session.MountView{unmounted}},
			{Summary: session.Summary{Name: "busy"}, Locked: true, ActivityReasons: []string{session.ActivityUnknownReason}},
		},
		Orphans: []session.Orphan{{Point: "/ws/orphan", FSType: "fuse.overlay", RuntimeDir: "/runtime/orphan"}},
	}
	var out bytes.Buffer
	if err := writeStatusOutput(&out, overview, goldenSessionTime, "/ws", false); err != nil {
		t.Fatal(err)
	}
	want := "dev · active · current\n" +
		"  alice/app:aaaaaaaaaa → app  [rw · healthy]\n" +
		"  alice/app:aaaaaaaaaa → stale  [rw · stale · warning]\n\n" +
		"rebooted · active · missing-mounts\n\n" +
		"leftover · closed · residual-mounts\n" +
		"  alice/app:aaaaaaaaaa → old  [rw · unmounted]\n\n" +
		"busy · locked · activity-unknown\n" +
		"orphan orphan  [fuse.overlay]\n"
	if out.String() != want {
		t.Fatalf("summary:\n%s\nwant:\n%s", &out, want)
	}
	// Layer IDs are also checked for ambiguity when their tree is hidden.
	overview.Sessions[0].Mounts[1].Layers = []session.LayerView{{Version: strings.Repeat("a", 63) + "b"}}
	out.Reset()
	if err := writeStatusOutput(&out, overview, goldenSessionTime, "/ws", false); err != nil || !strings.Contains(out.String(), "Short IDs collide; use status --json") {
		t.Fatalf("collision: %v %s", err, &out)
	}
	failure := errors.New("broken pipe")
	if err := writeStatusOutput(failWriter{failure}, overview, goldenSessionTime, "/ws", false); !errors.Is(err, failure) {
		t.Fatalf("write error: %v", err)
	}
}

func TestStatusVerboseFlagsAndJSONCompatibility(t *testing.T) {
	home, _ := withWorkspace(t)
	lib, _ := withStatusSeeds(t, home, map[string]bool{"/ws/dev": true})
	previous := sessionTimeNow
	sessionTimeNow = func() time.Time { return goldenSessionTime }
	t.Cleanup(func() { sessionTimeNow = previous })
	var summary, details, document string
	for _, entry := range [][]string{{"status"}, {"st"}, {"session", "status"}} {
		for _, flag := range []string{"", "-v", "--verbose", "--verbose=false"} {
			args := append(append([]string{}, lib...), entry...)
			args = append(args, "--active")
			if flag != "" {
				args = append(args, flag)
			}
			out, stderr, _, err := runCmd(t, args...)
			if err != nil {
				t.Fatalf("%v: %v %s", args, err, stderr)
			}
			verbose := flag == "-v" || flag == "--verbose"
			if strings.Contains(out, "Created:") != verbose || strings.Contains(out, "📁") != verbose {
				t.Fatalf("verbosity %v: %s", args, out)
			}
			expected := &summary
			if verbose {
				expected = &details
			}
			if *expected != "" && *expected != out {
				t.Fatalf("entrypoint output differs: %s", out)
			}
			*expected = out
			out, stderr, _, err = runCmd(t, append(args, "--json")...)
			if err != nil {
				t.Fatalf("JSON: %v %s", err, stderr)
			}
			if document != "" && document != out {
				t.Fatalf("JSON differs with verbosity: %s", out)
			}
			document = out
		}
	}
	if !strings.Contains(details, "[rw · mounted · healthy]") {
		t.Fatalf("verbose lost mount details: %s", details)
	}
}
