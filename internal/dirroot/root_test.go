//go:build linux || darwin

package dirroot

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jiangxin/tiler/internal/testutil"
)

func TestStatRejectsSymlinkThatLeavesTheRoot(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "src")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "file")); err != nil {
		t.Fatal(err)
	}
	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	_, err = root.Stat("src/lib")
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("directory symlink stat = %v", err)
	}
	_, err = root.Stat("file")
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("file symlink stat = %v", err)
	}
}

func TestStatFollowsSymlinkInsideTheRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "a"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("lib", filepath.Join(dir, "src")); err != nil {
		t.Fatal(err)
	}
	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	info, err := root.Stat("src")
	if err != nil || !info.IsDir() {
		t.Fatalf("src = %v, %v", info, err)
	}
	info, err = root.Lstat("src")
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("lstat src = %v, %v", info, err)
	}
	file, err := root.Open("src/a")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
}

func TestRemoveAllDropsSymlinkAndKeepsTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := root.RemoveAll("link"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("symlink target was removed: %v", err)
	}
	if err := root.RemoveAll("missing"); err != nil {
		t.Fatal(err)
	}
}

func TestOpenResolvesParentComponentsAfterSymlinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "b", "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b", "victim"), []byte("correct"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "victim"), []byte("wrong"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "victim"), []byte("also-wrong"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../b/c", filepath.Join(dir, "a", "hop")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a/hop/../victim", filepath.Join(dir, "entry")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing/../victim", filepath.Join(dir, "bad")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("f"), 0o644); err != nil {
		t.Fatal(err)
	}

	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	got := readRoot(t, root, "entry")
	native, err := os.ReadFile(dir + "/entry")
	if err != nil || string(native) != "correct" || got != "correct" {
		t.Fatalf("entry = %q, native %q, %v", got, native, err)
	}
	_, err = root.Open("bad")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing parent = %v", err)
	}
	_, err = root.Open("file/../victim")
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("file parent = %v", err)
	}
}

func TestLstatAllowsDeepTrees(t *testing.T) {
	dir := t.TempDir()
	rel := strings.Repeat("d/", 300) + "leaf"
	if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(rel)
	if err != nil || !info.IsDir() {
		t.Fatalf("lstat = %v, %v", info, err)
	}
	entries, err := fs.ReadDir(root.FS(), strings.Repeat("d/", 299)+"d")
	if err != nil || len(entries) != 1 || entries[0].Name() != "leaf" {
		t.Fatalf("deep readdir = %v, %v", entries, err)
	}
}

func TestDuplicatedDescriptorsKeepCloseOnExec(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	located, err := root.dup()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(located) }()
	assertCloseOnExec(t, located)

	// readDir duplicates the directory it lists. The duplicate is closed
	// before return, so check the same helper on that descriptor first.
	listed, err := dupCloexec(located)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(listed) }()
	assertCloseOnExec(t, listed)
	if _, err := namesFromFD(located); err != nil {
		t.Fatal(err)
	}
}

func assertCloseOnExec(t *testing.T, fd int) {
	t.Helper()
	flags, err := fdFlags(fd)
	if err != nil {
		t.Fatal(err)
	}
	if flags&syscall.FD_CLOEXEC == 0 {
		t.Fatalf("fd %d flags = %#x, want FD_CLOEXEC", fd, flags)
	}
}

