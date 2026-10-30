package activity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveLogDeletesTheFileAndItsLock(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "dev.log")
	if err := os.WriteFile(path, []byte("log"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".lock", []byte("lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveLog(root, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("log remains: %v", err)
	}
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock remains: %v", err)
	}
}

func TestRemoveLogMissingFileIsSuccess(t *testing.T) {
	root := t.TempDir()
	if err := RemoveLog(root, filepath.Join(root, "sessions", "absent.log")); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveLogRejectsASymlinkWithoutFollowingIt(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(external, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "dev.log")
	if err := os.Symlink(external, link); err != nil {
		t.Skip(err)
	}
	if err := os.WriteFile(link+".lock", []byte("lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := RemoveLog(root, link)
	if !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("RemoveLog = %v", err)
	}
	if data, readErr := os.ReadFile(external); readErr != nil || string(data) != "keep" {
		t.Fatalf("symlink was followed: %q, %v", data, readErr)
	}
	info, statErr := os.Lstat(link)
	if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was removed: %v", statErr)
	}
	if _, statErr := os.Stat(link + ".lock"); statErr != nil {
		t.Fatalf("lock was removed before the unsafe log: %v", statErr)
	}
}

func TestRemoveLogRejectsANonRegularFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sessions", "dev.log")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	err := RemoveLog(root, path)
	if !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("RemoveLog = %v", err)
	}
	info, statErr := os.Lstat(path)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("non-regular log was removed: %v", statErr)
	}
}

func TestRemoveLogRejectsAPathOutsideTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "dev.log")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveLog(root, outside); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("RemoveLog = %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file was removed: %v", err)
	}
}

func TestPruneEmptyDirsStopsAtTheBoundary(t *testing.T) {
	root := t.TempDir()
	images := filepath.Join(root, "images")
	leaf := filepath.Join(images, "alice", "app")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := PruneEmptyDirs(leaf, images); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(images, "alice")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty parent remains: %v", err)
	}
	if _, err := os.Stat(images); err != nil {
		t.Fatalf("boundary was removed: %v", err)
	}
}

func TestPruneEmptyDirsKeepsANonEmptyParent(t *testing.T) {
	root := t.TempDir()
	images := filepath.Join(root, "images")
	leaf := filepath.Join(images, "alice", "app")
	keep := filepath.Join(images, "alice", "neighbor", "keep.log")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(keep), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PruneEmptyDirs(leaf, images); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(leaf); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty leaf remains: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("neighbor was removed: %v", err)
	}
}
