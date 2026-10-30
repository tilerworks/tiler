package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
)

// statusView is one writable merged view with its upper under the
// runtime combination directory.
func statusView(point, runtimeDir string) mountinfo.Mount {
	return mountinfo.Mount{
		Point:  point,
		FSType: "fuse.fuse-overlayfs",
		Source: "/images/alice/app/image.sqsh",
		Upper:  filepath.Join(runtimeDir, "upper"),
	}
}

// withStatusSeeds installs the fake table and health seams and drops
// one active and one closed record into the session store.
func withStatusSeeds(t *testing.T, home string, healthy map[string]bool) (lib []string, sessions string) {
	t.Helper()
	_, lib = libFlags(home)
	sessions = sessionsDir(home)
	devRT := filepath.Join(sessions, "dev", "runtime", "m1")
	orphanRT := filepath.Join(sessions, "orphan", "runtime", "m1")
	table := []mountinfo.Mount{
		statusView("/ws/dev", devRT),
		statusView("/ws/orphan", orphanRT),
	}
	withSessionSeams(t, func(engine *session.Engine) {
		engine.LoadTable = func() ([]mountinfo.Mount, error) { return table, nil }
		engine.Health = func(point string) bool { return healthy[point] }
	})
	writeSession(t, sessions, "dev", "alice/app", "0123456789abcdef", "running")
	writeSession(t, sessions, "old", "alice/app", "0123456789abcdef", "closed")
	return lib, sessions
}

func TestStatusTreatsUnavailableMountTableAsEmpty(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withSessionSeams(t, func(engine *session.Engine) {
		engine.LoadTable = func() ([]mountinfo.Mount, error) {
			return nil, mountinfo.ErrUnavailable
		}
	})
	stdout, stderr, _, err := runCmd(t, append(lib, "status")...)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, stderr)
	}
	if stdout != "no sessions recorded\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	plain := filepath.Join(work, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	_, stderr, _, err = runCmd(t, append(lib, "status", plain)...)
	if err != nil {
		t.Fatalf("uncovered: ExitCode(%v) = %d\n%s", err, ExitCode(err), stderr)
	}
}

func TestStatusOverviewRendering(t *testing.T) {
	home, _ := withWorkspace(t)
	lib, _ := withStatusSeeds(t, home, map[string]bool{"/ws/dev": true})
	t.Setenv("TILER_SESSION_ID", "dev")

	stdout, stderr, _, err := runCmd(t, append(lib, "status")...)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, stderr)
	}
	for _, want := range []string{"dev · active · current", "[rw · mounted · healthy]", "old · closed", "orphan /ws/orphan"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("missing %q in %s", want, stdout)
		}
	}
}

func TestStatusOverviewMarksStaleMounts(t *testing.T) {
	home, _ := withWorkspace(t)
	lib, _ := withStatusSeeds(t, home, map[string]bool{})

	stdout, stderr, _, err := runCmd(t, append(lib, "status")...)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "mounted · stale") {
		t.Fatalf("stdout = %q, want the stale annotation", stdout)
	}
	// The dead view of a live record stays claimed: only the
	// unclaimed combination lists as an orphan.
	if strings.Contains(stdout, "orphan /ws/dev") {
		t.Fatalf("stdout = %q, want no orphan for the recorded view", stdout)
	}
}

func TestStatusOverviewWithoutRecords(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withSessionSeams(t, func(engine *session.Engine) {
		engine.LoadTable = func() ([]mountinfo.Mount, error) { return nil, nil }
		engine.Health = func(string) bool { return false }
	})

	stdout, stderr, _, err := runCmd(t, append(lib, "status")...)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, stderr)
	}
	if stdout != "no sessions recorded\n" {
		t.Fatalf("stdout = %q, want the empty overview line", stdout)
	}
}

func TestStatusDirectoryAndEntrypoints(t *testing.T) {
	home, work := withWorkspace(t)
	lib, sessions := withStatusSeeds(t, home, map[string]bool{"/ws/dev": true})
	for _, format := range [][]string{nil, {"--json"}} {
		var expected string
		for _, entry := range [][]string{{"status"}, {"st"}, {"session", "status"}} {
			args := append(append(entry, "/ws/dev/src", "-s", "dev", "-i", "alice/app"), format...)
			out, stderr, _, err := runCmd(t, append(lib, args...)...)
			if err != nil || !strings.Contains(out, "dev") {
				t.Fatalf("%v: %s %v %s", entry, out, err, stderr)
			}
			if expected != "" && expected != out {
				t.Fatalf("entrypoint differs: %s", out)
			}
			expected = out
		}
	}
	for _, path := range []string{filepath.Join(work, "elsewhere"), filepath.Join(sessions, "dev", "runtime", "m1", "upper")} {
		out, stderr, _, err := runCmd(t, append(lib, "status", path)...)
		if err != nil || out != "no sessions recorded\n" {
			t.Fatalf("unrelated directory: %q %v %s", out, err, stderr)
		}
	}
	file := filepath.Join(work, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, _, err := runCmd(t, append(lib, "status", file)...)
	if err == nil || !strings.Contains(stderr, "directory required") {
		t.Fatalf("file query: %v %s", err, stderr)
	}
}

func TestStatusPathStaleMark(t *testing.T) {
	home, _ := withWorkspace(t)
	lib, _ := withStatusSeeds(t, home, map[string]bool{})

	stdout, stderr, _, err := runCmd(t, append(lib, "status", "/ws/dev")...)
	if err != nil {
		t.Fatalf("status path: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "mounted · stale") {
		t.Fatalf("stdout = %q, want the stale mark", stdout)
	}
}

func TestStatusExitClassification(t *testing.T) {
	home, _ := withWorkspace(t)
	lib, _ := withStatusSeeds(t, home, map[string]bool{"/ws/dev": true})

	_, _, _, err := runCmd(t, append(lib, "status", "a", "b")...)
	if ExitCode(err) != 2 {
		t.Fatalf("two arguments: ExitCode(%v) = %d, want 2", err, ExitCode(err))
	}
	_, _, _, err = runCmd(t, append(lib, "status", "/nowhere")...)
	if err != nil {
		t.Fatalf("uncovered directory: %v", err)
	}
}
