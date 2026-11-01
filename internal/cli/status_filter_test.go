package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
)

func TestStatusActivityFlags(t *testing.T) {
	home, _ := withWorkspace(t)
	lib, _ := withStatusSeeds(t, home, nil)
	for _, tc := range []struct {
		flag           string
		want           string
		count, orphans int
	}{
		{"--active", "dev", 1, 1},
		{"--no-active", "old", 1, 0},
		{"--active=false", "dev", 2, 1},
		{"--no-active=false", "dev", 2, 1},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			out, stderr, _, err := runCmd(t, append(lib, "st", tc.flag, "--json")...)
			var doc statusDocument
			if err != nil || json.Unmarshal([]byte(out), &doc) != nil {
				t.Fatalf("status: %v %s %s", err, stderr, out)
			}
			if len(doc.Sessions) != tc.count || doc.Sessions[0].Name != tc.want || len(doc.OrphanViews) != tc.orphans {
				t.Fatalf("document = %+v", doc)
			}
			out, _, _, err = runCmd(t, append(lib, "status", tc.flag)...)
			if err != nil || !strings.Contains(out, tc.want+" · ") {
				t.Fatalf("text = %s, %v", out, err)
			}
		})
	}
	out, _, _, err := runCmd(t, append(lib, "status", "--session", "old", "--active", "--json")...)
	if err != nil || !strings.Contains(out, `"sessions": []`) {
		t.Fatalf("empty selection = %s, %v", out, err)
	}
}

func TestStatusActivityFlagsRejectConflictsBeforeQuery(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withSessionSeams(t, func(*session.Engine) { t.Fatal("queried conflicting flags") })
	for _, active := range []string{"--active", "--active=false"} {
		_, _, _, err := runCmd(t, append(lib, "status", active, "--no-active=false")...)
		if ExitCode(err) != 2 || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("conflict = %v", err)
		}
	}
}

func TestStatusActivityDiagnostics(t *testing.T) {
	home, _ := withWorkspace(t)
	lib, _ := withStatusSeeds(t, home, nil)
	withSessionSeams(t, func(engine *session.Engine) {
		engine.LoadTable = func() ([]mountinfo.Mount, error) { return nil, nil }
	})
	out, _, _, err := runCmd(t, append(lib, "status", "--active")...)
	if err != nil || !strings.Contains(out, "dev · active · missing-mounts") {
		t.Fatalf("missing mount text = %s, %v", out, err)
	}
	for _, tc := range []struct {
		class  session.ActivityClass
		reason string
		active bool
	}{
		{session.ActivityActive, session.ActivityMissingMounts, true},
		{session.ActivityActive, session.ActivityResidualMounts, true},
		{session.ActivityUnknown, session.ActivityUnknownReason, false},
		{session.ActivityInactive, "", false},
	} {
		view := session.View{ActivityClass: tc.class}
		if tc.reason != "" {
			view.ActivityReasons = []string{tc.reason}
		}
		entry := newStatusSession(view)
		if entry.Active != tc.active || entry.ActivityReasons == nil || len(entry.ActivityReasons) != len(view.ActivityReasons) {
			t.Fatalf("entry = %+v", entry)
		}
		var title strings.Builder
		writeStatusTitle(&title, view, goldenSessionTime)
		if tc.reason != "" && (!strings.Contains(title.String(), tc.reason) || entry.ActivityReasons[0] != tc.reason) {
			t.Fatalf("diagnostic mismatch: %s %+v", title.String(), entry)
		}
	}
	help, _, _, err := runCmd(t, "status", "--help")
	if err != nil || !strings.Contains(help, "--active") || !strings.Contains(help, "--no-active") {
		t.Fatalf("help = %s, %v", help, err)
	}
}
