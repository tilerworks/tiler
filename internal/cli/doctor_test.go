package cli

import (
	"context"
	"encoding/json"
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
	for _, name := range []string{
		"fuse_device",
		"squashfuse_ll",
		"fuse_overlayfs",
		"fusermount3",
		"mksquashfs",
		"git",
		"squashfuse_subdir",
	} {
		if !strings.Contains(stdout, name+"\t") {
			t.Fatalf("stdout missing %s\n%s", name, stdout)
		}
	}
}

func TestDoctorFailingProbeExitsOne(t *testing.T) {
	withWorkspace(t)
	restoreDoctor(t, func(context.Context, config.Config) ([]doctor.Result, error) {
		return []doctor.Result{{Name: "git", Status: "fail", Detail: "missing"}}, nil
	})
	stdout, stderr, err := runDoctorCmd(context.Background(), t)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1\n%s", err, ExitCode(err), stderr)
	}
	if stdout != "git\tfail\tmissing\n" {
		t.Fatalf("stdout = %q, want the report rendered before the verdict", stdout)
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
	restoreDoctor(t, func(context.Context, config.Config) ([]doctor.Result, error) {
		return []doctor.Result{
			{Name: "file_limit_images", Status: "warn", Detail: "limit found"},
			{Name: "storage_perf_cache", Status: "info", Detail: "measured"},
		}, nil
	})
	stdout, stderr, err := runDoctorCmd(context.Background(), t)
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, stderr)
	}
	want := "file_limit_images\twarn\tlimit found\n" +
		"storage_perf_cache\tinfo\tmeasured\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func TestDoctorCancelledContextExitsOne(t *testing.T) {
	withWorkspace(t)
	restoreDoctor(t, func(context.Context, config.Config) ([]doctor.Result, error) {
		return nil, context.Canceled
	})
	_, stderr, err := runDoctorCmd(context.Background(), t)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1\n%s", err, ExitCode(err), stderr)
	}
	if strings.Contains(stderr, "Usage:") {
		t.Fatalf("cancelled doctor printed usage\n%s", stderr)
	}
}

func TestDoctorJSONMatchesGolden(t *testing.T) {
	withWorkspace(t)
	fixture := goldenDoctorResults()
	restoreDoctor(t, func(context.Context, config.Config) ([]doctor.Result, error) {
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
	restoreDoctor(t, func(context.Context, config.Config) ([]doctor.Result, error) {
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

func restoreDoctor(t *testing.T, fn func(context.Context, config.Config) ([]doctor.Result, error)) {
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
