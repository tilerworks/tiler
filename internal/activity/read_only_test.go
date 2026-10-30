package activity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jiangxin/tiler/internal/filelock"
)

func TestReadLastActivityPreservesFilesAndReportsUnsafeTails(t *testing.T) {
	w, dir := openTestWriter(t, fixedClock(testTime))
	for _, phase := range []string{PhaseStarted, PhaseFailed} {
		if _, err := w.Append(Event{Event: EventMount, Phase: phase}); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(w.logPath, 0o644); err != nil {
			t.Fatal(err)
		}
		before, _ := os.Stat(w.logPath)
		at, err := ReadLastActivity(w.root, dir, testLogName)
		after, _ := os.Stat(w.logPath)
		if err != nil || !at.Equal(testTime) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || after.Mode().Perm() != 0o644 {
			t.Fatalf("read = %v, %v; file changed: %v -> %v", at, err, before, after)
		}
	}
	lock, err := os.Open(w.lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := filelock.Acquire(lock, 0); err != nil {
		t.Skip(err)
	}
	if _, err := ReadLastActivity(w.root, dir, testLogName); !errors.Is(err, filelock.ErrHeld) {
		t.Fatalf("held lock: %v", err)
	}
	_ = filelock.Release(lock)
	if err := os.WriteFile(w.logPath, []byte("broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLastActivity(w.root, dir, testLogName); !errors.Is(err, ErrDamagedLog) {
		t.Fatalf("damaged: %v", err)
	}
	if err := os.Remove(w.logPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(w.lockPath, w.logPath); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLastActivity(w.root, dir, testLogName); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("symlink: %v", err)
	}
}
func TestReadLastActivityNeverCreatesMissingPathsOrLocks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions")
	if _, err := ReadLastActivity(root, dir, "dev.log"); !errors.Is(err, ErrNoActivity) {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created directory: %v", err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"", mustLine(t, testTime, 1)} {
		if err := os.WriteFile(filepath.Join(dir, "dev.log"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ReadLastActivity(root, dir, "dev.log")
		if data == "" && !errors.Is(err, ErrNoActivity) || data != "" && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing lock: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "dev.log.lock")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("created lock: %v", err)
		}
	}
}