func readRoot(t *testing.T, root *Root, name string) string {
	t.Helper()
	file, err := root.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	body, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestWriteFileStaysInTheDirectory(t *testing.T) {
	dir := t.TempDir()
	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := root.WriteFile("note", []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := root.Rename("note", "kept"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "kept"))
	if err != nil || string(got) != "hello\n" {
		t.Fatalf("kept = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "note")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old name = %v", err)
	}
}

func TestFinalDotAndTrailingSlashRequireADirectory(t *testing.T) {
	dir := dotTree(t)
	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	for _, name := range []string{
		"file/.", "file/", "file/./.",
		"linkslash", "linkdot", "linkfile/",
		"file/./child", "linkdot/child", "linkslash/../victim",
	} {
		_, err := root.Open(name)
		if !errors.Is(err, syscall.ENOTDIR) {
			t.Fatalf("open %s = %v, want ENOTDIR", name, err)
		}
		_, err = root.Stat(name)
		if !errors.Is(err, syscall.ENOTDIR) {
			t.Fatalf("stat %s = %v, want ENOTDIR", name, err)
		}
	}
	for _, name := range []string{
		"file/.", "file/", "file/./.", "linkfile/",
		"file/./child", "linkdot/child", "linkslash/../victim",
	} {
		_, err = root.Lstat(name)
		if !errors.Is(err, syscall.ENOTDIR) {
			t.Fatalf("lstat %s = %v, want ENOTDIR", name, err)
		}
	}
	opened, err := root.Open("dir/.")
	if err != nil {
		t.Fatalf("open dir/. = %v", err)
	}
	_ = opened.Close()
	opened, err = root.Open("dir/./")
	if err != nil {
		t.Fatalf("open dir/./ = %v", err)
	}
	_ = opened.Close()
	info, err := root.Stat("dir/.")
	if err != nil || !info.IsDir() {
		t.Fatalf("stat dir/. = %v, %v", info, err)
	}
	info, err = root.Stat("dir/./")
	if err != nil || !info.IsDir() {
		t.Fatalf("stat dir/./ = %v, %v", info, err)
	}
	if got := readRoot(t, root, "dir/./child"); got != "child" {
		t.Fatalf("dir/./child = %q", got)
	}
	if got := readRoot(t, root, "linknest/child"); got != "child" {
		t.Fatalf("linknest/child = %q", got)
	}
	info, err = root.Lstat("linkslash")
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("lstat linkslash = %v, %v", info, err)
	}
	if _, err := os.ReadFile(filepath.Join(dir, "file")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(filepath.Join(dir, "victim")); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveHonorsAFinalDot(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		dir := dotTree(t)
		root, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		if err := root.Remove("file/."); !errors.Is(err, syscall.ENOTDIR) {
			t.Fatalf("remove = %v", err)
		}
		if _, err := os.ReadFile(filepath.Join(dir, "file")); err != nil {
			t.Fatalf("file was removed: %v", err)
		}
	})
	t.Run("directory", func(t *testing.T) {
		dir := dotTree(t)
		root, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		if err := root.Remove("dir/."); !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("remove = %v", err)
		}
		if _, err := os.ReadFile(filepath.Join(dir, "dir", "child")); err != nil {
			t.Fatalf("child = %v", err)
		}
	})
}

func TestRemoveAllHonorsAFinalDot(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		dir := dotTree(t)
		root, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		if err := root.RemoveAll("file/."); !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("removeall = %v", err)
		}
		if _, err := os.ReadFile(filepath.Join(dir, "file")); err != nil {
			t.Fatalf("file was removed: %v", err)
		}
	})
	t.Run("directory", func(t *testing.T) {
		dir := dotTree(t)
		root, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		if err := root.RemoveAll("dir/."); !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("removeall = %v", err)
		}
		if _, err := os.ReadFile(filepath.Join(dir, "dir", "child")); err != nil {
			t.Fatalf("child = %v", err)
		}
	})
	t.Run("trailing slash after dot", func(t *testing.T) {
		dir := dotTree(t)
		root, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		if err := root.RemoveAll("dir/./"); !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("removeall = %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "dir")); err != nil {
			t.Fatalf("dir = %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "dir", "child")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("child = %v, want removed", err)
		}
	})
}

