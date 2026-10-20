//go:build linux

package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// doctorProbeNames is the full probe set a real doctor run renders.
var doctorProbeNames = []string{
	"fuse_device",
	"squashfuse_ll",
	"fuse_overlayfs",
	"fusermount3",
	"mksquashfs",
	"git",
	"squashfuse_subdir",
	"file_limit_images",
	"file_limit_cache",
	"storage_perf_images",
	"storage_perf_cache",
	"cache_advice",
	"contrib_concatfs",
	"contrib_slowfs",
}

// doctorLine is one parsed human report line.
type doctorLine struct {
	status string
	detail string
}

func TestDoctorRealEnvironmentFullProbe(t *testing.T) {
	home, _ := withWorkspace(t)
	imagesDir := filepath.Join(home, "lib", "images")
	cacheDir := filepath.Join(home, "lib", "cache")
	for _, dir := range []string{imagesDir, cacheDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	lib := []string{
		"--images-dir", imagesDir,
		"--cache-dir", cacheDir,
		"--sessions-dir", filepath.Join(home, "lib", "sessions"),
	}

	// The human form renders every probe with a legal status.
	stdout, stderr, _, err := runCmd(t, append(lib, "doctor")...)
	human := parseDoctorLines(t, stdout)
	if len(human) != len(doctorProbeNames) {
		t.Fatalf("probe count = %d, want %d\n%s", len(human), len(doctorProbeNames), stdout)
	}
	for _, name := range doctorProbeNames {
		line, ok := human[name]
		if !ok {
			t.Fatalf("probe %s is missing\n%s", name, stdout)
		}
		switch line.status {
		case "pass", "fail", "warn", "info":
		default:
			t.Fatalf("%s has the illegal status %q", name, line.status)
		}
	}

	// A tool that is actually installed must yield a parseable banner.
	// Presence without a pass means the version probe drifted from the
	// real tool output; only genuinely absent tools may fail. This is
	// the assertion that catches a parser regression on a live host.
	toolBinaries := map[string]string{
		"squashfuse_ll":  "squashfuse_ll",
		"fuse_overlayfs": "fuse-overlayfs",
		"fusermount3":    "fusermount3",
		"mksquashfs":     "mksquashfs",
		"git":            "git",
	}
	for name, binary := range toolBinaries {
		if _, lookErr := exec.LookPath(binary); lookErr != nil {
			continue
		}
		if human[name].status != "pass" {
			t.Fatalf("%s is installed but probe = %s (%q), want pass",
				name, human[name].status, human[name].detail)
		}
	}

	// The exit code follows the verdict semantics: any fail exits 1
	// with the failed names reported, otherwise the run succeeds.
	failed := 0
	for _, line := range human {
		if line.status == "fail" {
			failed++
		}
	}
	if failed > 0 {
		if ExitCode(err) != 1 {
			t.Fatalf("ExitCode(%v) = %d, want 1 with %d failing probes",
				err, ExitCode(err), failed)
		}
		if !strings.Contains(stderr, "failing probes:") {
			t.Fatalf("stderr = %q, want the verdict listing the failed probes", stderr)
		}
	} else if err != nil {
		t.Fatalf("healthy run failed: %v\n%s", err, stderr)
	}

	// The storage probes conclude on the configured directories:
	// present directories never degrade to skipped, and the
	// performance probes measure real numbers on linux.
	for _, name := range []string{"file_limit_images", "file_limit_cache"} {
		detail := human[name].detail
		if strings.Contains(detail, "not present") || strings.Contains(detail, "not configured") {
			t.Fatalf("%s degraded although its directory exists: %q", name, detail)
		}
	}
	for _, name := range []string{"storage_perf_images", "storage_perf_cache"} {
		if human[name].status != "info" || !strings.Contains(human[name].detail, "seq ") {
			t.Fatalf("%s = %+v, want measured numbers", name, human[name])
		}
	}

	// The JSON form parses, carries the same verdict, and puts the
	// measurements into the structured fields.
	jsonStdout, _, _, jsonErr := runCmd(t, append(lib, "doctor", "--json")...)
	if ExitCode(jsonErr) != ExitCode(err) {
		t.Fatalf("json exit %d differs from human exit %d", ExitCode(jsonErr), ExitCode(err))
	}
	var doc doctorDocument
	if parseErr := json.Unmarshal([]byte(jsonStdout), &doc); parseErr != nil {
		t.Fatalf("doctor --json is not parseable: %v\n%s", parseErr, jsonStdout)
	}
	if len(doc.Results) != len(doctorProbeNames) {
		t.Fatalf("json results = %d, want %d", len(doc.Results), len(doctorProbeNames))
	}
	byName := map[string]string{}
	for _, result := range doc.Results {
		byName[result.Name] = result.Status
		switch result.Name {
		case "storage_perf_images", "storage_perf_cache":
			if result.SeqReadMiBps == nil || *result.SeqReadMiBps <= 0 ||
				result.SeqReadAvgMs == nil || *result.SeqReadAvgMs <= 0 ||
				result.RandReadAvgMs == nil || result.RandReadMaxMs == nil {
				t.Fatalf("%s carries no structured measurement: %+v", result.Name, result)
			}
		case "file_limit_images", "file_limit_cache":
			if result.LimitBytes != nil && *result.LimitBytes <= 0 {
				t.Fatalf("%s reports a non-positive limit: %+v", result.Name, result)
			}
		}
	}
	// The two runs measure independently, so only the statuses are
	// comparable; measurement details legitimately differ per run.
	for name, line := range human {
		if byName[name] != line.status {
			t.Fatalf("json %s status = %s differs from human %s",
				name, byName[name], line.status)
		}
	}

	// Every probe file is cleaned up: both directories are empty.
	for _, dir := range []string{imagesDir, cacheDir} {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("probe leftovers in %s: %v", dir, entries)
		}
	}
}

// parseDoctorLines parses the tab-separated human report.
func parseDoctorLines(t *testing.T, stdout string) map[string]doctorLine {
	t.Helper()
	lines := map[string]doctorLine{}
	for _, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			t.Fatalf("bad doctor line %q", line)
		}
		if fields[0] == "config" {
			// The config section precedes the probes and is not a probe.
			continue
		}
		lines[fields[0]] = doctorLine{status: fields[1], detail: fields[2]}
	}
	return lines
}
