//go:build linux

package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/session"
)

func TestStatusReconcilesRealMounts(t *testing.T) {
	requireMountTestEnv(t)
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	sessionsRoot := sessionsDir(home)
	buildFullImage(t, lib, "alice/app", subdirSource(t))
	t.Setenv(sessionEnvVar, "dev")

	view1 := filepath.Join(work, "view1")
	view2 := filepath.Join(work, "view2")
	deferForceUmount(t, sessionsRoot, view1, view2)
	mustSessionNew(t, lib, "dev")
	mountOK(t, lib, "mounted "+view1+"\n",
		"mount", "--session", "dev", "alice/app", view1)
	mountOK(t, lib, "mounted "+view2+"\n",
		"mount", "--session", "dev", "alice/app", view2)

	// Both views serve: the overview shows the active session with
	// two healthy mounts and no orphans.
	stdout := mustCmd(t, append(lib, "status")...)
	assertStatusOverview(t, stdout, "dev · active", 2, "mounted · healthy", 0)
	if !strings.Contains(stdout, " current") {
		t.Fatalf("stdout = %q, want the environment session marked current", stdout)
	}
	doc := statusOverviewJSON(t, lib)
	if doc.CurrentSession != "dev" || len(doc.Sessions) != 1 {
		t.Fatalf("doc = %+v, want the dev session", doc)
	}
	dev := doc.Sessions[0]
	if dev.State != session.StateActive || !dev.Current || dev.Locked ||
		len(dev.Actions) != 2 || len(doc.OrphanViews) != 0 {
		t.Fatalf("dev = %+v, want active and current with two actions", dev)
	}
	for i, action := range dev.Actions {
		want := []string{view1, view2}[i]
		if action.Mountpoint != want || action.Health != session.HealthHealthy ||
			action.Status != session.StatusMounted {
			t.Fatalf("action %d = %+v, want the healthy mount at %s", i, action, want)
		}
	}

	// A directory inside the merged view selects the complete session;
	// internal runtime paths do not select its mount directory.
	deep := filepath.Join(view1, "sub", "deep")
	stdout = mustCmd(t, append(lib, "status", deep)...)
	if !strings.HasPrefix(stdout, "dev · active") {
		t.Fatalf("deep path status = %q", stdout)
	}
	pathDoc := statusDirectoryJSON(t, lib, deep)
	if len(pathDoc.Sessions) != 1 || pathDoc.Sessions[0].Name != "dev" || len(pathDoc.Sessions[0].Mounts) != 2 {
		t.Fatalf("directory doc = %+v", pathDoc)
	}
	entry, ok := entryAt(realTable(t), view1)
	if !ok || entry.Upper == "" {
		t.Fatalf("no writable overlay entry at %s: %+v", view1, entry)
	}
	stdout = mustCmd(t, append(lib, "status", entry.Upper)...)
	if stdout != "no sessions recorded\n" {
		t.Fatalf("runtime upper is not a mount directory: %q", stdout)
	}
	pathDoc = statusDirectoryJSON(t, lib, entry.Upper)
	if len(pathDoc.Sessions) != 0 {
		t.Fatalf("runtime directory doc = %+v", pathDoc)
	}

	// Kill the second daemon: the overview and the path form both
	// report the dead view stale while the healthy one stays.
	killDaemon(t, findDaemon(t, "fuse-overlayfs", view2))
	waitOverlayDead(t, view2)
	stdout = mustCmd(t, append(lib, "status")...)
	assertStatusOverview(t, stdout, "dev · active", 2, "", 0)
	if !strings.Contains(stdout, "mounted · healthy") {
		t.Fatalf("stdout = %q, want the surviving view healthy", stdout)
	}
	if !strings.Contains(stdout, "mounted · stale") {
		t.Fatalf("stdout = %q, want the killed view marked stale", stdout)
	}
	pathDoc = statusDirectoryJSON(t, lib, view2)
	if len(pathDoc.Sessions) != 1 || pathDoc.Sessions[0].Actions[1].Health != session.HealthStale {
		t.Fatalf("stale path doc = %+v", pathDoc)
	}

	// Close tears everything down: the record shows closed with
	// unmounted actions and no orphan views remain.
	if out := mustCmd(t, append(lib, "session", "close", "dev")...); out != "closed dev\n" {
		t.Fatalf("close output = %q", out)
	}
	rec := loadSessionRecord(t, sessionsDir(home), "dev")
	if rec.State != session.StateClosed {
		t.Fatalf("record state = %s, want closed", rec.State)
	}
	for _, action := range rec.Actions {
		if action.Type == session.TypeMount && action.Status != session.StatusUnmounted {
			t.Fatalf("action %+v is not unmounted after close", action)
		}
	}
	stdout = mustCmd(t, append(lib, "status")...)
	assertStatusOverview(t, stdout, "dev · closed", 2, "", 0)
	doc = statusOverviewJSON(t, lib)
	if len(doc.Sessions) != 1 || doc.Sessions[0].State != session.StateClosed ||
		len(doc.Sessions[0].Actions) != 2 || len(doc.OrphanViews) != 0 {
		t.Fatalf("closed doc = %+v, want the closed record without orphans", doc)
	}
}

func TestStatusUncoveredRealDirectoryReturnsEmpty(t *testing.T) {
	requireMountTestEnv(t)
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	stdout, stderr, _, err := runCmd(t, append(lib, "status", work, "--json")...)
	if err != nil || !strings.Contains(stdout, `"sessions": []`) {
		t.Fatalf("empty result = %s %v %s", stdout, err, stderr)
	}
}

// assertStatusOverview checks the block structure of the human
// overview: the session header, the number of mount lines with
// their health marks, and the orphan line count.
func assertStatusOverview(t *testing.T, stdout, header string, actions int, health string, orphans int) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if !strings.HasPrefix(lines[0], header) {
		t.Fatalf("header = %q, want prefix %q", lines[0], header)
	}
	seenActions := 0
	seenOrphans := 0
	for _, line := range lines[1:] {
		switch {
		case strings.HasPrefix(line, "  📁 "):
			seenActions++
			if health != "" && !strings.Contains(line, health) {
				t.Fatalf("mount line = %q, want it to contain %q", line, health)
			}
		case strings.HasPrefix(line, "orphan "):
			seenOrphans++
		}
	}
	if seenActions != actions {
		t.Fatalf("mount lines = %d, want %d\n%s", seenActions, actions, stdout)
	}
	if seenOrphans != orphans {
		t.Fatalf("orphan lines = %d, want %d\n%s", seenOrphans, orphans, stdout)
	}
}

// statusOverviewJSON runs the real status --json overview and parses
// the document.
func statusOverviewJSON(t *testing.T, lib []string) statusDocument {
	t.Helper()
	stdout := mustCmd(t, append(lib, "status", "--json")...)
	var doc statusDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("status --json is not parseable: %v\n%s", err, stdout)
	}
	return doc
}

// statusDirectoryJSON runs status <directory> --json and parses the
// document.
func statusDirectoryJSON(t *testing.T, lib []string, path string) statusDocument {
	t.Helper()
	stdout := mustCmd(t, append(lib, "status", path, "--json")...)
	var doc statusDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("status directory --json is not parseable: %v\n%s", err, stdout)
	}
	return doc
}
