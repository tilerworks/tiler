//go:build linux

package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRemoveArchiveRetriesAfterPartialDeletion(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "archive")
	if err := os.Mkdir(archive, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two", "three"} {
		if err := os.WriteFile(filepath.Join(archive, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var before syscall.Stat_t
	if err := syscall.Stat(archive, &before); err != nil {
		t.Fatal(err)
	}
	identity := restoreIdentity{Device: uint64(before.Dev), Inode: before.Ino}
	parent, err := OpenParent(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	failure := errors.New("injected unlink failure")
	removed := ""
	err = removeArchiveWith(parent, "archive", identity, func(dir *os.File, name string, flags uintptr) error {
		if removed != "" {
			return failure
		}
		if err := unlinkAt(dir, name, flags); err != nil {
			return err
		}
		removed = name
		return nil
	})
	if !errors.Is(err, failure) || removed == "" {
		t.Fatalf("remove error = %v, removed = %q", err, removed)
	}
	if _, err := os.Lstat(filepath.Join(archive, removed)); !os.IsNotExist(err) {
		t.Fatalf("removed entry still exists: %v", err)
	}
	entries, err := os.ReadDir(archive)
	if err != nil || len(entries) != 2 {
		t.Fatalf("remaining entries = %v, error = %v", entries, err)
	}
	var after syscall.Stat_t
	if err := syscall.Stat(archive, &after); err != nil || after.Dev != before.Dev || after.Ino != before.Ino {
		t.Fatalf("archive identity changed: %v", err)
	}
	if err := removeArchive(parent, "archive", identity); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, err := os.Lstat(archive); !os.IsNotExist(err) {
		t.Fatalf("archive remains after retry: %v", err)
	}
}

func TestRemoveArchiveKeepsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "archive")
	if err := os.Mkdir(archive, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(archive, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(archive, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(archive, &st); err != nil {
		t.Fatal(err)
	}
	p, err := OpenParent(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if err = removeArchive(p, "archive", restoreIdentity{Device: uint64(st.Dev), Inode: st.Ino}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(archive); !os.IsNotExist(err) {
		t.Fatalf("archive remains: %v", err)
	}
}
func TestRemoveArchiveRejectsChangedIdentity(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "archive")
	if err := os.Mkdir(archive, 0700); err != nil {
		t.Fatal(err)
	}
	p, err := OpenParent(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if err = removeArchive(p, "archive", restoreIdentity{Inode: 1}); err == nil {
		t.Fatal("accepted wrong identity")
	}
	if _, err = os.Stat(archive); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveArchiveMissingIsAlreadyRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive")
	p, err := OpenParent(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if err = removeArchive(p, "archive", restoreIdentity{Inode: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveArchiveRetainsUnremovableDirectory(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "archive")
	if err := os.Mkdir(archive, 0700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(archive, "locked")
	if err := os.Mkdir(child, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(child, 0700) })
	if os.Geteuid() == 0 {
		t.Skip("root can open the protected directory")
	}
	var st syscall.Stat_t
	if err := syscall.Stat(archive, &st); err != nil {
		t.Fatal(err)
	}
	p, err := OpenParent(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if err := removeArchive(p, "archive", restoreIdentity{Device: uint64(st.Dev), Inode: st.Ino}); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(child); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveArchiveRejectsMountedTree(t *testing.T) {
	const path = "/proc"
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		t.Skipf("proc unavailable: %v", err)
	}
	p, err := OpenParent(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if err = removeArchive(p, "proc", restoreIdentity{Device: uint64(st.Dev), Inode: st.Ino}); err == nil {
		t.Fatal("accepted mounted tree")
	}
}
