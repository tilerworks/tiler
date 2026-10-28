package workspace

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// fixtureMtime is the whole-second mtime the fixtures share unless a
// test moves one side.
const fixtureMtime = int64(1600000000)

// fixtureRoots creates the empty upper and lower comparison roots.
func fixtureRoots(t *testing.T) (upper, lower string) {
	t.Helper()
	dir := t.TempDir()
	upper = filepath.Join(dir, "upper")
	lower = filepath.Join(dir, "lower")
	for _, root := range []string{upper, lower} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return upper, lower
}

// writeFixtureFile writes a file with an exact permission bit set,
// creating parent directories as needed. The explicit chmod keeps
// the umask out of the metadata comparison.
func writeFixtureFile(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatal(err)
	}
	setFixtureTime(t, path, fixtureMtime, 0)
}

// makeFixtureDir creates a directory and pins its mtime.
func makeFixtureDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	setFixtureTime(t, path, fixtureMtime, 0)
}

// setFixtureTime moves both timestamps of a path.
func setFixtureTime(t *testing.T, path string, sec, nsec int64) {
	t.Helper()
	when := time.Unix(sec, nsec)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// makeFixtureSymlink creates a symlink with parent directories.
func makeFixtureSymlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// fakePlatform fakes the platform probes so the rule tests run on
// any host: no overlay markers, single links, and zero device
// numbers unless a test asks for more.
type fakePlatform struct {
	markers  map[string][]string
	stats    map[string]platformStat
	stripped []string
}

func newFakePlatform() *fakePlatform {
	return &fakePlatform{
		markers: map[string][]string{},
		stats:   map[string]platformStat{},
	}
}

func (f *fakePlatform) mark(path, name string) {
	f.markers[path] = append(f.markers[path], name)
}

func (f *fakePlatform) has(path, name string) bool {
	for _, marker := range f.markers[path] {
		if marker == name {
			return true
		}
	}
	return false
}

func (f *fakePlatform) xattrs(path string) (map[string][]byte, error) {
	found := make(map[string][]byte, len(f.markers[path]))
	for _, name := range f.markers[path] {
		found[name] = []byte("y")
	}
	return found, nil
}

func (f *fakePlatform) strip(path, name string) error {
	for i, marker := range f.markers[path] {
		if marker == name {
			f.markers[path] = append(f.markers[path][:i], f.markers[path][i+1:]...)
			f.stripped = append(f.stripped, path)
			return nil
		}
	}
	return errors.New("no such xattr")
}

func (f *fakePlatform) stat(path string, _ os.FileInfo) (platformStat, error) {
	if st, ok := f.stats[path]; ok {
		return st, nil
	}
	return platformStat{nlink: 1}, nil
}

func (f *fakePlatform) hooks() pruneHooks {
	return pruneHooks{xattrs: f.xattrs, strip: f.strip, stat: f.stat}
}

// plainHooks fakes a Linux-like platform with no overlay markers.
func plainHooks() pruneHooks {
	return newFakePlatform().hooks()
}

func wantReport(t *testing.T, got, want PruneReport) {
	t.Helper()
	if got != want {
		t.Fatalf("report = %+v, want %+v", got, want)
	}
}

func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s still exists (err %v)", path, err)
	}
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("%s is gone: %v", path, err)
	}
}

func TestPruneUpperRejectsUnusableRoots(t *testing.T) {
	upper, lower := fixtureRoots(t)
	writeFixtureFile(t, filepath.Join(upper, "file"), "x", 0o644)
	writeFixtureFile(t, filepath.Join(lower, "file"), "x", 0o644)
	makeFixtureDir(t, filepath.Join(upper, "sub"))
	makeFixtureDir(t, filepath.Join(lower, "sub"))
	makeFixtureSymlink(t, lower, filepath.Join(upper, "dirlink"))
	cases := []struct {
		name      string
		upperDir  string
		lowerView string
	}{
		{"missing upper", filepath.Join(upper, "nope"), lower},
		{"upper is a file", filepath.Join(upper, "file"), lower},
		{"missing lower", upper, filepath.Join(lower, "nope")},
		{"lower is a file", upper, filepath.Join(lower, "file")},
		{"upper is a symlink", filepath.Join(upper, "dirlink"), lower},
		{"lower is a symlink", upper, filepath.Join(upper, "dirlink")},
		{"same directory twice", upper, upper},
		{"lower nested in upper", upper, filepath.Join(upper, "sub")},
		{"upper nested in lower", filepath.Join(lower, "sub"), lower},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := PruneUpper(tc.upperDir, tc.lowerView); err == nil {
				t.Fatal("PruneUpper accepted unusable roots")
			}
		})
	}
}