func TestDirectoryNamesAndDeletionIgnoreTheWorkingDirectory(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootDir, "widget"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "widget", "payload"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, "widget"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "widget", "other"), []byte("decoy"), 0o644); err != nil {
		t.Fatal(err)
	}
	testutil.Chdir(t, cwd)

	root, err := Open(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	entries, err := fs.ReadDir(root.FS(), "widget")
	if err != nil || len(entries) != 1 || entries[0].Name() != "payload" {
		t.Fatalf("readdir = %v, %v", namesOf(entries), err)
	}
	if err := root.RemoveAll("widget"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, "widget")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("widget = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(cwd, "widget", "other"))
	if err != nil || string(got) != "decoy" {
		t.Fatalf("decoy = %q, %v", got, err)
	}
}

func TestCreateRejectsATrailingDirectoryMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	_, err = root.OpenFile("fresh/", os.O_CREATE|os.O_WRONLY, 0o644)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("create fresh/ = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "fresh")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("fresh = %v", statErr)
	}
	_, err = root.OpenFile("file/.", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("truncate file/. = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "file"))
	if err != nil || string(got) != "keep" {
		t.Fatalf("file = %q, %v", got, err)
	}
}

func dotTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "victim"), []byte("wrong"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dir", "child"), []byte("child"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file/", filepath.Join(dir, "linkslash")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file/.", filepath.Join(dir, "linkdot")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(dir, "linkfile")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("dir/.", filepath.Join(dir, "linknest")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func namesOf(entries []fs.DirEntry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}

func TestRootListingsHaveIndependentCursors(t *testing.T) {
	dir := t.TempDir()
	want := make([]string, 512)
	for i := range want {
		want[i] = fmt.Sprintf("entry-%04d", i)
		if err := os.WriteFile(filepath.Join(dir, want[i]), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	check := func(fsys fs.FS) {
		entries, err := fs.ReadDir(fsys, ".")
		if err != nil {
			t.Error(err)
			return
		}
		if got := namesOf(entries); !reflect.DeepEqual(got, want) {
			t.Errorf("listing returned %d entries, want all %d in order", len(got), len(want))
		}
	}
	adapter := root.FS()
	check(adapter)
	check(adapter)
	check(root.FS())
	var workers sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		workers.Add(1)
		go func() { defer workers.Done(); <-start; check(root.FS()) }()
	}
	close(start)
	workers.Wait()
}

func TestStatPreservesNativeMetadata(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "note")
	if err := os.WriteFile(name, []byte("metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0o750) | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if err := os.Chmod(name, mode); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1700000000, 123456000)
	if err := os.Chtimes(name, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	native, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	info, err := root.Stat("note")
	if err != nil {
		t.Fatal(err)
	}
	if info.Name() != native.Name() || info.Size() != native.Size() || info.Mode() != native.Mode() || !info.ModTime().Equal(native.ModTime()) {
		t.Fatalf("metadata = %v, want %v", info, native)
	}
	got, ok := info.Sys().(*syscall.Stat_t)
	want := native.Sys().(*syscall.Stat_t)
	if !ok || got.Dev != want.Dev || got.Ino != want.Ino {
		t.Fatalf("stat identity = %v, want %v", got, want)
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %v, %v", entries, err)
	}
	entryInfo, err := entries[0].Info()
	if err != nil || entryInfo.Mode() != native.Mode() || !entryInfo.ModTime().Equal(native.ModTime()) {
		t.Fatalf("entry metadata = %v, %v", entryInfo, err)
	}
}

func TestClosedRootRejectsOperations(t *testing.T) {
	root, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".", "missing"} {
		if _, err := root.Open(name); !errors.Is(err, os.ErrClosed) {
			t.Errorf("Open(%q) = %v", name, err)
		}
		if _, err := fs.ReadDir(root.FS(), name); !errors.Is(err, os.ErrClosed) {
			t.Errorf("ReadDir(%q) = %v", name, err)
		}
	}
}

func TestCloseDoesNotInvalidateAcquiredDescriptor(t *testing.T) {
	root, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan int)
	closed := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		fd, err := root.dup()
		if err != nil {
			t.Error(err)
			close(acquired)
			return
		}
		defer func() { _ = syscall.Close(fd) }()
		acquired <- fd
		<-closed
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil {
			t.Errorf("acquired descriptor after Close: %v", err)
		}
	}()
	<-acquired
	if err := root.Close(); err != nil {
		t.Error(err)
	}
	close(closed)
	workers.Wait()
	if _, err := root.dup(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("dup after Close = %v", err)
	}
}

func TestRejectsInvalidPathsAndPreservesFailedRename(t *testing.T) {
	dir := t.TempDir()
	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for _, name := range []string{"", "/outside", "../outside", "bad\x00name"} {
		if file, err := root.Open(name); err == nil {
			_ = file.Close()
			t.Errorf("accepted %q", name)
		}
	}
	if err := root.WriteFile("kept", []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := root.Rename("kept", "missing/new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rename = %v", err)
	}
	if got := readRoot(t, root, "kept"); got != "keep" {
		t.Fatalf("source after failed rename = %q", got)
	}
}

func TestAcquiredParentIgnoresSymlinkReplacement(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	for path, data := range map[string]string{
		filepath.Join(dir, "parent", "note"): "inside",
		filepath.Join(outside, "note"):       "outside",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	parent, base, err := root.locate("parent/note", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(parent) }()
	// Replace the path after traversal has acquired its parent descriptor.
	if err := os.Rename(filepath.Join(dir, "parent"), filepath.Join(dir, "saved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "parent")); err != nil {
		t.Fatal(err)
	}
	fd, err := openat(parent, base, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), "note")
	got, err := io.ReadAll(file)
	cerr := file.Close()
	if err != nil || cerr != nil || string(got) != "inside" {
		t.Fatalf("acquired path = %q, %v, %v", got, err, cerr)
	}
	if _, err := root.Open("parent/note"); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("replacement path = %v", err)
	}
	// A final component replaced after locate must not follow its new target.
	if err := os.Remove(filepath.Join(dir, "saved", "note")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "note"), filepath.Join(dir, "saved", "note")); err != nil {
		t.Fatal(err)
	}
	fd, err = openat(parent, base, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err == nil {
		_ = syscall.Close(fd)
		t.Fatal("followed replaced final symlink")
	}
}
