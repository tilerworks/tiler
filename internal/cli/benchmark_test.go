package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/benchmark"
	"github.com/jiangxin/tiler/internal/config"
)

func TestBenchmarkMissingPathExits2(t *testing.T) {
	withWorkspace(t)
	_, _, _, err := runCmd(t, "benchmark")
	if ExitCode(err) != 2 {
		t.Fatalf("ExitCode(%v) = %d, want 2", err, ExitCode(err))
	}
}

func TestBenchmarkBadSizesExit2(t *testing.T) {
	_, work := withWorkspace(t)
	stubBenchmarkRun(t)
	for _, size := range []string{"1.5G", "2Mi"} {
		_, _, _, err := runCmd(t, "benchmark", work,
			"--type", "tar", "--size", size, "--file-size", "1", "--no-probe")
		if ExitCode(err) != 2 {
			t.Fatalf("size %q: ExitCode(%v) = %d, want 2", size, err, ExitCode(err))
		}
	}
}

func TestBenchmarkRejectsImagesDir(t *testing.T) {
	_, work := withWorkspace(t)
	stubBenchmarkRun(t)
	for _, args := range [][]string{
		{"--images-dir", "/tmp", "benchmark", work, "--type", "tar",
			"--size", "1", "--file-size", "1", "--no-probe", "--mode", "write"},
		{"benchmark", work, "--images-dir", "/tmp", "--type", "tar",
			"--size", "1", "--file-size", "1", "--no-probe", "--mode", "write"},
	} {
		_, _, _, err := runCmd(t, args...)
		if ExitCode(err) != 2 {
			t.Fatalf("%v: ExitCode(%v) = %d, want 2", args, err, ExitCode(err))
		}
		if err == nil || !strings.Contains(err.Error(), "images-dir") {
			t.Fatalf("%v: error = %v, want images-dir mention", args, err)
		}
	}
}

func TestBenchmarkHelpOmitsRootFlags(t *testing.T) {
	withWorkspace(t)
	stdout, stderr, _, err := runCmd(t, "benchmark", "--help")
	if err != nil {
		t.Fatalf("benchmark --help: %v\n%s", err, stderr)
	}
	help := stdout + stderr
	if strings.Contains(help, "Global Flags:") {
		t.Fatalf("benchmark help lists Global Flags:\n%s", help)
	}
	for _, flag := range []string{"--images-dir", "--cache-dir", "--log-level"} {
		if strings.Contains(help, flag) {
			t.Fatalf("benchmark help still documents %s:\n%s", flag, help)
		}
	}
	if !strings.Contains(help, "--readonly") {
		t.Fatalf("benchmark help missing local --readonly:\n%s", help)
	}
}

func TestBenchmarkValidatesWithoutProjectConfig(t *testing.T) {
	home, work := withWorkspace(t)
	if _, err := os.Stat(filepath.Join(work, ".tiler.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("project config present: %v", err)
	}
	_ = os.Remove(filepath.Join(home, ".tiler.yaml"))
	stubBenchmarkRun(t)

	_, _, cfg, err := runCmd(t, "benchmark", work,
		"--type", "tar", "--size", "1.5G", "--file-size", "1", "--no-probe")
	if ExitCode(err) != 2 {
		t.Fatalf("bad size without config: ExitCode(%v) = %d, want 2", err, ExitCode(err))
	}
	if cfg != (config.Config{}) {
		t.Fatalf("loaded config = %+v, want none", cfg)
	}

	got, ok := captureBenchmarkRun(t)
	_, _, cfg, err = runCmd(t, "benchmark", work,
		"--type", "tar", "--size", "128M", "--file-size", "4M",
		"--no-probe", "--mode", "write")
	if err != nil {
		t.Fatalf("valid flags: %v", err)
	}
	if cfg != (config.Config{}) {
		t.Fatalf("loaded config = %+v, want none", cfg)
	}
	if !ok() {
		t.Fatal("runBenchmark was not called")
	}
	d := got()
	if d.Size != 128<<20 || d.FileSize != 4<<20 || d.Type != "tar" {
		t.Fatalf("decision = %+v", d)
	}
	if !d.Create || d.Read || d.Probe {
		t.Fatalf("create/read/probe = %v/%v/%v", d.Create, d.Read, d.Probe)
	}
}

func TestBenchmarkReservedCwdStillRuns(t *testing.T) {
	home, _ := withWorkspace(t)
	inside := filepath.Join(home, ".tiler", "images")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(inside)
	stubBenchmarkRun(t)

	_, _, cfg, err := runCmd(t, "benchmark", inside,
		"--type", "tar", "--size", "1", "--file-size", "1",
		"--no-probe", "--mode", "write")
	if err != nil {
		t.Fatalf("benchmark inside reserved tree: %v", err)
	}
	if cfg != (config.Config{}) {
		t.Fatalf("loaded config = %+v, want none", cfg)
	}
}

func stubBenchmarkRun(t *testing.T) {
	t.Helper()
	orig := runBenchmark
	t.Cleanup(func() { runBenchmark = orig })
	runBenchmark = func(benchmark.Decision, benchmark.Env) error { return nil }
}

func captureBenchmarkRun(t *testing.T) (got func() benchmark.Decision, ok func() bool) {
	t.Helper()
	orig := runBenchmark
	t.Cleanup(func() { runBenchmark = orig })
	var (
		called bool
		d      benchmark.Decision
	)
	runBenchmark = func(decision benchmark.Decision, _ benchmark.Env) error {
		called = true
		d = decision
		return nil
	}
	return func() benchmark.Decision { return d }, func() bool { return called }
}