// TestPruneUpperRejectsAliasedRoots covers two spellings of the same
// directory that differ in their path elements: Lstat sees two real
// directories, so only the identity checks catch the alias.
func TestPruneUpperRejectsAliasedRoots(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	link := filepath.Join(dir, "link")
	makeFixtureDir(t, filepath.Join(plain, "x"))
	if err := os.Symlink(plain, link); err != nil {
		t.Fatal(err)
	}
	viaLink := filepath.Join(link, "x")
	viaPlain := filepath.Join(plain, "x")
	for _, tc := range []struct {
		name               string
		upperDir, lowerDir string
	}{
		{"same dir through symlinked parent", viaPlain, viaLink},
		{"nested through symlinked parent", plain, viaLink},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := PruneUpper(tc.upperDir, tc.lowerDir); err == nil {
				t.Fatal("PruneUpper accepted aliased roots")
			}
		})
	}
}

// TestPruneUpperRootMarkers pins the marker handling the upper root
// shares with subdirectories: a redirected root keeps the whole
// tree, an opaque root over lower content keeps the whole tree, and
// an opaque root the lower side has nothing to fill gets cleaned
// and stripped — but never removed.
func TestPruneUpperRootMarkers(t *testing.T) {
	t.Run("redirected root skips the tree", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		writeFixtureFile(t, filepath.Join(upper, "f.txt"), "hello", 0o644)
		writeFixtureFile(t, filepath.Join(lower, "f.txt"), "hello", 0o644)
		seam := newFakePlatform()
		seam.mark(upper, xattrRedirect)
		report, err := pruneUpper(upper, lower, seam.hooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{SkippedRedirect: 1})
		assertExists(t, filepath.Join(upper, "f.txt"))
	})
	t.Run("opaque root over lower content kept", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		writeFixtureFile(t, filepath.Join(upper, "f.txt"), "hello", 0o644)
		writeFixtureFile(t, filepath.Join(lower, "f.txt"), "hello", 0o644)
		seam := newFakePlatform()
		seam.mark(upper, xattrOpaque)
		report, err := pruneUpper(upper, lower, seam.hooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{Kept: 1})
		assertExists(t, filepath.Join(upper, "f.txt"))
		if !seam.has(upper, xattrOpaque) {
			t.Fatal("opaque marker was touched")
		}
	})
	t.Run("opaque root over empty lower cleaned and stripped", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		writeFixtureFile(t, filepath.Join(upper, "new.txt"), "new", 0o644)
		makeFixtureDir(t, filepath.Join(upper, "d"))
		seam := newFakePlatform()
		seam.mark(upper, xattrOpaque)
		report, err := pruneUpper(upper, lower, seam.hooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{RemovedDirs: 1, Kept: 1, StrippedOpaque: 1})
		assertExists(t, upper)
		assertExists(t, filepath.Join(upper, "new.txt"))
		assertGone(t, filepath.Join(upper, "d"))
		if seam.has(upper, xattrOpaque) {
			t.Fatal("opaque marker survived the strip")
		}
	})
}

func TestPruneUpperEmptyUpperIsNoop(t *testing.T) {
	upper, lower := fixtureRoots(t)
	report, err := PruneUpper(upper, lower)
	if err != nil {
		t.Fatal(err)
	}
	wantReport(t, report, PruneReport{})
	assertExists(t, upper)
}

func TestPruneUpperRemovesIdenticalFile(t *testing.T) {
	upper, lower := fixtureRoots(t)
	writeFixtureFile(t, filepath.Join(upper, "f.txt"), "hello", 0o644)
	writeFixtureFile(t, filepath.Join(lower, "f.txt"), "hello", 0o644)
	report, err := pruneUpper(upper, lower, plainHooks())
	if err != nil {
		t.Fatal(err)
	}
	wantReport(t, report, PruneReport{RemovedFiles: 1})
	assertGone(t, filepath.Join(upper, "f.txt"))
	assertExists(t, filepath.Join(lower, "f.txt"))
}

