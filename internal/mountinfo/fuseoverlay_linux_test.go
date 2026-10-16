//go:build linux

package mountinfo

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeFakeDaemon lays one process entry into a fake proc tree. An
// empty argv writes an empty cmdline, like a kernel thread.
func writeFakeDaemon(t *testing.T, proc string, pid int, argv ...string) {
	t.Helper()
	dir := filepath.Join(proc, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var line string
	for _, arg := range argv {
		line += arg + "\x00"
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
}

func overlayMount(point string) Mount {
	return Mount{Point: point, FSType: fuseOverlayType, Source: "fuse-overlayfs"}
}

func TestEnrichOverlaysRecoversWritableDirs(t *testing.T) {
	proc := t.TempDir()
	writeFakeDaemon(t, proc, 300, "fuse-overlayfs", "-o",
		"lowerdir=/rt/up-9-9/lower-0:/rt/up-9-9/lower-1,"+
			"upperdir=/rt/up-9-9/upper,workdir=/rt/up-9-9/work",
		"/work/view")
	mounts := []Mount{overlayMount("/work/view")}

	enrichOverlays(mounts, proc)

	if mounts[0].Upper != "/rt/up-9-9/upper" {
		t.Errorf("Upper = %q", mounts[0].Upper)
	}
	want := []string{"/rt/up-9-9/lower-0", "/rt/up-9-9/lower-1"}
	if strings.Join(mounts[0].Lowers, ":") != strings.Join(want, ":") {
		t.Errorf("Lowers = %v, want %v", mounts[0].Lowers, want)
	}
}

func TestEnrichOverlaysRecoversReadOnlyChain(t *testing.T) {
	proc := t.TempDir()
	writeFakeDaemon(t, proc, 42, "/usr/bin/fuse-overlayfs", "-o",
		"lowerdir=/rt/up-1-2/lower-0:/rt/up-1-2/lower-1", "/view")
	mounts := []Mount{overlayMount("/view")}

	enrichOverlays(mounts, proc)

	if mounts[0].Upper != "" {
		t.Errorf("a read-only chain gained Upper %q", mounts[0].Upper)
	}
	if len(mounts[0].Lowers) != 2 || mounts[0].Lowers[0] != "/rt/up-1-2/lower-0" {
		t.Errorf("Lowers = %v", mounts[0].Lowers)
	}
}

func TestEnrichOverlaysKeepsAnonymousEntryWithoutDaemon(t *testing.T) {
	proc := t.TempDir()
	writeFakeDaemon(t, proc, 7)
	writeFakeDaemon(t, proc, 9, "squashfuse_ll", "-o",
		"fsname=/img/image.sqsh", "/img/image.sqsh", "/other")
	mounts := []Mount{overlayMount("/view")}

	enrichOverlays(mounts, proc)

	if mounts[0].Upper != "" || mounts[0].Lowers != nil {
		t.Errorf("an entry without a daemon changed: %+v", mounts[0])
	}
}

func TestEnrichOverlaysPrefersNewestDaemon(t *testing.T) {
	proc := t.TempDir()
	writeFakeDaemon(t, proc, 100, "fuse-overlayfs", "-o",
		"lowerdir=/rt/up-1-1/lower-0,upperdir=/rt/up-1-1/upper,"+
			"workdir=/rt/up-1-1/work", "/view")
	writeFakeDaemon(t, proc, 400, "fuse-overlayfs", "-o",
		"lowerdir=/rt/up-2-2/lower-0,upperdir=/rt/up-2-2/upper,"+
			"workdir=/rt/up-2-2/work", "/view")
	mounts := []Mount{overlayMount("/view")}

	enrichOverlays(mounts, proc)

	if mounts[0].Upper != "/rt/up-2-2/upper" {
		t.Errorf("Upper = %q, want the newest daemon", mounts[0].Upper)
	}
}

func TestEnrichOverlaysKeepsParsedOptions(t *testing.T) {
	proc := t.TempDir()
	writeFakeDaemon(t, proc, 500, "fuse-overlayfs", "-o",
		"lowerdir=/bogus/lower-0,upperdir=/bogus/upper,workdir=/bogus/work",
		"/view")
	// A kernel overlayfs style entry already carries its
	// directories; a daemon at the same point must not shadow them.
	mounts := []Mount{{
		Point:     "/view",
		FSType:    "overlay",
		Source:    "overlay",
		SuperOpts: "rw,upperdir=/real/upper,workdir=/real/work",
		Upper:     "/real/upper",
		Lowers:    []string{"/real/lower-0"},
	}}

	enrichOverlays(mounts, proc)

	if mounts[0].Upper != "/real/upper" || mounts[0].Lowers[0] != "/real/lower-0" {
		t.Errorf("parsed directories were overwritten: %+v", mounts[0])
	}
}

func TestOverlayDaemonOptionsRejectsForeignShapes(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"another tool", []string{"squashfuse_ll", "-o", "lowerdir=/l", "/view"}},
		{"another point", []string{"fuse-overlayfs", "-o", "lowerdir=/l", "/elsewhere"}},
		{"no options", []string{"fuse-overlayfs", "/view"}},
		{"truncated", []string{"fuse-overlayfs", "-o"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := overlayDaemonOptions(tc.argv, "/view"); ok {
				t.Errorf("argv %v was accepted", tc.argv)
			}
		})
	}
}
