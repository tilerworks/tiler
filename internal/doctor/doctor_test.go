package doctor

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"strings"
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
				nameSquashfuseLL:  "0.5.0",
				nameSquashfuseSub: "0.5.0",
				nameFuseOverlayfs: "1.13.0",
				nameGit:           "2.43.0",
			},
		},
		{
			name:   "healthy environment above baseline",
			host:   healthyHost("0.10.0"),
			status: allPass(),
			details: map[string]string{
				nameSquashfuseLL:  "0.10.0",
				nameSquashfuseSub: "0.10.0",
			},
		},
		{
			name: "missing tool",
			host: healthyHost("0.5.1").without("git"),
			status: map[string]string{
				nameFuseDevice:    statusPass,
				nameSquashfuseLL:  statusPass,
				nameFuseOverlayfs: statusPass,
				nameFusermount3:   statusPass,
				nameMksquashfs:    statusPass,
				nameGit:           statusFail,
				nameSquashfuseSub: statusPass,
			},
			details: map[string]string{
				nameGit: "git",
			},
		},
		{
			name: "squashfuse below baseline",
			host: healthyHost("0.4.10"),
			status: map[string]string{
				nameFuseDevice:    statusPass,
				nameSquashfuseLL:  statusPass,
				nameFuseOverlayfs: statusPass,
				nameFusermount3:   statusPass,
				nameMksquashfs:    statusPass,
				nameGit:           statusPass,
				nameSquashfuseSub: statusFail,
			},
			details: map[string]string{
				nameSquashfuseLL:  "0.4.10",
				nameSquashfuseSub: "0.4.10 is below 0.5.0",
			},
		},
		{
			name: "unparsable banner",
			host: healthyHost("0.5.0").
				withOutput("git", "--version", "git version unknown\n", nil).
				withOutput("git", "-V", "still unknown\n", nil),
			status: map[string]string{
				nameGit: statusFail,
			},
			details: map[string]string{
				nameGit:           "unparsable version",
				nameSquashfuseSub: "0.5.0",
			},
		},
		{
			name: "banner surrounded by unrelated lines",
			host: healthyHost("0.5.0").withOutput("mksquashfs", "--version", ""+
				"wrapper version 1.2.3\n"+
				"loading plugins\n"+
				"mksquashfs version 4.6.1 (2023/01/01)\n"+
				"done version 9.8.7\n", nil),
			details: map[string]string{
				nameMksquashfs: "4.6.1",
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
				withOutput("git", "-V", "wrapper version 1.2.3\ngit version unknown\n", nil),
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

func TestMissingSquashfuseFailsSubdirWithReason(t *testing.T) {
	host := healthyHost("0.5.0").without("squashfuse_ll")
	results, err := Probe(context.Background(), host.seams(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := byName(t, results)
	if got[nameSquashfuseLL].Status != statusFail {
		t.Fatalf("squashfuse_ll status = %s", got[nameSquashfuseLL].Status)
	}
	sub := got[nameSquashfuseSub]
	if sub.Status != statusFail {
		t.Fatalf("subdir status = %s", sub.Status)
	}
	if !strings.Contains(sub.Detail, got[nameSquashfuseLL].Detail) {
		t.Fatalf("subdir detail %q does not include %q", sub.Detail, got[nameSquashfuseLL].Detail)
	}
}

func TestVersionFallbackUsesVWhenVersionFails(t *testing.T) {
	host := healthyHost("0.5.0")
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
		{Name: nameSquashfuseLL, Status: statusFail, Detail: "unparsable version"},
		{Name: "storage_perf_cache", Status: statusInfo, Detail: "measured"},
	}
	got := Failed(results)
	want := []string{nameGit, nameSquashfuseLL}
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
	if err := Write(&buf, results); err != nil {
		t.Fatal(err)
	}
	want := "file_limit_images\twarn\tlimit found\n" +
		"storage_perf_cache\tinfo\tseq 100 MiB/s\n"
	if buf.String() != want {
		t.Fatalf("Write = %q, want %q", buf.String(), want)
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
	if err := Write(&buf, results); err != nil {
		t.Fatal(err)
	}
	assertProbeNames(t, results, buf.String())
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

func assertProbeNames(t *testing.T, results []Result, rendered string) {
	t.Helper()
	if len(results) != len(toolSpecs)+4 {
		t.Fatalf("probe count = %d, want %d", len(results), len(toolSpecs)+4)
	}
	for _, result := range results {
		switch result.Status {
		case statusPass, statusFail, statusWarn, statusInfo:
		default:
			t.Fatalf("%s status = %s", result.Name, result.Status)
		}
		line := result.Name + "\t" + result.Status + "\t"
		if !strings.Contains(rendered, line) {
			t.Fatalf("rendered output missing %q\n%s", line, rendered)
		}
	}
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
		nameFuseDevice, nameSquashfuseLL, nameFuseOverlayfs, nameFusermount3,
		nameMksquashfs, nameGit, nameSquashfuseSub,
		nameFileLimitImages, nameFileLimitCache,
	} {
		if _, ok := got[name]; !ok {
			t.Fatalf("missing probe %s", name)
		}
	}
	return got
}

func allPass() map[string]string {
	return map[string]string{
		nameFuseDevice:    statusPass,
		nameSquashfuseLL:  statusPass,
		nameFuseOverlayfs: statusPass,
		nameFusermount3:   statusPass,
		nameMksquashfs:    statusPass,
		nameGit:           statusPass,
		nameSquashfuseSub: statusPass,
	}
}

type runResult struct {
	out []byte
	err error
}

type fakeHost struct {
	missing       map[string]error
	statErr       error
	truncate      func(dir string, size int64) error
	truncateCalls []string
	runs          map[string]runResult
	calls         []string
}

func healthyHost(squash string) *fakeHost {
	host := &fakeHost{runs: map[string]runResult{}}
	host.output("squashfuse_ll", "--version", "FUSE library version: 3.16.2\nsquashfuse_ll version "+squash+"\n", nil)
	host.output("fuse-overlayfs", "--version", "fuse-overlayfs version 1.13.0\n", nil)
	host.output("fusermount3", "--version", "", errors.New("exit status 1"))
	host.output("fusermount3", "-V", "fusermount3 version: 3.14.1\n", nil)
	host.output("mksquashfs", "--version", "mksquashfs version 4.6.1\n", nil)
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
	return Seams{
		LookPath: f.lookPath,
		Stat:     f.stat,
		Run:      f.run,
		TruncateProbe: func(dir string, size int64) error {
			f.truncateCalls = append(f.truncateCalls, dir)
			return truncate(dir, size)
		},
	}
}

func (f *fakeHost) truncateCalled(dir string) bool {
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
	f.calls = append(f.calls, key)
	res, ok := f.runs[key]
	if !ok {
		return nil, errors.New("unexpected command " + key)
	}
	return res.out, res.err
}

func (f *fakeHost) called(key string) bool {
	for _, call := range f.calls {
		if call == key {
			return true
		}
	}
	return false
}
