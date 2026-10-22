package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/doctor"
)

func TestDoctorPrintsEveryProbeName(t *testing.T) {
	if _, err := doctor.DefaultSeams(); err != nil {
		t.Skip(err.Error())
	}
	withWorkspace(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stdout, stderr, err := runDoctorCmd(ctx, t)
	// Real host probes may fail; under the verdict semantics that is
	// a runtime error (exit 1) and the report is rendered anyway.
	if err != nil {
		if ExitCode(err) != 1 || !strings.Contains(stderr, "failing probes:") {
			t.Fatalf("doctor: %v\n%s", err, stderr)
		}
	}
	for _, name := range expectedDoctorProbes() {
		if !strings.Contains(stdout, name) {
			t.Fatalf("stdout missing %s\n%s", name, stdout)
		}
	}
}

func TestDoctorFailingProbeExitsOne(t *testing.T) {
	withWorkspace(t)
	restoreDoctor(t, func(context.Context, config.Config, io.Writer) ([]doctor.Result, error) {
		return []doctor.Result{{Name: "git", Status: "fail", Detail: "missing"}}, nil
	})
	stdout, stderr, err := runDoctorCmd(context.Background(), t)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1\n%s", err, ExitCode(err), stderr)
	}
	if !strings.HasSuffix(stdout, "  git  fail  missing\n") {
		t.Fatalf("stdout = %q, want the report rendered before the verdict", stdout)
	}
	if !strings.Contains(stdout, "📋 Config") || strings.Contains(stdout, "\t") {
		t.Fatalf("stdout missing the config section or still tab-aligned\n%s", stdout)
	}
	if !strings.Contains(stderr, "failing probes: git") {
		t.Fatalf("stderr does not list the failed probe name\n%s", stderr)
	}
	if strings.Contains(stderr, "Usage:") {
		t.Fatalf("failing doctor printed usage\n%s", stderr)
	}
}

func TestDoctorWarnAndInfoProbesExitZero(t *testing.T) {
	withWorkspace(t)
	restoreDoctor(t, func(context.Context, config.Config, io.Writer) ([]doctor.Result, error) {
		return []doctor.Result{
			{Name: "file_limit_images", Status: "warn", Detail: "limit found"},
			{Name: "storage_perf_cache", Status: "info", Detail: "measured"},
		}, nil
	})
	stdout, stderr, err := runDoctorCmd(context.Background(), t)
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, stderr)
	}
	want := "  file_limit_images   warn  limit found\n" +
		"  storage_perf_cache  info  measured\n"
	if !strings.HasSuffix(stdout, want) {
		t.Fatalf("stdout = %q, want it to end with %q", stdout, want)
	}
	if !strings.Contains(stdout, "📋 Config") {
		t.Fatalf("stdout missing the config section\n%s", stdout)
	}
}

func TestDoctorCancelledContextExitsOne(t *testing.T) {
	home, _ := withWorkspace(t)
	restoreDoctor(t, func(context.Context, config.Config, io.Writer) ([]doctor.Result, error) {
		return nil, context.Canceled
	})
	stdout, stderr, err := runDoctorCmd(context.Background(), t)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1\n%s", err, ExitCode(err), stderr)
	}
	if strings.Contains(stderr, "Usage:") {
		t.Fatalf("cancelled doctor printed usage\n%s", stderr)
	}
	// The config section is rendered before the probe-set error, so a
	// host that cannot start probes still reports where tiler will
	// read and write.
	configPath := filepath.Join(home, ".tiler.yaml")
	flat := unwrapReport(stdout)
	if !strings.Contains(stdout, "📋 Config") || !strings.Contains(stdout, "🔧 Checks") ||
		!strings.Contains(flat, configPath) {
		t.Fatalf("stdout missing the config section\n%s", stdout)
	}
	stdout, stderr, err = runDoctorCmd(context.Background(), t, "--json")
	if ExitCode(err) != 1 {
		t.Fatalf("json ExitCode(%v) = %d, want 1\n%s", err, ExitCode(err), stderr)
	}
	if !strings.Contains(stdout, configPath) {
		t.Fatalf("json stdout missing the config path\n%s", stdout)
	}
}

