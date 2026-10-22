package doctor

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/jiangxin/tiler/internal/container"
)

func TestDarwinDoctorHdiutilPresent(t *testing.T) {
	host := newDarwinHost(true, nil)
	results, err := Probe(context.Background(), host.seams, Options{
		ImagesDir: "/images",
		CacheDir:  "/cache",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertDarwinProbeNames(t, results)
	got := mapResults(t, results)
	if got[nameImageToolchain].Status != statusPass || got[nameImageToolchain].Detail != "dmg" {
		t.Fatalf("toolchain = %s %q", got[nameImageToolchain].Status, got[nameImageToolchain].Detail)
	}
	if got[nameGit].Status != statusPass || got[nameGit].Detail != "2.43.0" {
		t.Fatalf("git = %s %q", got[nameGit].Status, got[nameGit].Detail)
	}
	if got[nameFileLimitImages].Status != statusPass || got[nameFileLimitCache].Status != statusPass {
		t.Fatalf("file limits = %s %s", got[nameFileLimitImages].Status, got[nameFileLimitCache].Status)
	}
	if !host.truncated("/images") || !host.truncated("/cache") {
		t.Fatalf("truncate calls = %v", host.trunc)
	}
	assertDarwinInfo(t, got)
	for _, run := range host.runs {
		if strings.Contains(run, "hdiutil") {
			t.Fatalf("doctor executed hdiutil: %v", host.runs)
		}
	}
	if failed := Failed(results); len(failed) != 0 {
		t.Fatalf("Failed = %v", failed)
	}
}

func TestDarwinDoctorHdiutilMissing(t *testing.T) {
	host := newDarwinHost(true, errors.New("missing"))
	results, err := Probe(context.Background(), host.seams, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := mapResults(t, results)
	tool := got[nameImageToolchain]
	if tool.Status != statusFail {
		t.Fatalf("status = %s, want fail (%s)", tool.Status, tool.Detail)
	}
	if !strings.Contains(tool.Detail, "not found on PATH") {
		t.Fatalf("detail = %q", tool.Detail)
	}
	for _, absent := range []string{"brew", "apt", "dnf"} {
		if strings.Contains(tool.Detail, absent) {
			t.Fatalf("detail suggests %s: %q", absent, tool.Detail)
		}
	}
	if failed := Failed(results); len(failed) != 1 || failed[0] != nameImageToolchain {
		t.Fatalf("Failed = %v", failed)
	}
	for _, name := range fuseProbeNames() {
		if _, ok := got[name]; ok {
			t.Fatalf("darwin doctor still reports %s", name)
		}
	}
}

func TestDarwinDoctorHFSPlusNote(t *testing.T) {
	restore := container.SetDMGCreateFilesystem(func() string { return "HFS+" })
	t.Cleanup(restore)
	host := newDarwinHost(true, nil)
	results, err := Probe(context.Background(), host.seams, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := mapResults(t, results)[nameImageToolchain]
	if got.Status != statusPass {
		t.Fatalf("status = %s, want pass (%s)", got.Status, got.Detail)
	}
	for _, want := range []string{"HFS+", "APFS", "shadow"} {
		if !strings.Contains(got.Detail, want) {
			t.Fatalf("detail = %q, want %s", got.Detail, want)
		}
	}
	if failed := Failed(results); len(failed) != 0 {
		t.Fatalf("Failed = %v", failed)
	}
}

func TestDarwinDoctorMissingGitSuggestsXcodeSelect(t *testing.T) {
	host := newDarwinHost(false, nil)
	results, err := Probe(context.Background(), host.seams, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := mapResults(t, results)[nameGit]
	if got.Status != statusFail {
		t.Fatalf("status = %s, want fail (%s)", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "xcode-select --install") {
		t.Fatalf("detail = %q", got.Detail)
	}
	if strings.Contains(got.Detail, "apt") || strings.Contains(got.Detail, "brew") {
		t.Fatalf("detail suggests a package manager: %q", got.Detail)
	}
}

func assertDarwinInfo(t *testing.T, got map[string]Result) {
	t.Helper()
	for _, name := range []string{
		namePerfImages, namePerfCache, nameCacheAdvice,
		nameContribConcatfs, nameContribSlowfs,
	} {
		if got[name].Status != statusInfo {
			t.Fatalf("%s = %s, want info (%s)", name, got[name].Status, got[name].Detail)
		}
	}
	concatfs := got[nameContribConcatfs]
	if concatfs.ToolPath != "/usr/bin/concatfs" {
		t.Fatalf("concatfs path = %q", concatfs.ToolPath)
	}
	if concatfs.Detail != darwinContribUnused+"; /usr/bin/concatfs" {
		t.Fatalf("concatfs detail = %q", concatfs.Detail)
	}
	slowfs := got[nameContribSlowfs]
	if slowfs.Detail != darwinContribUnused {
		t.Fatalf("slowfs detail = %q", slowfs.Detail)
	}
	for _, name := range []string{nameContribConcatfs, nameContribSlowfs} {
		detail := got[name].Detail
		for _, banned := range []string{"contrib/", "libfuse3", "gcc"} {
			if strings.Contains(detail, banned) {
				t.Fatalf("%s detail suggests a build: %q", name, detail)
			}
		}
	}
}

func assertDarwinProbeNames(t *testing.T, results []Result) {
	t.Helper()
	want := []string{
		nameGit, nameImageToolchain,
		nameFileLimitImages, nameFileLimitCache,
		namePerfImages, namePerfCache, nameCacheAdvice,
		nameContribConcatfs, nameContribSlowfs,
	}
	if len(results) != len(want) || len(want) != darwinProbeCount {
		t.Fatalf("len = %d, want %d", len(results), len(want))
	}
	for i, name := range want {
		if results[i].Name != name {
			t.Fatalf("probe %d = %s, want %s", i, results[i].Name, name)
		}
	}
}

func fuseProbeNames() []string {
	return []string{nameFuseDevice, nameFuseOverlayfs, nameFusermount3, nameSquashfuseSub}
}

func mapResults(t *testing.T, results []Result) map[string]Result {
	t.Helper()
	got := make(map[string]Result, len(results))
	for _, result := range results {
		if _, ok := got[result.Name]; ok {
			t.Fatalf("duplicate probe %s", result.Name)
		}
		got[result.Name] = result
	}
	return got
}

type darwinHost struct {
	mu    sync.Mutex
	seams Seams
	runs  []string
	trunc []string
}

func newDarwinHost(gitOK bool, hdiutil error) *darwinHost {
	host := &darwinHost{}
	host.seams = Seams{
		Profile: profileDarwin,
		Hdiutil: func() error { return hdiutil },
		LookPath: func(name string) (string, error) {
			switch name {
			case "git":
				if gitOK {
					return "/usr/bin/git", nil
				}
			case "concatfs":
				return "/usr/bin/concatfs", nil
			}
			return "", exec.ErrNotFound
		},
		Stat: func(string) error { return fs.ErrNotExist },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			host.mu.Lock()
			host.runs = append(host.runs, name+" "+strings.Join(args, " "))
			host.mu.Unlock()
			if strings.HasSuffix(name, "/git") {
				return []byte("git version 2.43.0\n"), nil
			}
			return nil, errors.New("unexpected command " + name)
		},
		TruncateProbe: func(dir string, _ int64) error {
			host.mu.Lock()
			host.trunc = append(host.trunc, dir)
			host.mu.Unlock()
			return nil
		},
		Measure: func(context.Context, string) (PerfNumbers, error) {
			return PerfNumbers{}, errors.New("warm cache")
		},
	}
	return host
}

func (h *darwinHost) truncated(dir string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, call := range h.trunc {
		if call == dir {
			return true
		}
	}
	return false
}
