//go:build linux

package workspace

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

// TestOverlayXattrHelpersRoundTrip exercises the raw syscall helpers
// with the user xattr namespace, which an unprivileged owner may
// use. The overlay marker scan must filter these names out.
func TestOverlayXattrHelpersRoundTrip(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	const name = "user.tiler-probe"
	if err := syscall.Setxattr(file, name, []byte("1"), 0); err != nil {
		t.Skipf("xattr support unavailable here: %v", err)
	}
	names, err := listXattr(file)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(names, name) {
		t.Fatalf("listXattr = %v, want %q included", names, name)
	}
	value, err := getXattr(file, name)
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != "1" {
		t.Fatalf("getXattr = %q, want %q", value, "1")
	}
	markers, err := overlayXattrs(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(markers) != 0 {
		t.Fatalf("overlayXattrs = %v, want the user namespace filtered out", markers)
	}
	if err := stripOverlayXattr(file, name); err != nil {
		t.Fatal(err)
	}
	names, err = listXattr(file)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(names, name) {
		t.Fatalf("listXattr = %v, want %q stripped", names, name)
	}
}

// TestOverlayXattrsEmptyOnFreshPath covers the no-xattr answer the
// prune rules rely on for plain fixtures.
func TestOverlayXattrsEmptyOnFreshPath(t *testing.T) {
	dir := t.TempDir()
	markers, err := overlayXattrs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(markers) != 0 {
		t.Fatalf("overlayXattrs = %v, want empty", markers)
	}
}

func TestPruneUpperKeepsFuseOpaqueContents(t *testing.T) {
	for _, marker := range []string{"xattr", "file"} {
		for _, directory := range []string{".", "objects"} {
			t.Run(marker+"/"+directory, func(t *testing.T) {
				upper, lower := fixtureRoots(t)
				dir := filepath.Join(upper, directory)
				file := filepath.Join(directory, "published")
				writeFixtureFile(t, filepath.Join(upper, file), "baked", 0o644)
				writeFixtureFile(t, filepath.Join(lower, file), "baked", 0o644)
				if marker == "xattr" {
					if err := syscall.Setxattr(dir, xattrFuseOpaque, []byte("y"), 0); err != nil {
						t.Skipf("user xattrs unavailable: %v", err)
					}
				} else {
					writeFixtureFile(t, filepath.Join(dir, ".wh..wh..opq"), "", 0o644)
				}
				report, err := PruneUpper(upper, lower)
				if err != nil {
					t.Fatal(err)
				}
				assertExists(t, filepath.Join(upper, file))
				markers, err := overlayXattrs(dir)
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := markers[xattrFuseOpaque]; !ok || report.RemovedFiles != 0 {
					t.Fatalf("opaque contents changed: markers=%v report=%+v", markers, report)
				}
			})
		}
	}
}

// TestPruneUpperRealOpaqueMarker walks the exported entry point
// against real trusted.overlay.opaque markers. Writing the trusted
// namespace needs privilege, so the test skips without it.
func TestPruneUpperRealOpaqueMarker(t *testing.T) {
	setOpaque := func(t *testing.T, dir string) {
		t.Helper()
		if err := syscall.Setxattr(dir, xattrOpaque, []byte("y"), 0); err != nil {
			t.Skipf("setting trusted xattrs needs privilege: %v", err)
		}
	}
	hasOpaque := func(t *testing.T, dir string) bool {
		t.Helper()
		markers, err := overlayXattrs(dir)
		if err != nil {
			t.Fatal(err)
		}
		_, ok := markers[xattrOpaque]
		return ok
	}
	t.Run("empty opaque dir removed", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		dir := filepath.Join(upper, "d")
		makeFixtureDir(t, dir)
		setOpaque(t, dir)
		report, err := PruneUpper(upper, lower)
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{RemovedDirs: 1})
		assertGone(t, dir)
	})
	t.Run("non-empty opaque dir stripped", func(t *testing.T) {
		upper, lower := fixtureRoots(t)
		dir := filepath.Join(upper, "d")
		writeFixtureFile(t, filepath.Join(dir, "f.txt"), "hello", 0o644)
		setOpaque(t, dir)
		report, err := PruneUpper(upper, lower)
		if err != nil {
			t.Fatal(err)
		}
		wantReport(t, report, PruneReport{StrippedOpaque: 1, Kept: 1})
		assertExists(t, dir)
		assertExists(t, filepath.Join(dir, "f.txt"))
		if hasOpaque(t, dir) {
			t.Fatal("opaque marker survived the strip")
		}
	})
}
