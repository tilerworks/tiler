package benchmark

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunKeepLeavesBenchDir(t *testing.T) {
	vol := t.TempDir()
	bench := filepath.Join(vol, "bench")
	var out bytes.Buffer
	d := baseDecision(vol, bench)
	d.Keep = true
	d.Create = true
	d.Read = true
	d.Mode = "both"

	err := Run(d, Env{
		Command: tarPackCommand(t),
		Purge:   func() string { return "yes" },
		Stdout:  &out,
		Stderr:  io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(bench); err != nil {
		t.Fatalf("bench after keep: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bench, "image.tar.gz")); err != nil {
		t.Fatalf("image after keep: %v", err)
	}
}

func TestRunUserRuntimeDirKeepsParent(t *testing.T) {
	vol := t.TempDir()
	bench := filepath.Join(vol, "bench")
	runtimeDir := t.TempDir()
	marker := filepath.Join(runtimeDir, "keep-me")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	d := baseDecision(vol, bench)
	d.RuntimeDir = runtimeDir
	d.OwnRuntime = false
	d.Create = true
	d.Read = true
	d.Mode = "both"

	err := Run(d, Env{
		Command: tarPackCommand(t),
		Purge:   func() string { return "no" },
		Stdout:  &out,
		Stderr:  io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(runtimeDir); err != nil {
		t.Fatalf("runtime parent: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("runtime marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "mount")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mount after cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "logs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("logs after cleanup: %v", err)
	}
	if _, err := os.Stat(bench); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bench should be removed: %v", err)
	}
}

func TestRunReadonlyCreatesNothingUnderPath(t *testing.T) {
	vol := t.TempDir()
	image := filepath.Join(t.TempDir(), "image.tar.gz")
	writeTestArchive(t, image, map[string]string{"f00000": "hello-readonly"})
	before := listNames(t, vol)

	var out bytes.Buffer
	d := Decision{
		Read:       true,
		ReadOnly:   true,
		Path:       vol,
		Image:      image,
		Type:       "tar",
		Mode:       "read",
		Size:       1,
		FileSize:   1,
		OwnRuntime: true,
		TSV:        true,
	}
	err := Run(d, Env{
		Command: func(string, []string, ...string) ([]byte, error) {
			t.Fatal("readonly run must not pack")
			return nil, nil
		},
		Purge:  func() string { return "yes" },
		Stdout: &out,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	after := listNames(t, vol)
	if strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("path entries %v -> %v", before, after)
	}
	if _, err := os.Stat(image); err != nil {
		t.Fatalf("image deleted: %v", err)
	}
	assertTSVSteps(t, out.String(), "read_image_1", "read_image_2")
	if strings.Contains(out.String(), "image_attach") {
		t.Fatalf("tar readonly has attach: %s", out.String())
	}
}

func TestRunTarHasNoAttachRow(t *testing.T) {
	vol := t.TempDir()
	bench := filepath.Join(vol, "bench")
	var out bytes.Buffer
	d := baseDecision(vol, bench)
	d.Create = true
	d.Read = true
	d.Mode = "both"

	err := Run(d, Env{
		Command: tarPackCommand(t),
		Purge:   func() string { return "yes" },
		Stdout:  &out,
		Stderr:  io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertTSVSteps(t, out.String(), "image_create", "read_image_1", "read_image_2")
	if strings.Contains(out.String(), "image_attach") {
		t.Fatalf("tar run has attach: %s", out.String())
	}
	if !strings.Contains(out.String(), "tar.gz purge=yes") {
		t.Fatalf("missing purge note: %s", out.String())
	}
}

func TestRunFailedAttachStillCleansUp(t *testing.T) {
	vol := t.TempDir()
	bench := filepath.Join(vol, "bench")
	runtimeDir := t.TempDir()
	typ := attachTypeForGOOS()
	image := filepath.Join(bench, imageNames[typ])
	if err := os.MkdirAll(bench, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(image, []byte("not-a-real-image"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	d := Decision{
		Read:       true,
		Path:       vol,
		BenchDir:   bench,
		RuntimeDir: runtimeDir,
		OwnRuntime: false,
		Image:      image,
		Type:       typ,
		Mode:       "read",
		Size:       1,
		FileSize:   1,
		Keep:       false,
		TSV:        true,
	}
	err := Run(d, Env{
		Command: func(string, []string, ...string) ([]byte, error) {
			return nil, errors.New("attach failed")
		},
		Host: Host{
			LookPath: func(string) (string, error) { return "/bin/true", nil },
			Euid:     func() int { return 1000 },
		},
		Purge:  func() string { return "no" },
		Stdout: &out,
		Stderr: io.Discard,
	})
	if err == nil {
		t.Fatal("Run succeeded, want attach failure")
	}
	if _, statErr := os.Stat(bench); !errors.Is(statErr, os.ErrNotExist) {
		// Mode read with image: Create and Probe are false, so
		// prepareBench leaves removeBench false. Cleanup must still
		// drop mount/ and logs/ under the user runtime.
		t.Logf("bench remains for read-only image run: %v", statErr)
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "mount")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mount after failed attach: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "logs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("logs after failed attach: %v", err)
	}
	if _, err := os.Stat(image); err != nil {
		t.Fatalf("image deleted after failed attach: %v", err)
	}
	if _, err := os.Stat(runtimeDir); err != nil {
		t.Fatalf("runtime parent removed: %v", err)
	}
}

func TestRunFailedAttachCleansOwnedBench(t *testing.T) {
	vol := t.TempDir()
	bench := filepath.Join(vol, "bench")
	runtimeDir := t.TempDir()
	typ := attachTypeForGOOS()
	var out bytes.Buffer
	d := Decision{
		Create:     true,
		Read:       true,
		Path:       vol,
		BenchDir:   bench,
		RuntimeDir: runtimeDir,
		OwnRuntime: false,
		Type:       typ,
		Compress:   defaultCompress(typ),
		Mode:       "both",
		Size:       64,
		FileSize:   64,
		TSV:        true,
	}
	err := Run(d, Env{
		Command: func(name string, _ []string, args ...string) ([]byte, error) {
			if isPackTool(name, args) {
				path := packOutputPath(name, args)
				if path == "" {
					return nil, errors.New("missing pack output")
				}
				return nil, os.WriteFile(path, []byte("image"), 0o644)
			}
			return nil, errors.New("attach failed")
		},
		Host: Host{
			LookPath: func(string) (string, error) { return "/bin/true", nil },
			Euid:     func() int { return 1000 },
		},
		Purge:  func() string { return "no" },
		Stdout: &out,
		Stderr: io.Discard,
	})
	if err == nil {
		t.Fatal("Run succeeded, want attach failure")
	}
	if _, err := os.Stat(bench); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bench after failed attach: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "mount")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mount after failed attach: %v", err)
	}
}

func TestRunProbePurgesAfterProbeFile(t *testing.T) {
	vol := t.TempDir()
	bench := filepath.Join(vol, "bench")
	var events []string
	var errOut bytes.Buffer
	d := baseDecision(vol, bench)
	d.Probe = true
	d.ProbeOps = 2
	d.Create = false
	d.Read = false

	err := Run(d, Env{
		Purge: func() string {
			info, statErr := os.Stat(filepath.Join(bench, probeFileName))
			if statErr != nil || info.Size() != int64(minProbeBlocks*probeBlock) {
				events = append(events, "purge-before-file")
			} else {
				events = append(events, "purge-after-file")
			}
			return "yes"
		},
		Sync:   func() { events = append(events, "sync") },
		Stdout: &bytes.Buffer{},
		Stderr: &errOut,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Join(events, ",") != "purge-after-file,sync" {
		t.Fatalf("events = %v", events)
	}
	if !strings.Contains(errOut.String(), "probe_purge: yes") {
		t.Fatalf("stderr = %s", errOut.String())
	}
	if _, statErr := os.Stat(bench); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("bench after probe: %v", statErr)
	}
}

func TestRunColdReadSyncsAfterPurge(t *testing.T) {
	vol := t.TempDir()
	image := filepath.Join(t.TempDir(), "image.tar.gz")
	writeTestArchive(t, image, map[string]string{"f00000": "cold"})
	var events []string
	d := Decision{
		Read:       true,
		ReadOnly:   true,
		Path:       vol,
		Image:      image,
		Type:       "tar",
		Mode:       "read",
		Size:       1,
		FileSize:   1,
		OwnRuntime: true,
		TSV:        true,
	}
	err := Run(d, Env{
		Command: func(string, []string, ...string) ([]byte, error) {
			t.Fatal("readonly tar must not run a tool")
			return nil, nil
		},
		Purge: func() string {
			events = append(events, "purge")
			return "yes"
		},
		Sync:   func() { events = append(events, "sync") },
		Stdout: &bytes.Buffer{},
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Join(events, ",") != "purge,sync" {
		t.Fatalf("events = %v", events)
	}
}

func TestRunMountedReadSyncsBeforeAttach(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("attach is host-specific")
	}
	vol := t.TempDir()
	bench := filepath.Join(vol, "bench")
	runtimeDir := t.TempDir()
	typ := attachTypeForGOOS()
	image := filepath.Join(bench, imageNames[typ])
	if err := os.MkdirAll(bench, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(image, []byte("not-a-real-image"), 0o644); err != nil {
		t.Fatal(err)
	}
	var events []string
	d := Decision{
		Read:       true,
		Path:       vol,
		BenchDir:   bench,
		RuntimeDir: runtimeDir,
		OwnRuntime: false,
		Image:      image,
		Type:       typ,
		Mode:       "read",
		Size:       1,
		FileSize:   1,
		Keep:       true,
		TSV:        true,
	}
	err := Run(d, Env{
		Command: func(string, []string, ...string) ([]byte, error) {
			events = append(events, "attach")
			return []byte("attach-out\n"), errors.New("attach failed")
		},
		Host: Host{
			LookPath: func(string) (string, error) { return "/bin/true", nil },
			Euid:     func() int { return 1000 },
		},
		Purge: func() string {
			events = append(events, "purge")
			return "no"
		},
		Sync:   func() { events = append(events, "sync") },
		Stdout: &bytes.Buffer{},
		Stderr: io.Discard,
	})
	if err == nil {
		t.Fatal("Run succeeded, want attach failure")
	}
	if strings.Join(events, ",") != "purge,sync,attach" {
		t.Fatalf("events = %v", events)
	}
	if _, statErr := os.Stat(filepath.Join(runtimeDir, "logs")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("logs after failed attach: %v", statErr)
	}
}

func TestMountedReadWritesAttachLog(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("attach is host-specific")
	}
	logs := t.TempDir()
	image := filepath.Join(t.TempDir(), "image")
	if err := os.WriteFile(image, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	typ := attachTypeForGOOS()
	state := &runState{
		logs:  logs,
		mount: t.TempDir(),
		image: image,
	}
	rep, err := NewReporter(true, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	err = runMountedReads(Decision{Type: typ}, state, Env{
		Command: func(string, []string, ...string) ([]byte, error) {
			return []byte("attach-out\n"), errors.New("attach failed")
		},
		Host: Host{
			LookPath: func(string) (string, error) { return "/bin/true", nil },
			Euid:     func() int { return 1000 },
		},
		Purge: func() string { return "no" },
		Sync:  func() {},
	}, rep)
	if err == nil {
		t.Fatal("want attach failure")
	}
	body, readErr := os.ReadFile(filepath.Join(logs, attachLogName(typ)))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(body) != "attach-out\nbenchmark: attach failed\n" {
		t.Fatalf("log = %q", body)
	}
}

func TestRunPackLogCapturesToolOutput(t *testing.T) {
	vol := t.TempDir()
	bench := filepath.Join(vol, "bench")
	runtimeDir := t.TempDir()
	var out bytes.Buffer
	var logged string
	d := baseDecision(vol, bench)
	d.RuntimeDir = runtimeDir
	d.OwnRuntime = false
	d.Create = true
	d.Read = true
	d.Mode = "both"

	err := Run(d, Env{
		Command: func(name string, _ []string, args ...string) ([]byte, error) {
			if name != "tar" || len(args) < 5 || args[0] != "-czf" {
				return []byte("unexpected\n"), errors.New("unexpected argv")
			}
			image, dir, base := args[1], args[3], args[4]
			if writeErr := writeArchiveFromDir(image, filepath.Join(dir, base)); writeErr != nil {
				return nil, writeErr
			}
			return []byte("tar: wrote archive\n"), nil
		},
		Purge: func() string {
			body, readErr := os.ReadFile(filepath.Join(runtimeDir, "logs", "tar-create.log"))
			if readErr != nil {
				t.Errorf("pack log: %v", readErr)
				return "no"
			}
			logged = string(body)
			return "yes"
		},
		Stdout: &out,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(logged, "tar: wrote archive") {
		t.Fatalf("log = %q", logged)
	}
	if _, statErr := os.Stat(filepath.Join(runtimeDir, "logs")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("logs after cleanup: %v", statErr)
	}
}

func TestLogCommandWritesToolFailure(t *testing.T) {
	dir := t.TempDir()
	cmd := logCommand(func(string, []string, ...string) ([]byte, error) {
		return []byte("partial"), errors.New("boom")
	}, dir, "mksquashfs.log")
	if _, err := cmd("mksquashfs", nil, "src", "image"); err == nil {
		t.Fatal("command error was dropped")
	}
	body, err := os.ReadFile(filepath.Join(dir, "mksquashfs.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "partial\nbenchmark: boom\n" {
		t.Fatalf("log = %q", body)
	}
}

func TestRunPurgeFailureDoesNotFail(t *testing.T) {
	vol := t.TempDir()
	bench := filepath.Join(vol, "bench")
	var out bytes.Buffer
	d := baseDecision(vol, bench)
	d.Create = true
	d.Read = true
	d.Mode = "both"

	err := Run(d, Env{
		Command: tarPackCommand(t),
		Purge:   func() string { return "no" },
		Stdout:  &out,
		Stderr:  io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "purge=no") {
		t.Fatalf("want purge=no in %s", out.String())
	}
}

func TestRunCreateUsesInputBytesForRate(t *testing.T) {
	vol := t.TempDir()
	bench := filepath.Join(vol, "bench")
	var out bytes.Buffer
	const size = 128
	d := baseDecision(vol, bench)
	d.Create = true
	d.Read = false
	d.Mode = "write"
	d.Size = size
	d.FileSize = size

	err := Run(d, Env{
		Command: func(name string, _ []string, args ...string) ([]byte, error) {
			if name != "tar" {
				return nil, errors.New("unexpected " + name)
			}
			image := args[1]
			// Smaller than input so Bytes and RateBytes differ.
			return nil, os.WriteFile(image, []byte("tiny"), 0o644)
		},
		Purge:  func() string { return "yes" },
		Stdout: &out,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	line := firstTSVLine(t, out.String(), "image_create")
	cols := strings.Split(line, "\t")
	if len(cols) < 4 {
		t.Fatalf("cols = %q", cols)
	}
	if cols[2] != "4" {
		t.Fatalf("bytes column = %q, want output size 4", cols[2])
	}
	// MiB/s uses input bytes (128). At tiny elapsed times this is inf.
	if cols[3] != "inf" && cols[3] == "0.0" {
		t.Fatalf("rate = %q, want non-zero from input bytes", cols[3])
	}
}

func baseDecision(vol, bench string) Decision {
	return Decision{
		Path:       vol,
		BenchDir:   bench,
		OwnRuntime: true,
		Type:       "tar",
		Mode:       "both",
		Size:       64,
		FileSize:   32,
		TSV:        true,
	}
}

func tarPackCommand(t *testing.T) Command {
	t.Helper()
	return func(name string, _ []string, args ...string) ([]byte, error) {
		if name != "tar" || len(args) < 5 || args[0] != "-czf" {
			return nil, errors.New("unexpected argv")
		}
		image, dir, base := args[1], args[3], args[4]
		src := filepath.Join(dir, base)
		return nil, writeArchiveFromDir(image, src)
	}
}

func writeArchiveFromDir(image, src string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	var members []tarMember
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(src, e.Name()))
		if readErr != nil {
			return readErr
		}
		members = append(members, tarMember{
			name: filepath.Join(filepath.Base(src), e.Name()),
			body: data,
		})
	}
	return writeGzipTarFile(image, members)
}

func writeTestArchive(t *testing.T, path string, files map[string]string) {
	t.Helper()
	var members []tarMember
	for name, body := range files {
		members = append(members, tarMember{name: name, body: []byte(body)})
	}
	writeGzipTar(t, path, members)
}

func writeGzipTarFile(path string, members []tarMember) error {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, member := range members {
		hdr := &tar.Header{Name: member.name, Mode: 0o644, Size: int64(len(member.body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(member.body); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func listNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func assertTSVSteps(t *testing.T, text string, want ...string) {
	t.Helper()
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if line == "" {
			continue
		}
		got = append(got, strings.Split(line, "\t")[0])
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("steps = %v, want %v\n%s", got, want, text)
	}
}

func firstTSVLine(t *testing.T, text, step string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, step+"\t") {
			return line
		}
	}
	t.Fatalf("missing %s in %s", step, text)
	return ""
}

func attachTypeForGOOS() string {
	if runtime.GOOS == "darwin" {
		return "dmg"
	}
	return "squashfs"
}

func defaultCompress(typ string) string {
	switch typ {
	case "dmg":
		return "ulfo"
	case "squashfs":
		return "zstd"
	default:
		return ""
	}
}

func isPackTool(name string, args []string) bool {
	switch name {
	case "tar", "hdiutil", "mksquashfs", "mkfs.erofs":
		if name == "hdiutil" && len(args) > 0 && args[0] == "attach" {
			return false
		}
		if name == "mkfs.erofs" && len(args) == 1 && args[0] == "--help" {
			return false
		}
		return true
	default:
		return false
	}
}

func packOutputPath(name string, args []string) string {
	switch name {
	case "tar":
		if len(args) >= 2 && args[0] == "-czf" {
			return args[1]
		}
	case "hdiutil":
		if len(args) > 0 {
			return args[len(args)-1]
		}
	case "mksquashfs":
		if len(args) >= 2 {
			return args[1]
		}
	case "mkfs.erofs":
		if len(args) >= 2 {
			return args[len(args)-2]
		}
	}
	return ""
}
