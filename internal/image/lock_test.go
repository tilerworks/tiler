package image

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/filelock"
)

func mustControl(t *testing.T, imagesDir, name, suffix string) string {
	t.Helper()
	path, err := controlFile(imagesDir, name, suffix)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImageLockIsExclusivePerName(t *testing.T) {
	dir := t.TempDir()
	hold, err := LockImage(dir, "alice/app", 0)
	if err != nil {
		t.Fatal(err)
	}
	path, err := controlFile(dir, "alice/app", ".lock")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode = %v, %v", info.Mode().Perm(), err)
	}
	for _, timeout := range []time.Duration{0, 20 * time.Millisecond} {
		_, err := LockImage(dir, "alice/app", timeout)
		if !errors.Is(err, filelock.ErrHeld) && !errors.Is(err, filelock.ErrTimeout) {
			t.Fatalf("timeout %s err = %v", timeout, err)
		}
	}
	other, err := LockImage(dir, "bob/docs", 0)
	if err != nil {
		t.Fatal(err)
	}
	other.Release()
	hold.Release()
	again, err := LockImage(dir, "alice/app", 0)
	if err != nil {
		t.Fatal(err)
	}
	again.Release()
	if _, err := LockImage(" ", "alice/app", 0); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("blank root err = %v", err)
	}
}

func TestImageLockRemovalKeepsAReplacementInode(t *testing.T) {
	dir := t.TempDir()
	hold, err := LockImage(dir, "alice/app", 0)
	if err != nil {
		t.Fatal(err)
	}
	path, err := controlFile(dir, "alice/app", ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_ = replacement.Close()
	if err := hold.Discard("alice/app"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("replacement lock removed: %v", err)
	}
	hold.Release()
}

func TestImageLockRemovalDropsTheLockAfterTheRepositoryIsGone(t *testing.T) {
	dir := t.TempDir()
	repo, err := OpenRepo(dir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	hold, err := LockImage(dir, "alice/app", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(repo.Dir); err != nil {
		t.Fatal(err)
	}
	if err := hold.DiscardIfRepositoryGone(dir, "alice/app"); err != nil {
		t.Fatal(err)
	}
	path, err := controlFile(dir, "alice/app", ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file after repository removal: %v", err)
	}
	hold.Release()
	if _, err := os.Stat(filepath.Join(dir, ".library.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("image locks created a library lock")
	}
}