func TestPruneUpperKeepsDifferentFile(t *testing.T) {
	cases := []struct {
		name       string
		upperBody  string
		upperPerm  os.FileMode
		upperMtime int64
	}{
		{"size differs", "hello!", 0o644, fixtureMtime},
		{"permission differs", "hello", 0o600, fixtureMtime},
		{"setuid bit differs", "hello", 0o644 | os.ModeSetuid, fixtureMtime},
		{"mtime second differs", "hello", 0o644, fixtureMtime + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upper, lower := fixtureRoots(t)
			writeFixtureFile(t, filepath.Join(lower, "f.txt"), "hello", 0o644)
			writeFixtureFile(t, filepath.Join(upper, "f.txt"), tc.upperBody, tc.upperPerm)
			setFixtureTime(t, filepath.Join(upper, "f.txt"), tc.upperMtime, 0)
			report, err := pruneUpper(upper, lower, plainHooks())
			if err != nil {
				t.Fatal(err)
			}
			wantReport(t, report, PruneReport{Kept: 1})
			assertExists(t, filepath.Join(upper, "f.txt"))
		})
	}
}

// TestPruneUpperOwnerDifferenceKept pins the ownership half of the
// metadata quick check: a copy-up whose only divergence from the
// lower file is its owner must survive, or a chown would silently
// revert on the next mount. The seam feeds the divergent owner, so
// the test needs no root privileges to chown the fixture.
func TestPruneUpperOwnerDifferenceKept(t *testing.T) {
	cases := []struct {
		name string
		stat platformStat
	}{
		{"uid differs", platformStat{nlink: 1, uid: 65534}},
		{"gid differs", platformStat{nlink: 1, gid: 65534}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upper, lower := fixtureRoots(t)
			writeFixtureFile(t, filepath.Join(upper, "f.txt"), "hello", 0o644)
			writeFixtureFile(t, filepath.Join(lower, "f.txt"), "hello", 0o644)
			seam := newFakePlatform()
			seam.stats[filepath.Join(upper, "f.txt")] = tc.stat
			report, err := pruneUpper(upper, lower, seam.hooks())
			if err != nil {
				t.Fatal(err)
			}
			wantReport(t, report, PruneReport{Kept: 1})
			assertExists(t, filepath.Join(upper, "f.txt"))
		})
	}
}

// TestPruneUpperSameSecondRewriteIsRemoved pins the accepted risk of
// the metadata quick check: a file rewritten with different content
// but an unchanged size inside the same mtime second is considered
// identical and deleted. Content is deliberately never hashed.
func TestPruneUpperSameSecondRewriteIsRemoved(t *testing.T) {
	upper, lower := fixtureRoots(t)
	writeFixtureFile(t, filepath.Join(lower, "f.txt"), "hello", 0o644)
	writeFixtureFile(t, filepath.Join(upper, "f.txt"), "HELLO", 0o644)
	setFixtureTime(t, filepath.Join(upper, "f.txt"), fixtureMtime, 0)
	report, err := pruneUpper(upper, lower, plainHooks())
	if err != nil {
		t.Fatal(err)
	}
	wantReport(t, report, PruneReport{RemovedFiles: 1})
	assertGone(t, filepath.Join(upper, "f.txt"))
}

// TestPruneUpperMtimeTruncatesToSeconds covers the second-granularity
// comparison: sub-second differences match, whole-second differences
// do not.
func TestPruneUpperMtimeTruncatesToSeconds(t *testing.T) {
	upper, lower := fixtureRoots(t)
	writeFixtureFile(t, filepath.Join(lower, "f.txt"), "hello", 0o644)
	setFixtureTime(t, filepath.Join(lower, "f.txt"), fixtureMtime, 800_000_000)
	writeFixtureFile(t, filepath.Join(upper, "f.txt"), "hello", 0o644)
	setFixtureTime(t, filepath.Join(upper, "f.txt"), fixtureMtime, 200_000_000)
	report, err := pruneUpper(upper, lower, plainHooks())
	if err != nil {
		t.Fatal(err)
	}
	wantReport(t, report, PruneReport{RemovedFiles: 1})
}

