package doctor

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProbeShapes(t *testing.T) {
	tests := []struct {
		name    string
		host    *fakeHost
		status  map[string]string
		details map[string]string
	}{
		{
			name:   "healthy environment at baseline",
			host:   healthyHost("0.5.0"),
			status: allPass(),
			details: map[string]string{
				nameImageToolchain: "erofs, squashfs",
				nameSquashfuseSub:  "0.5.0",
				nameFuseOverlayfs:  "1.18",
				nameFusermount3:    "3.14.0",
				nameGit:            "2.43.0",
			},
		},
		{
			name:   "healthy environment above baseline",
			host:   healthyHost("0.10.0"),
			status: allPass(),
			details: map[string]string{
				nameSquashfuseSub: "0.10.0",
			},
		},
		{
			name: "missing tool",
			host: healthyHost("0.5.1").without("git"),
			status: map[string]string{
				nameFuseDevice:     statusPass,
				nameFuseOverlayfs:  statusPass,
				nameFusermount3:    statusPass,
				nameGit:            statusFail,
				nameImageToolchain: statusPass,
				nameSquashfuseSub:  statusPass,
			},
			details: map[string]string{
				nameGit: "git",
			},
		},
		{
			name: "squashfuse below baseline",
			host: healthyHost("0.4.10"),
			status: map[string]string{
				nameFuseDevice:     statusPass,
				nameFuseOverlayfs:  statusPass,
				nameFusermount3:    statusPass,
				nameGit:            statusPass,
				nameImageToolchain: statusPass,
				nameSquashfuseSub:  statusFail,
			},
			details: map[string]string{
				nameSquashfuseSub: "0.4.10 is below 0.5.0",
			},
		},
		{
			name: "unparsable banner",
			host: healthyHost("0.5.0").
				withOutput("git", "--version", "git version unknown\n", nil).
				withOutput("git", "-V", "still unknown\n", nil).
				withOutput("git", "-version", "still unknown\n", nil),
			status: map[string]string{
				nameGit: statusFail,
			},
			details: map[string]string{
				nameGit:           "unparsable version",
				nameSquashfuseSub: "0.5.0",
			},
		},
		{
			name: "github wrapper line is not the git banner",
			host: healthyHost("0.5.0").withOutput("git", "--version", ""+
				"https://github.com/git/git 1.0.0\n"+
				"git version 2.43.0\n", nil),
			details: map[string]string{
				nameGit: "2.43.0",
			},
		},
		{
			name: "unnamed version does not replace a versionless tool line",
			host: healthyHost("0.5.0").
				withOutput("git", "--version", "wrapper version 1.2.3\ngit version unknown\n", nil).
				withOutput("git", "-V", "wrapper version 1.2.3\ngit version unknown\n", nil).
				withOutput("git", "-version", "wrapper version 1.2.3\ngit version unknown\n", nil),
			status: map[string]string{
				nameGit: statusFail,
			},
			details: map[string]string{
				nameGit: "unparsable version",
			},
		},
		{
			name: "fuse device missing",
			host: healthyHost("0.5.0").withStat(fs.ErrNotExist),
			status: map[string]string{
				nameFuseDevice: statusFail,
			},
			details: map[string]string{
				nameFuseDevice: fs.ErrNotExist.Error(),
			},
		},
		{
			name: "fuse device permission denied",
			host: healthyHost("0.5.0").withStat(os.ErrPermission),
			status: map[string]string{
				nameFuseDevice: statusFail,
			},
			details: map[string]string{
				nameFuseDevice: os.ErrPermission.Error(),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := Probe(context.Background(), tt.host.seams(), Options{})
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			assertResults(t, results, tt.status, tt.details)
		})
	}
}

func TestFuseDeviceFailureIncludesRecoveryAdvice(t *testing.T) {
	for _, cause := range []error{fs.ErrNotExist, os.ErrPermission} {
		t.Run(cause.Error(), func(t *testing.T) {
			result, err := probeDevice(context.Background(), healthyHost("0.5.0").withStat(cause).seams())
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != statusFail {
				t.Fatalf("status = %q, want fail", result.Status)
			}
			for _, want := range []string{
				cause.Error(), "Hint:", "host supports FUSE", "/dev/fuse exists",
				"accessible to the current user", "--device /dev/fuse --cap-add SYS_ADMIN",
				"seccomp/AppArmor/SELinux",
			} {
				if !strings.Contains(result.Detail, want) {
					t.Errorf("detail = %q, want %q", result.Detail, want)
				}
			}
		})
	}
}

