package doctor

import (
	"context"
	"errors"
	"testing"
)

func TestParseVersionAcceptsTwoComponentBanner(t *testing.T) {
	// fuse-overlayfs 1.18 prints only "version 1.18" with no patch
	// component. A two-component number is still that tool's version.
	ver, ok := parseVersion("fuse-overlayfs: version 1.18")
	if !ok {
		t.Fatalf("parseVersion(%q) = not ok, want ok", "fuse-overlayfs: version 1.18")
	}
	if ver.raw != "1.18" {
		t.Fatalf("raw = %q, want %q", ver.raw, "1.18")
	}
	if ver.major != 1 || ver.minor != 18 || ver.patch != 0 {
		t.Fatalf("version = %+v, want 1.18.0", ver)
	}
}

func TestParseVersionKeepsThreeComponents(t *testing.T) {
	ver, ok := parseVersion("mksquashfs version 4.6.1 (2023/03/25)")
	if !ok {
		t.Fatal("parseVersion = not ok, want ok")
	}
	if ver.raw != "4.6.1" {
		t.Fatalf("raw = %q, want %q", ver.raw, "4.6.1")
	}
	if ver.major != 4 || ver.minor != 6 || ver.patch != 1 {
		t.Fatalf("version = %+v, want 4.6.1", ver)
	}
}

func TestSelectBannerMatchesToolNameWithTrailingColon(t *testing.T) {
	// The real fuse-overlayfs banner names the tool as "fuse-overlayfs:"
	// with a trailing colon, and reports a two-component version. The
	// three-component lines around it belong to other tools and must not
	// be selected.
	out := []byte("fuse-overlayfs: version 1.18\n" +
		"FUSE library version 3.14.0\n" +
		"using FUSE kernel interface version 7.31\n" +
		"fusermount3 version: 3.14.0\n")
	ver, ok := selectBanner(out, []string{"fuse-overlayfs", "fuse_overlayfs"})
	if !ok {
		t.Fatal("selectBanner = not ok, want the fuse-overlayfs line")
	}
	if ver.raw != "1.18" {
		t.Fatalf("raw = %q, want %q", ver.raw, "1.18")
	}
}

func TestReadVersionFallsBackToSingleDashVersion(t *testing.T) {
	// squashfs-tools 4.6.1 rejects --version and -V, printing usage and
	// exiting nonzero; only -version reports the banner.
	seams := Seams{
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			switch args[0] {
			case "--version", "-V":
				return []byte("SYNTAX:mksquashfs source1 ...\n"), errors.New("exit status 1")
			case "-version":
				return []byte("mksquashfs version 4.6.1 (2023/03/25)\n"), nil
			}
			return nil, errors.New("unexpected arg " + args[0])
		},
	}
	ver, detail, err := readVersion(context.Background(), seams, "/usr/bin/mksquashfs", []string{"mksquashfs"})
	if err != nil {
		t.Fatalf("readVersion err = %v", err)
	}
	if detail != "" {
		t.Fatalf("detail = %q, want empty", detail)
	}
	if ver.raw != "4.6.1" {
		t.Fatalf("raw = %q, want %q", ver.raw, "4.6.1")
	}
}