func TestDoctorJSONMatchesGolden(t *testing.T) {
	// A fixed, non-writable home keeps the golden byte-stable: the config
	// section embeds the home path. EnsureUserFile cannot create the file
	// there, which is fine because it is best-effort and Load falls back
	// to the built-in defaults resolved under the same home.
	t.Setenv("HOME", goldenHome)
	t.Chdir(t.TempDir())
	fixture := goldenDoctorResults()
	restoreDoctor(t, func(context.Context, config.Config, io.Writer) ([]doctor.Result, error) {
		return fixture, nil
	})
	stdout, stderr, err := runDoctorCmd(context.Background(), t, "--json")
	if err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, stderr)
	}
	assertGolden(t, "doctor_json", []byte(stdout))
	var doc doctorDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("doctor --json is not parseable: %v\n%s", err, stdout)
	}
	if len(doc.Results) != len(fixture) {
		t.Fatalf("results = %d, want %d", len(doc.Results), len(fixture))
	}
	for i, result := range doc.Results {
		if result.Name != fixture[i].Name || result.Status != fixture[i].Status {
			t.Fatalf("result %d = %s/%s, want %s/%s", i,
				result.Name, result.Status, fixture[i].Name, fixture[i].Status)
		}
	}
}

func TestDoctorJSONFailingProbeExitsOne(t *testing.T) {
	withWorkspace(t)
	restoreDoctor(t, func(context.Context, config.Config, io.Writer) ([]doctor.Result, error) {
		return []doctor.Result{{Name: "git", Status: "fail", Detail: "missing"}}, nil
	})
	stdout, stderr, err := runDoctorCmd(context.Background(), t, "--json")
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1\n%s", err, ExitCode(err), stderr)
	}
	var doc doctorDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("doctor --json is not parseable: %v\n%s", err, stdout)
	}
	if len(doc.Results) != 1 || doc.Results[0].Status != "fail" {
		t.Fatalf("results = %+v, want the single fail rendered before the verdict",
			doc.Results)
	}
}

func TestDoctorContribWarningsKeepExitZero(t *testing.T) {
	withWorkspace(t)
	guidance := "optional tool; C sources ship in the contrib/ directory of " +
		"the tiler source tree; tiler never installs it automatically"
	restoreDoctor(t, func(context.Context, config.Config, io.Writer) ([]doctor.Result, error) {
		return []doctor.Result{
			{Name: "contrib_concatfs", Status: "warn", Detail: guidance},
			{Name: "contrib_slowfs", Status: "warn", Detail: guidance},
		}, nil
	})
	stdout, stderr, err := runDoctorCmd(context.Background(), t)
	if err != nil {
		t.Fatalf("doctor with contrib warnings: %v\n%s", err, stderr)
	}
	for _, name := range []string{"contrib_concatfs", "contrib_slowfs"} {
		if !strings.Contains(stdout, name) || !strings.Contains(stdout, "warn") {
			t.Fatalf("stdout missing the %s warn line\n%s", name, stdout)
		}
	}
	if !strings.Contains(stdout, "contrib/ directory") ||
		!strings.Contains(stdout, "never installs it automatically") {
		t.Fatalf("stdout missing the guidance wording\n%s", stdout)
	}

	stdout, stderr, err = runDoctorCmd(context.Background(), t, "--json")
	if err != nil {
		t.Fatalf("doctor --json with contrib warnings: %v\n%s", err, stderr)
	}
	var doc doctorDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("doctor --json is not parseable: %v\n%s", err, stdout)
	}
	if len(doc.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(doc.Results))
	}
	for _, result := range doc.Results {
		if result.Status != "warn" || result.Detail != guidance {
			t.Fatalf("result = %+v, want warn carrying the guidance", result)
		}
	}
}