func TestPruneUpperSymlinks(t *testing.T) {
	t.Run("matching target removed", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		makeFixtureSymlink(t, "f.txt", filepath.Join(upper, "link"))
		makeFixtureSymlink(t, "f.txt", filepath.Join(lower, "link"))
		report, err := pruneUpper(upper, lower, plainHooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{RemovedLinks: 1})
		assertGone(t, filepath.Join(upper, "link"))
	})
	t.Run("different target kept", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		makeFixtureSymlink(t, "f.txt", filepath.Join(upper, "link"))
		makeFixtureSymlink(t, "other.txt", filepath.Join(lower, "link"))
		report, err := pruneUpper(upper, lower, plainHooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{Kept: 1})
		assertExists(t, filepath.Join(upper, "link"))
	})
	t.Run("lower missing kept", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		makeFixtureSymlink(t, "f.txt", filepath.Join(upper, "link"))
		report, err := pruneUpper(upper, lower, plainHooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{Kept: 1})
	})
	t.Run("lower is a regular file kept", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		makeFixtureSymlink(t, "f.txt", filepath.Join(upper, "link"))
		writeFixtureFile(t, filepath.Join(lower, "link"), "hello", 0o644)
		report, err := pruneUpper(upper, lower, plainHooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{Kept: 1})
		assertExists(t, filepath.Join(upper, "link"))
	})
}

func TestPruneUpperHardLinkedFileKept(t *testing.T) {
	upper, lower := fixtureRoots(t)
	writeFixtureFile(t, filepath.Join(upper, "a.txt"), "hello", 0o644)
	if err := os.Link(filepath.Join(upper, "a.txt"), filepath.Join(upper, "b.txt")); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(lower, "a.txt"), "hello", 0o644)
	writeFixtureFile(t, filepath.Join(lower, "b.txt"), "hello", 0o644)
	seam := newFakePlatform()
	for _, name := range []string{"a.txt", "b.txt"} {
		seam.stats[filepath.Join(upper, name)] = platformStat{nlink: 2}
	}
	report, err := pruneUpper(upper, lower, seam.hooks())
	if err != nil {
		t.Fatal(err)
	}
	wantReport(t, report, PruneReport{Kept: 2})
	assertExists(t, filepath.Join(upper, "a.txt"))
	assertExists(t, filepath.Join(upper, "b.txt"))
}

func TestPruneUpperSpecialFilesKept(t *testing.T) {
	t.Run("fifo and socket kept", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		fifo := filepath.Join(upper, "fifo")
		if err := syscall.Mknod(fifo, syscall.S_IFIFO|0o644, 0); err != nil {
			t.Skipf("fifo creation unavailable: %v", err)
		}
		sock := filepath.Join(upper, "sock")
		listener, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = listener.Close() }()
		report, err := pruneUpper(upper, lower, plainHooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{Kept: 2})
		assertExists(t, fifo)
		assertExists(t, sock)
	})
	t.Run("real device node kept", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		dev := filepath.Join(upper, "dev")
		if err := syscall.Mknod(dev, syscall.S_IFCHR|0o600, 1<<8|3); err != nil {
			t.Skipf("creating device nodes needs privilege: %v", err)
		}
		seam := newFakePlatform()
		seam.stats[dev] = platformStat{nlink: 1, rdev: 1<<8 | 3}
		report, err := pruneUpper(upper, lower, seam.hooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{Kept: 1})
		assertExists(t, dev)
	})
}

// TestPruneUpperWhiteout exercises the zero-rdev character device
// rule against the real platform probes. Creating device nodes needs
// privilege, so the test skips without it.
func TestPruneUpperWhiteout(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("whiteout probing needs linux")
	}
	mknodWhiteout := func(t *testing.T, path string) {
		t.Helper()
		if err := syscall.Mknod(path, syscall.S_IFCHR|0o600, 0); err != nil {
			t.Skipf("creating whiteout device nodes needs privilege: %v", err)
		}
	}
	t.Run("lower gone whiteout removed", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		mknodWhiteout(t, filepath.Join(upper, "gone.txt"))
		report, err := PruneUpper(upper, lower)
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{RemovedWhiteouts: 1})
		assertGone(t, filepath.Join(upper, "gone.txt"))
		assertExists(t, upper)
	})
	t.Run("lower still serves path whiteout kept", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		mknodWhiteout(t, filepath.Join(upper, "live.txt"))
		writeFixtureFile(t, filepath.Join(lower, "live.txt"), "hello", 0o644)
		report, err := PruneUpper(upper, lower)
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{Kept: 1})
		assertExists(t, filepath.Join(upper, "live.txt"))
	})
}

