//go:build linux

package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// lockHelperEnv marks a test binary re-executed as a lock holder.
// Its value carries the store directory, the session name, the
// shared log path, and the hold duration in milliseconds, joined
// with "|".
const lockHelperEnv = "TILER_SESSION_LOCK_HELPER"

// TestHelperLockProcess is not a test of its own: a re-executed
// child process takes one session's real advisory lock, marks its
// critical section in the shared log, holds the lock, and exits.
// The parent process drives it through lockHelperEnv.
func TestHelperLockProcess(t *testing.T) {
	spec := os.Getenv(lockHelperEnv)
	if spec == "" {
		t.Skip("not the lock helper process")
	}
	parts := strings.Split(spec, "|")
	if len(parts) != 4 {
		t.Fatalf("helper spec = %q", spec)
	}
	dir, name, logPath := parts[0], parts[1], parts[2]
	hold, err := strconv.Atoi(parts[3])
	if err != nil {
		t.Fatalf("helper spec = %q: %v", spec, err)
	}
	release, err := (Store{Dir: dir}).lockSession(name, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	appendLogLine(t, logPath, "enter "+name)
	time.Sleep(time.Duration(hold) * time.Millisecond)
	appendLogLine(t, logPath, "exit "+name)
}

// spawnLockHelper starts one child test process that takes the
// session's lock, logs its critical section into the shared log,
// and holds the lock for the given duration.
func spawnLockHelper(t *testing.T, dir, name, logPath string, hold time.Duration) *exec.Cmd {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("cannot locate the test binary: %v", err)
	}
	spec := strings.Join([]string{
		dir, name, logPath, strconv.Itoa(int(hold.Milliseconds())),
	}, "|")
	cmd := exec.Command(self, "-test.run", "^TestHelperLockProcess$")
	cmd.Env = append(os.Environ(), lockHelperEnv+"="+spec)
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn lock helper: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	return cmd
}

// waitProc waits for one helper process and fails on its exit
// status. The helper bounds itself: the lock wait is one minute and
// the hold is milliseconds.
func waitProc(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("lock helper failed: %v", err)
	}
}

// appendLogLine adds one line to the shared log through a single
// appending write, so the line order reflects the kernel's order.
func appendLogLine(t *testing.T, path, line string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

// readLog returns the shared log's lines, tolerating its absence
// before the first holder wrote.
func readLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	trimmed := strings.TrimSuffix(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// waitLog polls until the shared log carries at least want lines.
func waitLog(t *testing.T, path string, want int) []string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if lines := readLog(t, path); len(lines) >= want {
			return lines
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("log %s never reached %d lines: %v", path, want, readLog(t, path))
	return nil
}

// TestLockSerializesAcrossProcesses proves the advisory lock is a
// cross-process mutex: two holders of the same session never overlap
// inside their critical sections, whichever order they started in.
func TestLockSerializesAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log")
	first := spawnLockHelper(t, dir, "dev", logPath, 400*time.Millisecond)
	// The first holder is inside its critical section before the
	// second one starts.
	waitLog(t, logPath, 1)
	time.Sleep(150 * time.Millisecond)
	second := spawnLockHelper(t, dir, "dev", logPath, 200*time.Millisecond)
	waitProc(t, first)
	waitProc(t, second)
	lines := waitLog(t, logPath, 4)
	want := []string{"enter dev", "exit dev", "enter dev", "exit dev"}
	if !slices.Equal(lines, want) {
		t.Fatalf("log = %v, want %v: the lock did not serialize", lines, want)
	}
}

// TestLockRunsDifferentSessionsInParallel proves the lock scope is
// one session: a second session's holder enters its critical section
// while the first is still holding, so unrelated work never queues.
func TestLockRunsDifferentSessionsInParallel(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log")
	holder := spawnLockHelper(t, dir, "aaa", logPath, 600*time.Millisecond)
	waitLog(t, logPath, 1)
	time.Sleep(200 * time.Millisecond)
	other := spawnLockHelper(t, dir, "bbb", logPath, 200*time.Millisecond)
	waitProc(t, holder)
	waitProc(t, other)
	lines := waitLog(t, logPath, 4)
	enterBBB, exitAAA := -1, -1
	for i, line := range lines {
		if line == "enter bbb" && enterBBB < 0 {
			enterBBB = i
		}
		if line == "exit aaa" && exitAAA < 0 {
			exitAAA = i
		}
	}
	if enterBBB < 0 || exitAAA < 0 {
		t.Fatalf("log = %v, want both critical sections", lines)
	}
	if enterBBB > exitAAA {
		t.Fatalf("the second session waited behind the first: %v", lines)
	}
}