func TestImageToolchainThreeStates(t *testing.T) {
	tests := []struct {
		name    string
		missing []string
		status  string
		want    []string
		failed  bool
	}{
		{
			name:    "squashfs only",
			missing: []string{"mkfs.erofs", "erofsfuse"},
			status:  statusWarn,
			want:    []string{"squashfs", "erofs missing"},
		},
		{
			name:    "erofs only",
			missing: []string{"mksquashfs", "squashfuse_ll"},
			status:  statusWarn,
			want:    []string{"erofs", "squashfs missing"},
		},
		{
			name:    "neither stack",
			missing: []string{"mksquashfs", "squashfuse_ll", "mkfs.erofs", "erofsfuse"},
			status:  statusFail,
			want:    []string{"no complete image stack", "squashfs missing", "erofs missing"},
			failed:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := healthyHost("0.5.0")
			for _, binary := range tt.missing {
				host = host.without(binary)
			}
			results, err := Probe(context.Background(), host.seams(), Options{})
			if err != nil {
				t.Fatal(err)
			}
			got := byName(t, results)[nameImageToolchain]
			if got.Status != tt.status {
				t.Fatalf("status = %s, want %s (%s)", got.Status, tt.status, got.Detail)
			}
			for _, want := range tt.want {
				if !strings.Contains(got.Detail, want) {
					t.Fatalf("detail = %q, want %q", got.Detail, want)
				}
			}
			names := Failed(results)
			var saw bool
			for _, name := range names {
				if name == nameImageToolchain {
					saw = true
				}
			}
			if saw != tt.failed {
				t.Fatalf("Failed = %v, image_toolchain failed = %v, want %v", names, saw, tt.failed)
			}
		})
	}
}

