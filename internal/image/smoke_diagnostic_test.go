package image

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/container"
)

func TestSmokeMountPermissionDiagnostic(t *testing.T) {
	format, err := container.Lookup("squashfs")
	if err != nil {
		t.Fatal(err)
	}
	original := smokeRun
	t.Cleanup(func() { smokeRun = original })
	for _, tc := range []struct {
		name   string
		cause  error
		denied bool
	}{
		{"mount denied", errors.New("squashfuse_ll: exit status 1: fusermount3: mount failed: Operation not permitted"), true},
		{"permission denied", errors.New("squashfuse_ll: Permission denied"), true},
		{"typed permission", fmt.Errorf("mount: %w", os.ErrPermission), true},
		{"corrupt image", errors.New("squashfuse_ll: invalid superblock"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			smokeRun = func(string, ...string) error {
				calls++
				return tc.cause
			}
			err := runIncrSmoke(IncrRequest{MountSmoke: func(path string) error {
				return smokeMount(format, path)
			}}, "image.squashfs", ImageMeta{}, Format{})
			if !errors.Is(err, tc.cause) || calls != 1 {
				t.Fatalf("error=%v, calls=%d", err, calls)
			}
			if !tc.denied {
				if strings.Contains(err.Error(), "Hint:") || !strings.Contains(err.Error(), "invalid superblock") ||
					!strings.Contains(err.Error(), "build: image validation failed: smoke mount:") || strings.Count(err.Error(), "build:") != 1 {
					t.Fatalf("unexpected diagnostic: %v", err)
				}
				return
			}
			for _, want := range []string{"build: image validation failed: FUSE smoke mount",
				"/dev/fuse access, fusermount3 permissions", "CAP_SYS_ADMIN", "For Docker",
				"--device /dev/fuse --cap-add SYS_ADMIN", "seccomp/AppArmor/SELinux"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("diagnostic %q missing %q", err, want)
				}
			}
			if strings.Count(err.Error(), "build:") != 1 || strings.Contains(err.Error(), "exit status") {
				t.Fatalf("redundant context: %v", err)
			}
		})
	}
}

func TestSkippedSmokeReportsReasonWithoutFailingBuild(t *testing.T) {
	var out bytes.Buffer
	req := IncrRequest{Name: "my/app", Diagnostics: &out, MountSmoke: func(string) error {
		return fmt.Errorf("%w: fusermount3 is missing", ErrSmokeSkipped)
	}}
	if err := runIncrSmoke(req, "image", ImageMeta{}, Format{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Warning: build: image "my/app" was not mount-validated.`, "fusermount3 is missing", "tiler doctor"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("notice %q missing %q", out.String(), want)
		}
	}
	if strings.Contains(out.String(), "mount smoke gate") {
		t.Fatalf("internal skip context leaked: %q", out.String())
	}
}
