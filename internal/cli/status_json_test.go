package cli

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
)

// statusJSONLib returns command arguments that pin the runtime tree
// to a fixed absolute path, so the golden files never carry the
// temporary home directory.
func statusJSONLib(home string) []string {
	return []string{
		"--images-dir", filepath.Join(home, "lib", "images"),
		"--runtime-dir", "/rt",
		"--sessions-dir", sessionsDir(home),
	}
}

// withStatusJSONSeams installs a fixed fake mount table: the view
// the dev record claims plus one unclaimed combination. SessionsDir
// is pinned to /sessions so orphan runtime paths stay golden-stable
// without writing into that absolute path.
func withStatusJSONSeams(t *testing.T, healthy map[string]bool) {
	t.Helper()
	table := []mountinfo.Mount{
		statusView("/ws/dev/app", "/sessions/dev/runtime/m1"),
		statusView("/ws/orphan", "/sessions/orphan/runtime/m1"),
	}
	withSessionSeams(t, func(engine *session.Engine) {
		engine.SessionsDir = "/sessions"
		engine.LoadTable = func() ([]mountinfo.Mount, error) { return table, nil }
		engine.Health = func(point string) bool { return healthy[point] }
	})
}

func TestStatusOverviewJSONMatchesGolden(t *testing.T) {
	home, _ := withWorkspace(t)
	lib := statusJSONLib(home)
	withStatusJSONSeams(t, map[string]bool{"/ws/dev/app": true})
	writeGoldenSessionRecords(t, home)
	t.Setenv("TILER_SESSION_ID", "dev")

	stdout, stderr, _, err := runCmd(t, append(lib, "status", "--json")...)
	if err != nil {
		t.Fatalf("status --json: %v\n%s", err, stderr)
	}
	assertGolden(t, "status_overview", []byte(stdout))
	var doc statusDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("status --json is not parseable: %v\n%s", err, stdout)
	}
	if doc.CurrentSession != "dev" {
		t.Fatalf("current_session = %q, want the verbatim environment value",
			doc.CurrentSession)
	}
	if len(doc.Sessions) != 2 || len(doc.OrphanViews) != 1 {
		t.Fatalf("doc = %+v, want two sessions and one orphan", doc)
	}
	dev, old := doc.Sessions[0], doc.Sessions[1]
	if !dev.Current || dev.Locked || len(dev.Actions) != 3 {
		t.Fatalf("dev = %+v, want the current record with three actions", dev)
	}
	if dev.Actions[0].Health != "healthy" || dev.Actions[1].Health != "" ||
		dev.Actions[2].Health != "" {
		t.Fatalf("health marks = %+v, want only the live mount annotated", dev.Actions)
	}
	if old.State != "closed" || len(old.Actions) != 0 {
		t.Fatalf("old = %+v, want closed without actions", old)
	}
	orphan := doc.OrphanViews[0]
	if orphan.Point != "/ws/orphan" || orphan.FSType != "fuse.fuse-overlayfs" ||
		orphan.RuntimeDir != "/sessions/orphan/runtime/m1" {
		t.Fatalf("orphan = %+v", orphan)
	}
}

func TestStatusPathJSONMatchesGolden(t *testing.T) {
	home, _ := withWorkspace(t)
	lib := statusJSONLib(home)
	withStatusJSONSeams(t, map[string]bool{"/ws/dev/app": true})
	writeGoldenSessionRecords(t, home)

	stdout, stderr, _, err := runCmd(t,
		append(lib, "status", "/ws/dev/app/src/main.go", "--json")...)
	if err != nil {
		t.Fatalf("status path --json: %v\n%s", err, stderr)
	}
	assertGolden(t, "status_path", []byte(stdout))
	var doc statusPathDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("status path --json is not parseable: %v\n%s", err, stdout)
	}
	if doc.Path != "/ws/dev/app/src/main.go" || doc.Session != "dev" ||
		doc.State != "active" || doc.Mountpoint != "/ws/dev/app" || doc.Stale {
		t.Fatalf("doc = %+v", doc)
	}
	if doc.Action == nil || doc.Action.Seq != 1 || doc.Action.Health != "healthy" {
		t.Fatalf("action = %+v, want the healthy covering mount", doc.Action)
	}
}

func TestStatusPathJSONStaleAndUncovered(t *testing.T) {
	home, _ := withWorkspace(t)
	lib := statusJSONLib(home)
	withStatusJSONSeams(t, map[string]bool{})
	writeGoldenSessionRecords(t, home)

	stdout, stderr, _, err := runCmd(t, append(lib, "status", "/ws/dev/app", "--json")...)
	if err != nil {
		t.Fatalf("status path --json: %v\n%s", err, stderr)
	}
	var doc statusPathDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("status path --json is not parseable: %v\n%s", err, stdout)
	}
	if !doc.Stale || doc.Action == nil || doc.Action.Health != "stale" {
		t.Fatalf("doc = %+v, want the stale covering view", doc)
	}

	// An uncovered path stays a runtime error: the report goes to
	// stderr and stdout carries no document.
	stdout, _, _, err = runCmd(t, append(lib, "status", "/nowhere", "--json")...)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want no document for an uncovered path", stdout)
	}
}