// TestDoctorReportsConfigSection pins the human-readable config
// section: the user config file location and the core runtime
// directories resolved from the injected home.
func TestDoctorReportsConfigSection(t *testing.T) {
	home, _ := withWorkspace(t)
	restoreDoctor(t, func(context.Context, config.Config, io.Writer) ([]doctor.Result, error) {
		return []doctor.Result{{Name: "git", Status: "pass", Detail: "2.43.0"}}, nil
	})
	stdout, _, err := runDoctorCmd(context.Background(), t)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	// macOS temp dirs exceed the report width, so a path continues on
	// the next line. Join those continuations before matching.
	flat := unwrapReport(stdout)
	for _, want := range []string{
		"path          " + filepath.Join(home, ".tiler.yaml"),
		"images_dir    " + filepath.Join(home, ".tiler", "images"),
		"sessions_dir  " + filepath.Join(home, ".tiler", "sessions"),
		"cache_dir     " + filepath.Join(home, ".tiler", "cache"),
		"log_level     info",
	} {
		if !strings.Contains(flat, want) {
			t.Fatalf("stdout missing %q\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "runtime_dir") {
		t.Fatalf("doctor config section still reports runtime_dir\n%s", stdout)
	}
	if strings.Contains(stdout, "\t") {
		t.Fatalf("doctor human output is tab-aligned\n%s", stdout)
	}
	// The config heading precedes the probe heading.
	if strings.Index(stdout, "📋 Config") > strings.Index(stdout, "🔧 Checks") {
		t.Fatalf("config section is not rendered before the probes\n%s", stdout)
	}
}

// TestDoctorJSONIncludesConfig asserts the --json envelope carries the
// resolved config summary alongside the probe results.
func TestDoctorJSONIncludesConfig(t *testing.T) {
	home, _ := withWorkspace(t)
	restoreDoctor(t, func(context.Context, config.Config, io.Writer) ([]doctor.Result, error) {
		return []doctor.Result{{Name: "git", Status: "pass", Detail: "2.43.0"}}, nil
	})
	stdout, _, err := runDoctorCmd(context.Background(), t, "--json")
	if err != nil {
		t.Fatalf("doctor --json: %v", err)
	}
	var doc doctorDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("doctor --json is not parseable: %v\n%s", err, stdout)
	}
	if doc.Config == nil {
		t.Fatalf("doctor --json has no config object\n%s", stdout)
	}
	if doc.Config.Path != filepath.Join(home, ".tiler.yaml") {
		t.Fatalf("config.path = %q", doc.Config.Path)
	}
	if doc.Config.ImagesDir != filepath.Join(home, ".tiler", "images") {
		t.Fatalf("config.images_dir = %q", doc.Config.ImagesDir)
	}
	if doc.Config.LogLevel != "info" {
		t.Fatalf("config.log_level = %q", doc.Config.LogLevel)
	}
}

// goldenHome is the fixed HOME the doctor --json golden renders under.
// It is a literal, never a real or writable directory, so the config
// paths in the golden stay identical across runs and machines.
const goldenHome = "/tiler-doctor-golden-home"

// goldenDoctorResults is the fixed --json input: every status, the
// structured measurement fields, and detail text with & and < so the
// golden file pins the unescaped rendering.
func goldenDoctorResults() []doctor.Result {
	limit := int64(4 * 1024 * 1024 * 1024)
	seq := 123.5
	avg := 4.25
	worst := 9.75
	return []doctor.Result{
		{Name: "fuse_device", Status: "pass", Detail: "/dev/fuse"},
		{Name: "git", Status: "pass", Detail: "2.43.0", ToolPath: "/usr/bin/git"},
		{
			Name:       "file_limit_images",
			Status:     "warn",
			Detail:     "single-file size limit <4 GiB> & split storage disabled",
			LimitBytes: &limit,
		},
		{
			Name:          "storage_perf_cache",
			Status:        "info",
			Detail:        "seq 123.5 MiB/s rand avg 4.25 ms max 9.75 ms",
			SeqReadMiBps:  &seq,
			RandReadAvgMs: &avg,
			RandReadMaxMs: &worst,
		},
	}
}

func TestDoctorOptionsMapsSplitSize(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	cfg.SplitSize = 2 << 30
	opts := doctorOptions(cfg)
	if opts.ImagesDir != cfg.ImagesDir || opts.CacheDir != cfg.CacheDir {
		t.Fatalf("dirs = %+v", opts)
	}
	if opts.SplitSize != 2<<30 {
		t.Fatalf("SplitSize = %d, want 2G", opts.SplitSize)
	}
}

// unwrapReport joins a detail that Write wrapped under the value
// column. A continuation is indented past the two-space row indent;
// a new row starts at that indent. Short lines stay unchanged.
func unwrapReport(stdout string) string {
	var b strings.Builder
	first := true
	for _, line := range strings.Split(stdout, "\n") {
		if wrappedContinuation(line) {
			b.WriteString(strings.TrimLeft(line, " "))
			continue
		}
		if !first {
			b.WriteByte('\n')
		}
		first = false
		b.WriteString(line)
	}
	return b.String()
}

func wrappedContinuation(line string) bool {
	return len(line) >= 3 &&
		line[0] == ' ' && line[1] == ' ' && line[2] == ' ' &&
		strings.TrimLeft(line, " ") != ""
}

func TestUnwrapReportJoinsValueContinuations(t *testing.T) {
	const head = "  path          "
	detail := "/var/folders/gk/TestDoctorReportsConfigSection565068672/001/.tiler.yaml"
	wrapped := head + detail[:20] + "\n" + strings.Repeat(" ", len(head)) + detail[20:] + "\n" +
		"  log_level     info\n"
	flat := unwrapReport(wrapped)
	if !strings.Contains(flat, head+detail) {
		t.Fatalf("flat = %q, want the path on one line", flat)
	}
	if !strings.Contains(flat, "log_level     info") {
		t.Fatalf("flat = %q, want the next row preserved", flat)
	}
}

// expectedDoctorProbes is the real doctor report for this host.
// macOS checks hdiutil and omits the Linux FUSE failure probes.
func expectedDoctorProbes() []string {
	if runtime.GOOS == "darwin" {
		return []string{
			"git",
			"image_toolchain",
			"file_limit_images",
			"file_limit_cache",
			"storage_perf_images",
			"storage_perf_cache",
			"cache_advice",
			"contrib_concatfs",
			"contrib_slowfs",
		}
	}
	return []string{
		"fuse_device",
		"fuse_overlayfs",
		"fusermount3",
		"git",
		"image_toolchain",
		"squashfuse_subdir",
		"file_limit_images",
		"file_limit_cache",
		"storage_perf_images",
		"storage_perf_cache",
		"cache_advice",
		"contrib_concatfs",
		"contrib_slowfs",
	}
}

func TestDoctorProgressStaysOffTheReport(t *testing.T) {
	withWorkspace(t)
	restoreDoctor(t, func(_ context.Context, _ config.Config, progress io.Writer) ([]doctor.Result, error) {
		_, _ = fmt.Fprint(progress, "probing\n\n  git ...\n  git ... done\n")
		return []doctor.Result{{Name: "git", Status: "pass", Detail: "2.43.0"}}, nil
	})
	stdout, stderr, err := runDoctorCmd(context.Background(), t)
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, stderr)
	}
	if strings.Contains(stdout, "probing") {
		t.Fatalf("stdout contains probe progress\n%s", stdout)
	}
	if !strings.Contains(stdout, "📋 Config") || !strings.Contains(stdout, "git  pass  2.43.0") {
		t.Fatalf("stdout missing the finished report\n%s", stdout)
	}
	if !strings.Contains(stderr, "probing\n\n  git ...\n") || !strings.Contains(stderr, "  git ... done\n") {
		t.Fatalf("stderr = %q, want the probe progress", stderr)
	}
}

func restoreDoctor(t *testing.T, fn func(context.Context, config.Config, io.Writer) ([]doctor.Result, error)) {
	t.Helper()
	previous := runDoctor
	runDoctor = fn
	t.Cleanup(func() { runDoctor = previous })
}

func runDoctorCmd(ctx context.Context, t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := NewRootCmd()
	cmd.SetContext(ctx)
	var out, errBuf strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(append([]string{"doctor"}, args...))
	err = cmd.Execute()
	return out.String(), errBuf.String(), err
}
