package cli

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/session"
)

// goldenSessionTime is the fixed clock of the session goldens; it
// matches the existing session test convention.
var goldenSessionTime = time.Date(2026, 10, 17, 3, 15, 0, 0, time.UTC)

func TestSessionListJSONMatchesGolden(t *testing.T) {
	previousNow := sessionTimeNow
	sessionTimeNow = func() time.Time { return goldenSessionTime }
	t.Cleanup(func() { sessionTimeNow = previousNow })
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	writeGoldenSessionRecords(t, home)

	stdout, stderr, _, err := runCmd(t, append(lib, "session", "list", "--json")...)
	if err != nil {
		t.Fatalf("session list --json: %v\n%s", err, stderr)
	}
	assertGolden(t, "session_list", []byte(stdout))
	var doc sessionsDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("session list --json is not parseable: %v\n%s", err, stdout)
	}
	if len(doc.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(doc.Sessions))
	}
	dev, old := doc.Sessions[0], doc.Sessions[1]
	if dev.Name != "dev" || dev.State != "active" || dev.Mounts != 1 {
		t.Fatalf("dev = %+v, want active with one mount", dev)
	}
	if dev.ClosedAt != "0001-01-01T00:00:00Z" {
		t.Fatalf("dev closed_at = %q, want the zero time as-is", dev.ClosedAt)
	}
	if old.Name != "old" || old.State != "closed" || old.Mounts != 0 {
		t.Fatalf("old = %+v, want closed without mounts", old)
	}

	human, stderr, _, err := runCmd(t, append(lib, "session", "list")...)
	if err != nil {
		t.Fatalf("session list: %v\n%s", err, stderr)
	}
	want := "" +
		"SESSION  STATE   MOUNTS  CREATED      CLOSED       LAST ACTIVITY  RM\n" +
		"dev      active  1       10-17 03:15  -            -              -\n" +
		"old      closed  0       10-16 03:15  10-16 07:15  -              -\n"
	if human != want {
		t.Fatalf("human list =\n%s\nwant:\n%s", human, want)
	}
}

func TestSessionShowJSONMatchesGolden(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	writeGoldenSessionRecords(t, home)

	stdout, stderr, _, err := runCmd(t, append(lib, "session", "show", "dev", "--json")...)
	if err != nil {
		t.Fatalf("session show --json: %v\n%s", err, stderr)
	}
	assertGolden(t, "session_show", []byte(stdout))
	var rec session.Record
	if err := json.Unmarshal([]byte(stdout), &rec); err != nil {
		t.Fatalf("session show --json is not parseable: %v\n%s", err, stdout)
	}
	if rec.Name != "dev" || rec.State != session.StateActive || len(rec.Actions) != 3 {
		t.Fatalf("record = %s/%s with %d actions, want dev/active with 3",
			rec.Name, rec.State, len(rec.Actions))
	}
	if !rec.CreatedAt.Equal(goldenSessionTime) {
		t.Fatalf("created_at = %v, want %v", rec.CreatedAt, goldenSessionTime)
	}
	if rec.Actions[1].Subdir != "docs" || rec.Actions[2].Type != session.TypeUmount {
		t.Fatalf("actions = %+v, want the subdir mount and the umount", rec.Actions)
	}

	human, stderr, _, err := runCmd(t, append(lib, "session", "show", "dev")...)
	if err != nil {
		t.Fatalf("session show: %v\n%s", err, stderr)
	}
	want, stderr, _, err := runCmd(t, append(lib, "status", "-v", "-s", "dev")...)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, stderr)
	}
	if human != want {
		t.Fatalf("human show =\n%s\nwant:\n%s", human, want)
	}
}

func TestSessionJSONKeepsErrorSemantics(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	writeGoldenSessionRecords(t, home)

	tests := []struct {
		name string
		args []string
		want int
		// wantEmptyStdout applies once cobra argument validation
		// passed: an argument-count failure prints the standard
		// usage help on stdout, the same with or without --json.
		wantEmptyStdout bool
	}{
		{name: "bad name is a usage error", args: []string{"show", "bad name", "--json"},
			want: 2, wantEmptyStdout: true},
		{name: "missing record is a runtime error", args: []string{"show", "ghost", "--json"},
			want: 1, wantEmptyStdout: true},
		{name: "show without a name is a usage error", args: []string{"show", "--json"}, want: 2},
		{name: "list with an argument is a usage error", args: []string{"list", "extra", "--json"}, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, _, err := runCmd(t, append(lib, append([]string{"session"}, tt.args...)...)...)
			if ExitCode(err) != tt.want {
				t.Fatalf("ExitCode(%v) = %d, want %d", err, ExitCode(err), tt.want)
			}
			if tt.wantEmptyStdout && stdout != "" {
				t.Fatalf("stdout = %q, want no human text mixed into the JSON stream", stdout)
			}
		})
	}
}

// writeGoldenSessionRecords drops two fixed records: an active
// session with a library mount, a torn-down file-path mount with a
// subdir, and its umount action; plus a closed session whose single
// mount was unmounted.
func writeGoldenSessionRecords(t *testing.T, home string) {
	t.Helper()
	base := goldenSessionTime
	library := session.Action{
		Type:       session.TypeMount,
		Image:      "alice/app",
		Name:       "alice/app",
		FullID:     goldenActiveSum,
		Mountpoint: "/ws/dev/app",
		Mode:       session.ModeRW,
		Status:     session.StatusMounted,
		Timestamp:  base,
	}
	fileMount := session.Action{
		Type:       session.TypeMount,
		Image:      "/images/tool.sqsh",
		Mountpoint: "/ws/dev/tool",
		Mode:       session.ModeRO,
		Subdir:     "docs",
		Status:     session.StatusUnmounted,
		Timestamp:  base.Add(time.Minute),
	}
	fileUmount := session.Action{
		Type:       session.TypeUmount,
		Image:      "/images/tool.sqsh",
		Mountpoint: "/ws/dev/tool",
		Status:     session.StatusUnmounted,
		Timestamp:  base.Add(2 * time.Minute),
	}
	dev := session.Record{
		Name:      "dev",
		State:     session.StateActive,
		Actions:   []session.Action{library, fileMount, fileUmount},
		CreatedAt: base,
	}
	for i := range dev.Actions {
		dev.Actions[i].Seq = i + 1
	}
	old := session.Record{
		Name:  "old",
		State: session.StateClosed,
		Actions: []session.Action{{
			Seq:        1,
			Type:       session.TypeMount,
			Image:      "alice/app",
			Name:       "alice/app",
			FullID:     goldenActiveSum,
			Mountpoint: "/ws/old/app",
			Mode:       session.ModeRW,
			Status:     session.StatusUnmounted,
			Timestamp:  base.Add(-24 * time.Hour),
		}},
		CreatedAt: base.Add(-24 * time.Hour),
		ClosedAt:  base.Add(-20 * time.Hour),
	}
	store := session.Store{Dir: sessionsDir(home)}
	for _, rec := range []session.Record{dev, old} {
		if err := store.Save(rec); err != nil {
			t.Fatal(err)
		}
	}
}
