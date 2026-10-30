package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
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
		engine.ImagesDir = "/images"
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
	if old.State != "closed" || len(old.Actions) != 1 {
		t.Fatalf("old = %+v, want closed historical actions", old)
	}
	orphan := doc.OrphanViews[0]
	if orphan.Point != "/ws/orphan" || orphan.FSType != "fuse.fuse-overlayfs" ||
		orphan.RuntimeDir != "/sessions/orphan/runtime/m1" {
		t.Fatalf("orphan = %+v", orphan)
	}
}

func TestStatusDirectoryJSONMatchesGolden(t *testing.T) {
	home, _ := withWorkspace(t)
	lib := statusJSONLib(home)
	withStatusJSONSeams(t, map[string]bool{"/ws/dev/app": true})
	writeGoldenSessionRecords(t, home)

	stdout, stderr, _, err := runCmd(t,
		append(lib, "status", "/ws/dev/app/src/main.go", "--json")...)
	if err != nil {
		t.Fatalf("status directory --json: %v\n%s", err, stderr)
	}
	assertGolden(t, "status_path", []byte(stdout))
	var doc statusDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("status directory --json is not parseable: %v\n%s", err, stdout)
	}
	if len(doc.Sessions) != 1 || doc.Sessions[0].Name != "dev" || len(doc.Sessions[0].Mounts) != 2 || doc.Sessions[0].Actions[0].Health != "healthy" {
		t.Fatalf("doc = %+v", doc)
	}
}

func TestStatusDirectoryJSONStaleAndEmpty(t *testing.T) {
	home, _ := withWorkspace(t)
	lib := statusJSONLib(home)
	withStatusJSONSeams(t, map[string]bool{})
	writeGoldenSessionRecords(t, home)

	stdout, stderr, _, err := runCmd(t, append(lib, "status", "/ws/dev/app", "--json")...)
	if err != nil {
		t.Fatalf("status directory --json: %v\n%s", err, stderr)
	}
	var doc statusDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("status directory --json is not parseable: %v\n%s", err, stdout)
	}
	if len(doc.Sessions) != 1 || doc.Sessions[0].Actions[0].Health != "stale" {
		t.Fatalf("doc = %+v, want the stale mount", doc)
	}

	stdout, _, _, err = runCmd(t, append(lib, "status", "/nowhere", "--json")...)
	if err != nil || strings.Contains(stdout, "null") || !strings.Contains(stdout, `"sessions": []`) {
		t.Fatalf("empty directory JSON = %s, %v", stdout, err)
	}
}

func TestStatusMountJSONPreservesCompleteIdentities(t *testing.T) {
	id := strings.Repeat("a", 64)
	mount := session.MountView{ActionView: session.ActionView{Action: session.Action{Seq: 1, FullID: id, TipID: id, ServedID: id, ServedTip: id}}}
	mount.Layers = []session.LayerView{{Image: "alice/app", Version: id, Kind: "full", Mountpoint: "/runtime/lower-0", Available: true}}
	entry := newStatusMount(mount)
	if entry.FullID != id || entry.TipID != id || entry.ServedID != id || entry.ServedTip != id || entry.Layers[0].Version != id || entry.Layers[0].Mountpoint != "/runtime/lower-0" {
		t.Fatalf("mount JSON = %+v", entry)
	}
	if entry.OverlayLowers == nil || entry.Diagnostics == nil || entry.Layers == nil {
		t.Fatalf("null array in %+v", entry)
	}
}