func TestPruneUpperTypeMismatchKept(t *testing.T) {
	t.Run("file over directory kept", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		writeFixtureFile(t, filepath.Join(upper, "x"), "hello", 0o644)
		makeFixtureDir(t, filepath.Join(lower, "x"))
		report, err := pruneUpper(upper, lower, plainHooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{Kept: 1})
		assertExists(t, filepath.Join(upper, "x"))
	})
	t.Run("directory over file kept", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		makeFixtureDir(t, filepath.Join(upper, "x"))
		writeFixtureFile(t, filepath.Join(lower, "x"), "hello", 0o644)
		report, err := pruneUpper(upper, lower, plainHooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{Kept: 1})
		assertExists(t, filepath.Join(upper, "x"))
	})
}

func TestPruneUpperMetacopyStub(t *testing.T) {
	// newStub builds an upper metacopy stub and its lower data file.
	newStub := func(t *testing.T, stubPerm os.FileMode, stubMtime int64, lowerKind string) (upper, lower string, seam *fakePlatform) {
		t.Helper()
		upper, lower = fixtureRoots(t)
		seam = newFakePlatform()
		switch lowerKind {
		case "file":
			writeFixtureFile(t, filepath.Join(lower, "f.txt"), "hello", 0o644)
		case "dir":
			makeFixtureDir(t, filepath.Join(lower, "f.txt"))
		}
		writeFixtureFile(t, filepath.Join(upper, "f.txt"), "", stubPerm)
		setFixtureTime(t, filepath.Join(upper, "f.txt"), stubMtime, 0)
		seam.mark(filepath.Join(upper, "f.txt"), xattrMetacopy)
		return upper, lower, seam
	}
	t.Run("matching stub removed", func(t *testing.T) {
		upper, lower, seam := newStub(t, 0o644, fixtureMtime, "file")
		report, err := pruneUpper(upper, lower, seam.hooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{RemovedFiles: 1})
		assertGone(t, filepath.Join(upper, "f.txt"))
	})
	t.Run("permission differs kept", func(t *testing.T) {
		upper, lower, seam := newStub(t, 0o600, fixtureMtime, "file")
		report, err := pruneUpper(upper, lower, seam.hooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{SkippedMetacopy: 1})
		assertExists(t, filepath.Join(upper, "f.txt"))
	})
	t.Run("setuid stub kept", func(t *testing.T) {
		upper, lower, seam := newStub(t, 0o644|os.ModeSetuid, fixtureMtime, "file")
		report, err := pruneUpper(upper, lower, seam.hooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{SkippedMetacopy: 1})
		assertExists(t, filepath.Join(upper, "f.txt"))
	})
	t.Run("owner differs kept", func(t *testing.T) {
		upper, lower, seam := newStub(t, 0o644, fixtureMtime, "file")
		seam.stats[filepath.Join(upper, "f.txt")] = platformStat{nlink: 1, gid: 65534}
		report, err := pruneUpper(upper, lower, seam.hooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{SkippedMetacopy: 1})
		assertExists(t, filepath.Join(upper, "f.txt"))
	})
	t.Run("mtime second differs kept", func(t *testing.T) {
		upper, lower, seam := newStub(t, 0o644, fixtureMtime+5, "file")
		report, err := pruneUpper(upper, lower, seam.hooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{SkippedMetacopy: 1})
		assertExists(t, filepath.Join(upper, "f.txt"))
	})
	t.Run("lower missing kept", func(t *testing.T) {
		upper, lower, seam := newStub(t, 0o644, fixtureMtime, "missing")
		report, err := pruneUpper(upper, lower, seam.hooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{SkippedMetacopy: 1})
		assertExists(t, filepath.Join(upper, "f.txt"))
	})
	t.Run("lower type mismatch kept", func(t *testing.T) {
		upper, lower, seam := newStub(t, 0o644, fixtureMtime, "dir")
		report, err := pruneUpper(upper, lower, seam.hooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{SkippedMetacopy: 1})
		assertExists(t, filepath.Join(upper, "f.txt"))
	})
	t.Run("stub carrying data kept", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		seam := newFakePlatform()
		writeFixtureFile(t, filepath.Join(lower, "f.txt"), "hello", 0o644)
		writeFixtureFile(t, filepath.Join(upper, "f.txt"), "hello", 0o644)
		seam.mark(filepath.Join(upper, "f.txt"), xattrMetacopy)
		report, err := pruneUpper(upper, lower, seam.hooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{SkippedMetacopy: 1})
		assertExists(t, filepath.Join(upper, "f.txt"))
	})
}

