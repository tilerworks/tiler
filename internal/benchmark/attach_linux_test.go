//go:build linux

package benchmark

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/container"
)

func TestAttachFUSEMatchesMountArgv(t *testing.T) {
	cases := []struct {
		typ  string
		tool string
	}{
		{typ: "squashfs", tool: "squashfuse_ll"},
		{typ: "erofs", tool: "erofsfuse"},
	}
	for _, tc := range cases {
		t.Run(tc.typ, func(t *testing.T) {
			rec := &argvRec{}
			host := fuseHost(tc.tool, 0)
			image := "/bench/app." + tc.typ
			got, err := Attach(rec.command, host, tc.typ, image, packPoint)
			if err != nil {
				t.Fatalf("Attach: %v", err)
			}
			if got.Tool != tc.tool {
				t.Fatalf("tool = %q", got.Tool)
			}
			format, err := container.Lookup(tc.typ)
			if err != nil {
				t.Fatal(err)
			}
			want := format.MountArgv(image, "", "", image, packPoint)
			if len(rec.calls) != 1 || rec.calls[0].name != want[0] || rec.calls[0].env != nil ||
				!slices.Equal(rec.calls[0].args, want[1:]) {
				t.Fatalf("call = %s, want %q", rec.calls[0], want)
			}
		})
	}
}

func TestAttachLoopMountWhenRootLacksFUSE(t *testing.T) {
	cases := []string{"squashfs", "erofs"}
	for _, typ := range cases {
		t.Run(typ, func(t *testing.T) {
			rec := &argvRec{}
			host := Host{
				LookPath: func(string) (string, error) {
					return "", errors.New("not found")
				},
				Euid: func() int { return 0 },
			}
			image := "/bench/app." + typ
			got, err := Attach(rec.command, host, typ, image, packPoint)
			if err != nil {
				t.Fatalf("Attach: %v", err)
			}
			if got.Tool != "mount" {
				t.Fatalf("tool = %q", got.Tool)
			}
			want := []string{"-t", typ, "-o", "ro,loop", image, packPoint}
			if len(rec.calls) != 1 || rec.calls[0].name != "mount" || !slices.Equal(rec.calls[0].args, want) {
				t.Fatalf("calls = %v", rec.calls)
			}
		})
	}
}

func TestAttachNamesMissingFUSEClientForNonRoot(t *testing.T) {
	cases := []struct {
		typ  string
		tool string
	}{
		{typ: "squashfs", tool: "squashfuse_ll"},
		{typ: "erofs", tool: "erofsfuse"},
	}
	for _, tc := range cases {
		t.Run(tc.typ, func(t *testing.T) {
			rec := &argvRec{}
			host := Host{
				LookPath: func(string) (string, error) {
					return "", errors.New("not found")
				},
				Euid: func() int { return 1000 },
			}
			_, err := Attach(rec.command, host, tc.typ, "/bench/app", packPoint)
			if err == nil || !strings.Contains(err.Error(), tc.tool) {
				t.Fatalf("Attach error = %v", err)
			}
			if len(rec.calls) != 0 {
				t.Fatalf("calls = %v", rec.calls)
			}
		})
	}
}

func TestAttachDMGOnLinuxReturnsError(t *testing.T) {
	rec := &argvRec{}
	_, err := Attach(rec.command, Host{}, "dmg", packImage, packPoint)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("Attach error = %v", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("calls = %v", rec.calls)
	}
}

func TestDetachFUSEUsesFusermount3(t *testing.T) {
	rec := &argvRec{}
	err := Detach(rec.command, Mount{Tool: "squashfuse_ll"}, packPoint)
	if err != nil {
		t.Fatalf("Detach: %v", err)
	}
	want := []string{"-u", packPoint}
	if len(rec.calls) != 1 || rec.calls[0].name != "fusermount3" || !slices.Equal(rec.calls[0].args, want) {
		t.Fatalf("calls = %v", rec.calls)
	}
}

func TestDetachLoopUsesUmount(t *testing.T) {
	rec := &argvRec{}
	if err := Detach(rec.command, Mount{Tool: "mount"}, packPoint); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	if len(rec.calls) != 1 || rec.calls[0].name != "umount" || !slices.Equal(rec.calls[0].args, []string{packPoint}) {
		t.Fatalf("calls = %v", rec.calls)
	}
}

func fuseHost(tool string, euid int) Host {
	return Host{
		LookPath: func(name string) (string, error) {
			if name == tool {
				return "/usr/bin/" + tool, nil
			}
			return "", errors.New("not found")
		},
		Euid: func() int { return euid },
	}
}
