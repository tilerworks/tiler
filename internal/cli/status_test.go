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
	if ExitCode(err) != 1 || !strings.Contains(stderr, "no session covers") {
		t.Fatalf("uncovered: ExitCode(%v) = %d\n%s", err, ExitCode(err), stderr)
	}
}

func TestStatusOverviewRendering(t *testing.T) {
	home, _ := withWorkspace(t)
	lib, sessions := withStatusSeeds(t, home, map[string]bool{"/ws/dev": true})
	t.Setenv("TILER_SESSION_ID", "dev")

	stdout, stderr, _, err := runCmd(t, append(lib, "status")...)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("stdout = %q, want dev block, old header, and the orphan line", stdout)
	}
	if !strings.HasPrefix(lines[0], "dev active created=") ||
		!strings.HasSuffix(lines[0], " current") {
		t.Fatalf("dev header = %q, want the current marker", lines[0])
	}
	want := "  1 mount alice/app /ws/dev mode=rw status=mounted health=healthy"
	if lines[1] != want {
		t.Fatalf("action line = %q, want %q", lines[1], want)
	}
	if !strings.HasPrefix(lines[2], "old closed created=") {
		t.Fatalf("old header = %q, want the closed summary", lines[2])
	}
	want = "orphan /ws/orphan fuse.fuse-overlayfs " +
		filepath.Join(sessions, "orphan", "runtime", "m1")
	if lines[3] != want {
		t.Fatalf("orphan line = %q, want %q", lines[3], want)
	}
}

func TestStatusOverviewMarksStaleMounts(t *testing.T) {
	home, _ := withWorkspace(t)
	lib, _ := withStatusSeeds(t, home, map[string]bool{})

	stdout, stderr, _, err := runCmd(t, append(lib, "status")...)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "status=mounted health=stale") {
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

func TestStatusPathRendering(t *testing.T) {
	home, work := withWorkspace(t)
	lib, sessions := withStatusSeeds(t, home, map[string]bool{"/ws/dev": true})

	_, stderr, _, err := runCmd(t, append(lib, "status", filepath.Join(work, "elsewhere"))...)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1\n%s", err, ExitCode(err), stderr)
	}
	if !strings.Contains(stderr, "no session covers") {
		t.Fatalf("stderr = %q, want the covering-session error", stderr)
	}

	stdout, stderr, _, err := runCmd(t, append(lib, "status", "/ws/dev/src/main.go")...)
	if err != nil {
		t.Fatalf("status path: %v\n%s", err, stderr)
	}
	want := "dev active mountpoint=/ws/dev\n" +
		"  1 mount alice/app /ws/dev mode=rw status=mounted\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}

	// A runtime directory path resolves back to the same session.
	stdout, stderr, _, err = runCmd(t, append(lib, "status",
		filepath.Join(sessions, "dev", "runtime", "m1", "upper"))...)
	if err != nil {
		t.Fatalf("status runtime path: %v\n%s", err, stderr)
	}
	if !strings.HasPrefix(stdout, "dev active mountpoint=/ws/dev") {
		t.Fatalf("stdout = %q, want the covering session", stdout)
	}
}

func TestStatusPathStaleMark(t *testing.T) {
	home, _ := withWorkspace(t)
	lib, _ := withStatusSeeds(t, home, map[string]bool{})

	stdout, stderr, _, err := runCmd(t, append(lib, "status", "/ws/dev")...)
	if err != nil {
		t.Fatalf("status path: %v\n%s", err, stderr)
	}
	if !strings.HasPrefix(stdout, "dev active mountpoint=/ws/dev stale\n") {
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
	if ExitCode(err) != 1 {
		t.Fatalf("uncovered path: ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
}
