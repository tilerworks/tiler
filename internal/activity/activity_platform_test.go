//go:build !linux && !darwin

package activity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jiangxin/tiler/internal/filelock"
)

// TestAppendRefusesUnlockedPlatform proves a platform without
// advisory locking fails the append instead of writing unlocked.
func TestAppendRefusesUnlockedPlatform(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "dev")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := OpenLog(root, dir, testLogName, fixedClock(testTime))
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	if _, err := w.Append(Event{Event: EventMount, Phase: PhaseStarted}); !errors.Is(err, filelock.ErrUnavailable) {
		t.Fatalf("err = %v, want filelock.ErrUnavailable", err)
	}
	if _, err := os.Stat(filepath.Join(dir, testLogName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("log written without a lock: %v", err)
	}
}
