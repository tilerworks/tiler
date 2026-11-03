package doctor

import (
	"context"
	"strings"
	"testing"
)

func TestClassifyOS(t *testing.T) {
	tests := []struct {
		name string
		text string
		want distro
	}{
		{name: "ubuntu", text: "ID=ubuntu\nID_LIKE=debian\n", want: distroDebian},
		{name: "debian", text: "ID=debian\n", want: distroDebian},
		{name: "linux mint", text: "ID=linuxmint\nID_LIKE=ubuntu\n", want: distroDebian},
		{name: "fedora", text: "ID=fedora\n", want: distroRedHat},
		{name: "rhel", text: "ID=\"rhel\"\nID_LIKE=\"fedora\"\n", want: distroRedHat},
		{name: "rocky", text: "ID=rocky\nID_LIKE=\"rhel centos fedora\"\n", want: distroRedHat},
		{name: "empty", text: "", want: distroUnknown},
		{name: "arch", text: "ID=arch\nID_LIKE=archlinux\n", want: distroUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyOS(tt.text); got != tt.want {
				t.Fatalf("classifyOS = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInstallHintDedupesPackages(t *testing.T) {
	got := installHint([]string{"mksquashfs", "unsquashfs", "squashfuse_ll"}, distroDebian)
	want := "install: sudo apt install squashfs-tools squashfuse"
	if got != want {
		t.Fatalf("hint = %q, want %q", got, want)
	}
	if installHint(nil, distroDebian) != "" {
		t.Fatal("empty binaries produced a hint")
	}
	if installHint([]string{"not-a-tool"}, distroUnknown) != "" {
		t.Fatal("unknown binary produced a hint")
	}
}

func TestInstallGuidanceFollowsDistro(t *testing.T) {
	orig := readOSRelease
	t.Cleanup(func() { readOSRelease = orig })

	tests := []struct {
		name    string
		release string
		readErr error
		missing []string
		want    []string
		absent  []string
	}{
		{
			name:    "ubuntu shows apt only",
			release: "ID=ubuntu\nID_LIKE=debian\n",
			missing: []string{"mkfs.erofs", "erofsfuse"},
			want:    []string{"sudo apt install erofs-utils erofsfuse"},
			absent:  []string{"dnf"},
		},
		{
			name:    "fedora shows dnf only",
			release: "ID=fedora\n",
			missing: []string{"mkfs.erofs", "erofsfuse"},
			want:    []string{"sudo dnf install erofs-utils erofs-fuse"},
			absent:  []string{"apt"},
		},
		{
			name:    "rocky prepares EPEL and CRB",
			release: "ID=rocky\nVERSION_ID=9.8\nID_LIKE=\"rhel centos fedora\"\n",
			missing: []string{"mkfs.erofs", "erofsfuse"},
			want:    []string{"sudo dnf install dnf-plugins-core epel-release", "sudo dnf config-manager --set-enabled crb", "sudo dnf install erofs-utils erofs-fuse"},
			absent:  []string{"sudo apt"},
		},
		{
			name:    "unreadable os-release shows both",
			readErr: osReleaseUnreadable{},
			missing: []string{"mksquashfs", "squashfuse_ll", "mkfs.erofs", "erofsfuse"},
			want: []string{
				"sudo apt install erofs-utils erofsfuse squashfs-tools squashfuse",
				"sudo dnf install erofs-utils erofs-fuse squashfs-tools squashfuse",
			},
		},
		{
			name:    "unknown distro shows both",
			release: "ID=arch\n",
			missing: []string{"git"},
			want:    []string{"sudo apt install git", "sudo dnf install git"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readOSRelease = func() (string, error) {
				return tt.release, tt.readErr
			}
			host := healthyHost("0.5.0")
			for _, binary := range tt.missing {
				host = host.without(binary)
			}
			results, err := Probe(context.Background(), host.seams(), Options{})
			if err != nil {
				t.Fatal(err)
			}
			detail := installDetail(t, results, tt.missing)
			for _, want := range tt.want {
				if !strings.Contains(detail, want) {
					t.Fatalf("detail = %q, want %q", detail, want)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(detail, absent) {
					t.Fatalf("detail = %q, contains %q", detail, absent)
				}
			}
		})
	}
}

func TestHealthyProbeOmitsInstallHint(t *testing.T) {
	orig := readOSRelease
	t.Cleanup(func() { readOSRelease = orig })
	readOSRelease = func() (string, error) {
		return "ID=ubuntu\n", nil
	}
	results, err := Probe(context.Background(), healthyHost("0.5.0").seams(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if strings.Contains(result.Detail, "install:") {
			t.Fatalf("%s detail = %q, want no install hint", result.Name, result.Detail)
		}
	}
}

func TestRepositoryGuidance(t *testing.T) {
	for _, tc := range []struct {
		release string
		binary  string
		want    string
	}{
		{"ID=rocky\nVERSION_ID=8.10", "mkfs.erofs", "--set-enabled powertools"},
		{"ID=almalinux\nVERSION_ID=10.0", "erofsfuse", "--set-enabled crb"},
		{"ID=rhel\nVERSION_ID=9.8", "squashfuse_ll", "CodeReady Builder"},
		{"ID=fedora\nVERSION_ID=43", "squashfuse_ll", ""},
		{"ID=rocky\nVERSION_ID=9.8", "git", ""},
		{"ID=ubuntu", "erofsfuse", ""},
	} {
		got := repositoryHint(tc.release, []string{tc.binary})
		if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
			t.Errorf("repositoryHint(%q, %q) = %q, want %q", tc.release, tc.binary, got, tc.want)
		}
	}
}

// installDetail is the toolchain line, or the shared-tool line when
// the only missing binary is fuse-overlayfs, fusermount3, or git.
func installDetail(t *testing.T, results []Result, missing []string) string {
	t.Helper()
	got := byName(t, results)
	if len(missing) == 1 {
		switch missing[0] {
		case "git":
			return got[nameGit].Detail
		case "fuse-overlayfs":
			return got[nameFuseOverlayfs].Detail
		case "fusermount3":
			return got[nameFusermount3].Detail
		case "squashfuse_ll":
			return got[nameSquashfuseSub].Detail
		}
	}
	return got[nameImageToolchain].Detail
}

type osReleaseUnreadable struct{}

func (osReleaseUnreadable) Error() string { return "unreadable" }