func TestPruneUpperRedirectSkipsSubtree(t *testing.T) {
	upper, lower := fixtureRoots(t)
	makeFixtureDir(t, filepath.Join(upper, "d"))
	writeFixtureFile(t, filepath.Join(upper, "d", "f.txt"), "hello", 0o644)
	writeFixtureFile(t, filepath.Join(lower, "d", "f.txt"), "hello", 0o644)
	seam := newFakePlatform()
	seam.mark(filepath.Join(upper, "d"), xattrRedirect)
	report, err := pruneUpper(upper, lower, seam.hooks())
	if err != nil {
		t.Fatal(err)
	}
	wantReport(t, report, PruneReport{SkippedRedirect: 1})
	assertExists(t, filepath.Join(upper, "d", "f.txt"))
}

func TestPruneUpperOpaqueDir(t *testing.T) {
	// newOpaque builds an upper directory carrying the opaque
	// marker, optionally with a child file, plus the lower side.
	newOpaque := func(t *testing.T, child bool, lowerKind string) (upper, lower string, dir string, seam *fakePlatform) {
		t.Helper()
		upper, lower = fixtureRoots(t)
		seam = newFakePlatform()
		dir = filepath.Join(upper, "d")
		makeFixtureDir(t, dir)
		seam.mark(dir, xattrOpaque)
		if child {
			writeFixtureFile(t, filepath.Join(dir, "f.txt"), "hello", 0o644)
		}
		switch lowerKind {
		case "content":
			writeFixtureFile(t, filepath.Join(lower, "d", "f.txt"), "hello", 0o644)
		case "empty":
			makeFixtureDir(t, filepath.Join(lower, "d"))
		}
		return upper, lower, dir, seam
	}
	t.Run("empty dir without lower removed", func(t *testing.T) {
		upper, lower, dir, seam := newOpaque(t, false, "missing")
		report, err := pruneUpper(upper, lower, seam.hooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{RemovedDirs: 1})
		assertGone(t, dir)
	})
	t.Run("empty dir over empty lower removed", func(t *testing.T) {
		upper, lower, dir, seam := newOpaque(t, false, "empty")
		report, err := pruneUpper(upper, lower, seam.hooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{RemovedDirs: 1})
		assertGone(t, dir)
	})
	t.Run("lower content keeps the whole subtree", func(t *testing.T) {
		upper, lower, dir, seam := newOpaque(t, true, "content")
		report, err := pruneUpper(upper, lower, seam.hooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{Kept: 1})
		assertExists(t, dir)
		assertExists(t, filepath.Join(dir, "f.txt"))
		if !seam.has(dir, xattrOpaque) {
			t.Fatal("opaque marker was touched")
		}
	})
	t.Run("non-empty dir without lower stripped", func(t *testing.T) {
		upper, lower, dir, seam := newOpaque(t, true, "missing")
		report, err := pruneUpper(upper, lower, seam.hooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{StrippedOpaque: 1, Kept: 1})
		assertExists(t, dir)
		assertExists(t, filepath.Join(dir, "f.txt"))
		if seam.has(dir, xattrOpaque) {
			t.Fatal("opaque marker survived the strip")
		}
	})
	t.Run("non-empty dir over empty lower stripped", func(t *testing.T) {
		upper, lower, dir, seam := newOpaque(t, true, "empty")
		report, err := pruneUpper(upper, lower, seam.hooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{StrippedOpaque: 1, Kept: 1})
		assertExists(t, dir)
		if seam.has(dir, xattrOpaque) {
			t.Fatal("opaque marker survived the strip")
		}
	})
}

