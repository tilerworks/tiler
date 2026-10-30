package activity

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrateRenamesLegacyLogAndKeepsLastActivity(t *testing.T) {
	root := t.TempDir()
	legacyDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "logs")
	for _, dir := range []string{legacyDir, newDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	legacy := filepath.Join(legacyDir, LogName)
	w, err := OpenLog(root, legacyDir, LogName, "", fixedClock(testTime))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(Event{Event: EventMount, Phase: PhaseSucceeded}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := OpenLog(root, newDir, "dev.log", legacy, fixedClock(testTime.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	last, err := migrated.LastActivity()
	if err != nil || !last.Equal(testTime) {
		t.Fatalf("last activity = %v, %v, want %v", last, err, testTime)
	}
	got, err := os.ReadFile(filepath.Join(newDir, "dev.log"))
	if err != nil || !bytes.Equal(got, before) {
		t.Fatalf("migrated log = %q, %v", got, err)
	}
	for _, name := range []string{LogName, LockName} {
		if _, err := os.Stat(filepath.Join(legacyDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy %s remains: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(newDir, "dev.log.lock")); err != nil {
		t.Fatalf("renamed lock: %v", err)
	}
}

func TestMigrateCopiesUnwritableLegacyAndLeavesIt(t *testing.T) {
	root := t.TempDir()
	legacyDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "logs")
	for _, dir := range []string{legacyDir, newDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	legacy := filepath.Join(legacyDir, LogName)
	w, err := OpenLog(root, legacyDir, LogName, "", fixedClock(testTime))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(Event{Event: EventMount, Phase: PhaseSucceeded}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(legacyDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(legacyDir, 0o755) })
	migrated, err := OpenLog(root, newDir, "dev.log", legacy, fixedClock(testTime))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := os.Stat(filepath.Join(newDir, "dev.log.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unrenamed lock was copied: %v", err)
	}
	last, err := migrated.LastActivity()
	if err != nil || !last.Equal(testTime) {
		t.Fatalf("last activity = %v, %v", last, err)
	}
	got, err := os.ReadFile(filepath.Join(newDir, "dev.log"))
	if err != nil || !bytes.Equal(got, before) {
		t.Fatalf("copied log = %q, %v", got, err)
	}
	left, err := os.ReadFile(legacy)
	if err != nil || !bytes.Equal(left, before) {
		t.Fatalf("original log = %q, %v", left, err)
	}
	if _, err := os.Stat(filepath.Join(legacyDir, LockName)); err != nil {
		t.Fatalf("original lock: %v", err)
	}
}

func TestMigrateLeavesAnExistingLogAlone(t *testing.T) {
	root := t.TempDir()
	legacyDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "logs")
	for _, dir := range []string{legacyDir, newDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	legacy := filepath.Join(legacyDir, LogName)
	if err := os.WriteFile(legacy, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(newDir, "dev.log")
	if err := os.WriteFile(dst, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLog(root, newDir, "dev.log", legacy, fixedClock(testTime)); err != nil {
		t.Fatal(err)
	}
	left, err := os.ReadFile(legacy)
	if err != nil || string(left) != "old\n" {
		t.Fatalf("legacy = %q, %v", left, err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "new\n" {
		t.Fatalf("destination = %q, %v", got, err)
	}
}