func TestMissingSquashfuseWarnsSubdir(t *testing.T) {
	host := healthyHost("0.5.0").without("squashfuse_ll")
	results, err := Probe(context.Background(), host.seams(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := byName(t, results)
	sub := got[nameSquashfuseSub]
	if sub.Status != statusWarn {
		t.Fatalf("subdir status = %s, want warn (%s)", sub.Status, sub.Detail)
	}
	if !strings.Contains(sub.Detail, "not installed") {
		t.Fatalf("subdir detail = %q", sub.Detail)
	}
	if got[nameImageToolchain].Status != statusWarn {
		t.Fatalf("toolchain = %s (%s), want warn", got[nameImageToolchain].Status, got[nameImageToolchain].Detail)
	}
}

func TestVersionFallbackUsesVWhenVersionFails(t *testing.T) {
	// Synthetic scenario for the second fallback argument. No tool in
	// the real probe set fails --version yet answers -V today, so this
	// behavior is constructed here rather than captured from a host.
	host := healthyHost("0.5.0").
		withOutput("fusermount3", "--version", "", errors.New("exit status 1")).
		withOutput("fusermount3", "-V", "fusermount3 version: 3.14.1\n", nil)
	_, err := Probe(context.Background(), host.seams(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !host.called("/usr/bin/fusermount3 -V") {
		t.Fatalf("calls = %v, want fusermount3 -V", host.calls)
	}
	if host.called("/usr/bin/git -V") {
		t.Fatalf("git -V was called after a parseable --version: %v", host.calls)
	}
}

func TestProbeCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Probe(ctx, healthyHost("0.5.0").seams(), Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Probe err = %v, want context canceled", err)
	}
}

func TestFailedCollectsFailingProbeNamesInOrder(t *testing.T) {
	results := []Result{
		{Name: nameFuseDevice, Status: statusPass, Detail: fuseDevicePath},
		{Name: nameGit, Status: statusFail, Detail: "missing"},
		{Name: "file_limit_images", Status: statusWarn, Detail: "limit found"},
		{Name: nameImageToolchain, Status: statusFail, Detail: "no complete image stack"},
		{Name: "storage_perf_cache", Status: statusInfo, Detail: "measured"},
	}
	got := Failed(results)
	want := []string{nameGit, nameImageToolchain}
	if len(got) != len(want) {
		t.Fatalf("Failed = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Failed = %v, want %v", got, want)
		}
	}
}

func TestFailedWithoutFailuresIsEmpty(t *testing.T) {
	results := []Result{
		{Name: nameFuseDevice, Status: statusPass, Detail: fuseDevicePath},
		{Name: "file_limit_images", Status: statusWarn, Detail: "limit found"},
		{Name: "storage_perf_cache", Status: statusInfo, Detail: "measured"},
	}
	if got := Failed(results); len(got) != 0 {
		t.Fatalf("Failed = %v, want empty", got)
	}
}

func TestWriteRendersWarnAndInfoStatuses(t *testing.T) {
	var buf bytes.Buffer
	results := []Result{
		{Name: "file_limit_images", Status: statusWarn, Detail: "limit found"},
		{Name: "storage_perf_cache", Status: statusInfo, Detail: "seq 100 MiB/s"},
	}
	if err := Write(&buf, results, 120); err != nil {
		t.Fatal(err)
	}
	want := "🔧 Checks\n" +
		"\n" +
		"  file_limit_images   warn  limit found\n" +
		"  storage_perf_cache  info  seq 100 MiB/s\n"
	if buf.String() != want {
		t.Fatalf("Write = %q, want %q", buf.String(), want)
	}
}

func TestWriteWrapsLongDetailUnderTheValueColumn(t *testing.T) {
	var buf bytes.Buffer
	detail := "optional tool; sources ship in the contrib/ directory " +
		"of the tiler source tree with origin, license, and build notes"
	if err := Write(&buf, []Result{{Name: "contrib_concatfs", Status: statusWarn, Detail: detail}}, 120); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	if strings.Contains(text, "\t") {
		t.Fatalf("rendered output contains a tab:\n%s", text)
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("expected a wrapped detail, got %q", text)
	}
	first := lines[2]
	if !strings.Contains(first, "contrib_concatfs") || !strings.Contains(first, "warn") {
		t.Fatalf("first row = %q", first)
	}
	detailAt := strings.Index(first, "optional tool")
	if detailAt < 0 {
		t.Fatalf("detail missing from first row %q", first)
	}
	for _, line := range lines[3:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, strings.Repeat(" ", detailAt)) {
			t.Fatalf("continuation %q is not aligned at column %d", line, detailAt)
		}
	}
}

func TestDefaultSeamsListsEveryProbe(t *testing.T) {
	seams, err := DefaultSeams()
	if err != nil {
		t.Skip(err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results, err := Probe(ctx, seams, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Write(&buf, results, 120); err != nil {
		t.Fatal(err)
	}
	assertProbeNames(t, results, buf.String(), seams.listedProbeCount())
}

func assertResults(t *testing.T, results []Result, status, details map[string]string) {
	t.Helper()
	got := byName(t, results)
	for name, want := range status {
		if got[name].Status != want {
			t.Fatalf("%s status = %s, want %s (%s)", name, got[name].Status, want, got[name].Detail)
		}
	}
	for name, want := range details {
		if got[name].Detail != want && !strings.Contains(got[name].Detail, want) {
			t.Fatalf("%s detail = %q, want %q", name, got[name].Detail, want)
		}
	}
}

func assertProbeNames(t *testing.T, results []Result, rendered string, want int) {
	t.Helper()
	if len(results) != want {
		t.Fatalf("probe count = %d, want %d", len(results), want)
	}
	for _, result := range results {
		switch result.Status {
		case statusPass, statusFail, statusWarn, statusInfo:
		default:
			t.Fatalf("%s status = %s", result.Name, result.Status)
		}
		found := false
		for _, line := range strings.Split(rendered, "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == result.Name && fields[1] == result.Status {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("rendered output missing %s %s\n%s", result.Name, result.Status, rendered)
		}
	}
}

func indexOf(t *testing.T, results []Result, name string) int {
	t.Helper()
	for i, result := range results {
		if result.Name == name {
			return i
		}
	}
	t.Fatalf("missing probe %s", name)
	return -1
}

func byName(t *testing.T, results []Result) map[string]Result {
	t.Helper()
	got := make(map[string]Result, len(results))
	for _, result := range results {
		if _, ok := got[result.Name]; ok {
			t.Fatalf("duplicate probe %s", result.Name)
		}
		got[result.Name] = result
	}
	for _, name := range []string{
		nameFuseDevice, nameFuseOverlayfs, nameFusermount3,
		nameGit, nameImageToolchain, nameSquashfuseSub,
		nameFileLimitImages, nameFileLimitCache,
		namePerfImages, namePerfCache, nameCacheAdvice,
		nameContribConcatfs, nameContribSlowfs,
	} {
		if _, ok := got[name]; !ok {
			t.Fatalf("missing probe %s", name)
		}
	}
	return got
}

func allPass() map[string]string {
	return map[string]string{
		nameFuseDevice:     statusPass,
		nameFuseOverlayfs:  statusPass,
		nameFusermount3:    statusPass,
		nameGit:            statusPass,
		nameImageToolchain: statusPass,
		nameSquashfuseSub:  statusPass,
	}
}

type runResult struct {
	out []byte
	err error
}

type fakeHost struct {
	mu            sync.Mutex
	missing       map[string]error
	statErr       error
	truncate      func(dir string, size int64) error
	truncateCalls []string
	measure       func(ctx context.Context, dir string) (PerfNumbers, error)
	runs          map[string]runResult
	calls         []string
}

// healthyHost models a real, healthy host. Every banner is captured
// from the actual tool, keeping its exit code and name token, so the
// parser is exercised against reality instead of an idealized
// "tool version x.y.z" shape. The quirks that matter: squashfuse_ll
// names itself "squashfuse" and exits 2; fuse-overlayfs prints a
// two-component version behind a colon; fusermount3 answers --version
// directly; mksquashfs rejects --version and -V and answers only
// -version.
func healthyHost(squash string) *fakeHost {
	host := &fakeHost{runs: map[string]runResult{}}
	host.output("squashfuse_ll", "--version",
		"squashfuse "+squash+" (c) 2012 Dave Vasilevsky\n\n"+
			"Usage: squashfuse_ll [options] ARCHIVE MOUNTPOINT\n",
		errors.New("exit status 2"))
	host.output("fuse-overlayfs", "--version", ""+
		"fuse-overlayfs: version 1.18\n"+
		"FUSE library version 3.14.0\n"+
		"using FUSE kernel interface version 7.31\n"+
		"fusermount3 version: 3.14.0\n", nil)
	host.output("fusermount3", "--version", "fusermount3 version: 3.14.0\n", nil)
	host.output("mksquashfs", "--version",
		"SYNTAX:mksquashfs source1 source2 ...  FILESYSTEM [OPTIONS]\n",
		errors.New("exit status 1"))
	host.output("mksquashfs", "-V",
		"SYNTAX:mksquashfs source1 source2 ...  FILESYSTEM [OPTIONS]\n",
		errors.New("exit status 1"))
	host.output("mksquashfs", "-version",
		"mksquashfs version 4.6.1 (2023/03/25)\n"+
			"copyright (C) 2023 Phillip Lougher <phillip@squashfs.org.uk>\n", nil)
	host.output("git", "--version", "git version 2.43.0\n", nil)
	return host
}

func (f *fakeHost) without(binary string) *fakeHost {
	if f.missing == nil {
		f.missing = map[string]error{}
	}
	f.missing[binary] = exec.ErrNotFound
	return f
}

func (f *fakeHost) withStat(err error) *fakeHost {
	f.statErr = err
	return f
}

func (f *fakeHost) withOutput(binary, arg, out string, err error) *fakeHost {
	f.output(binary, arg, out, err)
	return f
}

func (f *fakeHost) output(binary, arg, out string, err error) {
	f.runs["/usr/bin/"+binary+" "+arg] = runResult{out: []byte(out), err: err}
}

func (f *fakeHost) seams() Seams {
	truncate := f.truncate
	if truncate == nil {
		truncate = func(string, int64) error { return fs.ErrNotExist }
	}
	measure := f.measure
	if measure == nil {
		measure = func(context.Context, string) (PerfNumbers, error) {
			return PerfNumbers{}, fs.ErrNotExist
		}
	}
	return Seams{
		LookPath: f.lookPath,
		Stat:     f.stat,
		Run:      f.run,
		TruncateProbe: func(dir string, size int64) error {
			f.mu.Lock()
			f.truncateCalls = append(f.truncateCalls, dir)
			f.mu.Unlock()
			return truncate(dir, size)
		},
		Measure: measure,
	}
}

func (f *fakeHost) truncateCalled(dir string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.truncateCalls {
		if call == dir {
			return true
		}
	}
	return false
}

func (f *fakeHost) lookPath(name string) (string, error) {
	if err, ok := f.missing[name]; ok {
		return "", err
	}
	return "/usr/bin/" + name, nil
}

func (f *fakeHost) stat(string) error { return f.statErr }

func (f *fakeHost) run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, key)
	res, ok := f.runs[key]
	f.mu.Unlock()
	if !ok {
		return nil, errors.New("unexpected command " + key)
	}
	return res.out, res.err
}

func (f *fakeHost) called(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call == key {
			return true
		}
	}
	return false
}