func TestPruneUpperPlainDirs(t *testing.T) {
	t.Run("empty dir without lower removed", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		makeFixtureDir(t, filepath.Join(upper, "d"))
		report, err := pruneUpper(upper, lower, plainHooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{RemovedDirs: 1})
		assertGone(t, filepath.Join(upper, "d"))
	})
	t.Run("empty dir over lower dir kept", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		makeFixtureDir(t, filepath.Join(upper, "d"))
		makeFixtureDir(t, filepath.Join(lower, "d"))
		report, err := pruneUpper(upper, lower, plainHooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{Kept: 1})
		assertExists(t, filepath.Join(upper, "d"))
	})
	t.Run("nested empty dirs removed post-order", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		makeFixtureDir(t, filepath.Join(upper, "d", "e"))
		report, err := pruneUpper(upper, lower, plainHooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{RemovedDirs: 2})
		assertGone(t, filepath.Join(upper, "d"))
	})
	t.Run("dir emptied by pruning over lower kept", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		writeFixtureFile(t, filepath.Join(upper, "d", "f.txt"), "hello", 0o644)
		writeFixtureFile(t, filepath.Join(lower, "d", "f.txt"), "hello", 0o644)
		report, err := pruneUpper(upper, lower, plainHooks())
		if err != nil {
			t.Fatal(err)
		}
		// The child matched and went away, but the lower still has
		// the directory, so the cleaned parent stays.
		wantReport(t, report, PruneReport{RemovedFiles: 1, Kept: 1})
		assertExists(t, filepath.Join(upper, "d"))
	})
	t.Run("non-empty dir kept", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		writeFixtureFile(t, filepath.Join(upper, "d", "new.txt"), "new", 0o644)
		report, err := pruneUpper(upper, lower, plainHooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{Kept: 2})
		assertExists(t, filepath.Join(upper, "d"))
	})
}

// TestPruneUpperReadErrorsKeepFailSafe covers the fail-safe rule for
// unreadable fixtures. Root can read mode 000 directories, so the
// test skips there.
func TestPruneUpperReadErrorsKeepFailSafe(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode 000 directories")
	}
	lockDown := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	}
	unlock := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("unreadable upper dir kept", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		writeFixtureFile(t, filepath.Join(upper, "d", "f.txt"), "hello", 0o644)
		hidden := filepath.Join(upper, "d")
		lockDown(t, hidden)
		report, err := pruneUpper(upper, lower, plainHooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{Errors: 1})
		unlock(t, hidden)
		assertExists(t, filepath.Join(hidden, "f.txt"))
	})
	t.Run("unreadable lower keeps the matching file", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		writeFixtureFile(t, filepath.Join(upper, "f.txt"), "hello", 0o644)
		writeFixtureFile(t, filepath.Join(lower, "f.txt"), "hello", 0o644)
		lockDown(t, lower)
		report, err := pruneUpper(upper, lower, plainHooks())
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{Errors: 1})
		unlock(t, lower)
		assertExists(t, filepath.Join(upper, "f.txt"))
	})
	t.Run("unreadable upper root is an error", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		writeFixtureFile(t, filepath.Join(upper, "f.txt"), "hello", 0o644)
		lockDown(t, upper)
		if _, err := pruneUpper(upper, lower, plainHooks()); err == nil {
			t.Fatal("PruneUpper accepted an unreadable upper root")
		}
		unlock(t, upper)
		assertExists(t, filepath.Join(upper, "f.txt"))
	})
}

// TestPruneUpperReportCounters walks one mixed tree and pins every
// counter of the report.
func TestPruneUpperReportCounters(t *testing.T) {
	upper, lower := fixtureRoots(t)
	writeFixtureFile(t, filepath.Join(upper, "same.txt"), "hello", 0o644)
	writeFixtureFile(t, filepath.Join(lower, "same.txt"), "hello", 0o644)
	writeFixtureFile(t, filepath.Join(upper, "changed.txt"), "hello!", 0o644)
	writeFixtureFile(t, filepath.Join(lower, "changed.txt"), "hello", 0o644)
	writeFixtureFile(t, filepath.Join(upper, "new.txt"), "new", 0o644)
	makeFixtureSymlink(t, "same.txt", filepath.Join(upper, "link"))
	makeFixtureSymlink(t, "same.txt", filepath.Join(lower, "link"))
	writeFixtureFile(t, filepath.Join(upper, "d", "inner.txt"), "inner", 0o644)
	writeFixtureFile(t, filepath.Join(lower, "d", "inner.txt"), "inner", 0o644)
	makeFixtureDir(t, filepath.Join(upper, "e"))
	report, err := pruneUpper(upper, lower, plainHooks())
	if err != nil {
		t.Fatal(err)
	}
	wantReport(t, report, PruneReport{
		RemovedFiles: 2, // same.txt and d/inner.txt
		RemovedLinks: 1, // link
		RemovedDirs:  1, // e
		Kept:         3, // changed.txt, new.txt, and the emptied d
	})
	assertGone(t, filepath.Join(upper, "same.txt"))
	assertGone(t, filepath.Join(upper, "link"))
	assertGone(t, filepath.Join(upper, "d", "inner.txt"))
	assertGone(t, filepath.Join(upper, "e"))
	assertExists(t, filepath.Join(upper, "changed.txt"))
	assertExists(t, filepath.Join(upper, "new.txt"))
	assertExists(t, filepath.Join(upper, "d"))
}

// TestPruneUpperRealProbesOnLinux runs the exported entry point with
// the real platform probes: raw stat fields through lstat and the
// overlay marker scan on paths that carry no markers.
func TestPruneUpperRealProbesOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the real overlay metadata probes need linux")
	}
	t.Run("identical file removed", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		writeFixtureFile(t, filepath.Join(upper, "f.txt"), "hello", 0o644)
		writeFixtureFile(t, filepath.Join(lower, "f.txt"), "hello", 0o644)
		report, err := PruneUpper(upper, lower)
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{RemovedFiles: 1})
		assertGone(t, filepath.Join(upper, "f.txt"))
	})
	t.Run("matching symlink removed", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		makeFixtureSymlink(t, "f.txt", filepath.Join(upper, "link"))
		makeFixtureSymlink(t, "f.txt", filepath.Join(lower, "link"))
		report, err := PruneUpper(upper, lower)
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{RemovedLinks: 1})
	})
	t.Run("hard linked file kept", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		writeFixtureFile(t, filepath.Join(upper, "f.txt"), "hello", 0o644)
		if err := os.Link(filepath.Join(upper, "f.txt"), filepath.Join(upper, "g.txt")); err != nil {
			t.Fatal(err)
		}
		writeFixtureFile(t, filepath.Join(lower, "f.txt"), "hello", 0o644)
		writeFixtureFile(t, filepath.Join(lower, "g.txt"), "hello", 0o644)
		report, err := PruneUpper(upper, lower)
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{Kept: 2})
		assertExists(t, filepath.Join(upper, "f.txt"))
	})
	t.Run("empty dir removed", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		makeFixtureDir(t, filepath.Join(upper, "d"))
		report, err := PruneUpper(upper, lower)
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{RemovedDirs: 1})
	})
	t.Run("setuid copy-up kept", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		writeFixtureFile(t, filepath.Join(lower, "f.txt"), "hello", 0o644)
		writeFixtureFile(t, filepath.Join(upper, "f.txt"), "hello", 0o644|os.ModeSetuid)
		report, err := PruneUpper(upper, lower)
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{Kept: 1})
		assertExists(t, filepath.Join(upper, "f.txt"))
	})
}

// TestPruneUpperKeepsEverythingOffLinux pins the always-conservative
// stubs: off Linux no probe succeeds, so nothing is removed. The
// root marker probe fails first and keeps the whole tree fail-safe,
// counting a single error.
func TestPruneUpperKeepsEverythingOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("linux probes the real overlay metadata")
	}
	upper, lower := fixtureRoots(t)
	writeFixtureFile(t, filepath.Join(upper, "f.txt"), "hello", 0o644)
	writeFixtureFile(t, filepath.Join(lower, "f.txt"), "hello", 0o644)
	makeFixtureDir(t, filepath.Join(upper, "d"))
	makeFixtureSymlink(t, "f.txt", filepath.Join(upper, "link"))
	report, err := PruneUpper(upper, lower)
	if err != nil {
		t.Fatal(err)
	}
	wantReport(t, report, PruneReport{Errors: 1})
	assertExists(t, filepath.Join(upper, "f.txt"))
	assertExists(t, filepath.Join(upper, "d"))
	assertExists(t, filepath.Join(upper, "link"))
}
