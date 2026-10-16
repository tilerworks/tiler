package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
)

// libFlags points the library directories at a temporary tree under
// home, outside the working directory, so the reserved-tree precheck
// and the tests never interfere.
func libFlags(home string) (imagesDir string, args []string) {
	imagesDir = filepath.Join(home, "lib", "images")
	args = []string{
		"--images-dir", imagesDir,
		"--runtime-dir", filepath.Join(home, "lib", "runtime"),
		"--sessions-dir", filepath.Join(home, "lib", "sessions"),
	}
	return imagesDir, args
}

func sessionsDir(home string) string {
	return filepath.Join(home, "lib", "sessions")
}

// withBuildSeams installs the injected mount table, a fake packer
// that needs no mksquashfs, and a passing mount smoke gate.
func withBuildSeams(t *testing.T, table []mountinfo.Mount) {
	t.Helper()
	origTable, origPack, origSmoke := loadMountTable, packTool, incrSmoke
	t.Cleanup(func() {
		loadMountTable, packTool, incrSmoke = origTable, origPack, origSmoke
	})
	loadMountTable = func() ([]mountinfo.Mount, error) { return table, nil }
	packTool = fakePacker(t)
	incrSmoke = func(string) error { return nil }
}

// fakePacker writes deterministic bytes derived from the source tree
// digest, so equal trees pack to equal ids and different trees do not.
func fakePacker(t *testing.T) image.ToolRunner {
	t.Helper()
	return func(name string, args ...string) error {
		if name != "mksquashfs" {
			return fmt.Errorf("unexpected tool %q", name)
		}
		if len(args) < 2 {
			return fmt.Errorf("mksquashfs needs src and dst: %v", args)
		}
		sum := sha256.New()
		err := filepath.WalkDir(args[0], func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, relErr := filepath.Rel(args[0], p)
			if relErr != nil {
				return relErr
			}
			_, _ = fmt.Fprintf(sum, "%s %s\n", rel, d.Type())
			if d.Type().IsRegular() {
				body, readErr := os.ReadFile(p)
				if readErr != nil {
					return readErr
				}
				_, _ = sum.Write(body)
			}
			return nil
		})
		if err != nil {
			return err
		}
		body := "fake-squashfs " + hex.EncodeToString(sum.Sum(nil))
		return os.WriteFile(args[1], []byte(body), 0o644)
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildImage runs one build command and returns the printed id.
func buildImage(t *testing.T, lib []string, name, src string) string {
	t.Helper()
	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", name, src)...)
	if err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, stderr)
	}
	return buildOutput(t, stdout)
}

// buildOutput checks one build result line and returns the id.
func buildOutput(t *testing.T, stdout string) string {
	t.Helper()
	fields := strings.Fields(stdout)
	if len(fields) != 3 || !isHex16(fields[2]) {
		t.Fatalf("build stdout = %q", stdout)
	}
	return fields[2]
}

func isHex16(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// viewTable is one writable merged view over the library image, with
// its upper under the runtime tree.
func viewTable(imagesDir, name, merged, upper string) []mountinfo.Mount {
	owner, repo, _ := strings.Cut(name, "/")
	return []mountinfo.Mount{{
		Point:  merged,
		FSType: "fuse.fuse-overlayfs",
		Source: filepath.Join(imagesDir, owner, repo, image.ImageFile),
		Upper:  upper,
	}}
}

func TestBuildFullPrintsNameAndID(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")

	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--image", "alice/app", src)...)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, stderr)
	}
	fields := strings.Fields(stdout)
	if len(fields) != 3 || fields[0] != "full" || fields[1] != "alice/app" || !isHex16(fields[2]) {
		t.Fatalf("stdout = %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(imagesDir, "alice", "app", "image.sqsh")); err != nil {
		t.Fatal(err)
	}
	meta, err := image.Load(filepath.Join(imagesDir, "alice", "app", "meta.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != fields[2] {
		t.Fatalf("meta id = %s, stdout id = %s", meta.ID, fields[2])
	}
	if meta.Builder != Version {
		t.Fatalf("builder = %q, want %q", meta.Builder, Version)
	}
}

func TestBuildUsageErrorsExitTwo(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "f.txt"), "x")

	tests := []struct {
		name string
		args []string
	}{
		{name: "both mode flags", args: []string{"build", "--full", "--incr", "--image", "alice/app", src}},
		{name: "missing image name", args: []string{"build", src}},
		{name: "bad image name", args: []string{"build", "--image", "nope", src}},
		{name: "incr outside a view", args: []string{"build", "--incr", "--image", "alice/app", src}},
		{name: "two source arguments", args: []string{"build", "--image", "alice/app", src, src}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, _, err := runCmd(t, append(lib, tt.args...)...)
			if ExitCode(err) != 2 {
				t.Fatalf("ExitCode(%v) = %d, want 2", err, ExitCode(err))
			}
			if strings.HasPrefix(stdout, "full ") || strings.HasPrefix(stdout, "incr ") {
				t.Fatalf("usage failure printed a result: %q", stdout)
			}
		})
	}
}

func TestBuildPackerFailureExitsOne(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	packTool = func(string, ...string) error { return errors.New("mksquashfs: boom") }
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "f.txt"), "x")

	stdout, _, _, err := runCmd(t, append(lib, "build", "--image", "alice/app", src)...)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
	if stdout != "" {
		t.Fatalf("failed build printed %q", stdout)
	}
	if _, statErr := os.Stat(filepath.Join(imagesDir, "alice", "app", "image.sqsh")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed build left an active image: %v", statErr)
	}
}

func TestBuildIncrementalInsideView(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	runtimeDir := filepath.Join(home, "lib", "runtime")
	merged := filepath.Join(work, "merged")
	upper := filepath.Join(runtimeDir, "up-1-2", "upper")
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "base.txt"), "base")
	withBuildSeams(t, viewTable(imagesDir, "alice/app", merged, upper))
	fullID := buildImage(t, lib, "alice/app", src)

	writeTestFile(t, filepath.Join(upper, "new.txt"), "layer change")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(merged)
	stdout, stderr, _, err := runCmd(t, append(lib, "build")...)
	if err != nil {
		t.Fatalf("incr build: %v\n%s", err, stderr)
	}
	fields := strings.Fields(stdout)
	if len(fields) != 3 || fields[0] != "incr" || fields[1] != "alice/app" || !isHex16(fields[2]) {
		t.Fatalf("stdout = %q", stdout)
	}
	meta, err := image.Load(filepath.Join(imagesDir, "alice", "app", "meta.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != fullID || len(meta.Chain) != 1 || meta.Chain[0].ID != fields[2] {
		t.Fatalf("meta = %+v, stdout id = %s", meta, fields[2])
	}
	if _, err := os.Stat(filepath.Join(imagesDir, "alice", "app", "incr", fields[2]+".sqsh")); err != nil {
		t.Fatal(err)
	}
}

func TestBuildIncrementalRejectsSourceArgument(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	upper := filepath.Join(home, "lib", "runtime", "up-1-2", "upper")
	withBuildSeams(t, viewTable(imagesDir, "alice/app", merged, upper))
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(merged)
	_, _, _, err := runCmd(t, append(lib, "build", "sub")...)
	if ExitCode(err) != 2 {
		t.Fatalf("ExitCode(%v) = %d, want 2", err, ExitCode(err))
	}
}

func TestBuildIdentityFallsBackToSessionRecord(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	// A killed daemon left an anonymous table entry: no source
	// resolves through the library, and only the session record
	// still names the image behind the view.
	table := []mountinfo.Mount{{
		Point:  merged,
		FSType: "fuse.fuse-overlayfs",
		Source: "fuse-overlayfs",
	}}
	withBuildSeams(t, table)
	writeTestFile(t, filepath.Join(merged, "base.txt"), "base")
	writeSessionRecord(t, sessionsDir(home), "dev",
		libraryAction("alice/app", merged, session.ModeRW))
	t.Chdir(merged)

	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--full")...)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, stderr)
	}
	fields := strings.Fields(stdout)
	if len(fields) != 3 || fields[0] != "full" || fields[1] != "alice/app" || !isHex16(fields[2]) {
		t.Fatalf("stdout = %q", stdout)
	}

	// The recorded identity still refuses a conflicting explicit
	// name and accepts a consistent one.
	_, _, _, err = runCmd(t, append(lib, "build", "--full", "--image", "bob/other")...)
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflict: ExitCode(%v) = %d: %v", err, ExitCode(err), err)
	}
	if _, stderr, _, err := runCmd(t, append(lib, "build", "--full", "--image", "alice/app")...); err != nil {
		t.Fatalf("consistent build: %v\n%s", err, stderr)
	}
}

func TestBuildReadOnlyViewNeedsExplicitFull(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	table := []mountinfo.Mount{{
		Point:  merged,
		FSType: "overlay",
		Source: "overlay",
	}}
	withBuildSeams(t, table)
	writeTestFile(t, filepath.Join(merged, "f.txt"), "x")
	t.Chdir(merged)

	_, _, _, err := runCmd(t, append(lib, "build")...)
	if ExitCode(err) != 2 {
		t.Fatalf("ExitCode(%v) = %d, want 2", err, ExitCode(err))
	}

	stdout, stderr, _, err := runCmd(t, append(lib, "build", "--full", "--image", "alice/app")...)
	if err != nil {
		t.Fatalf("full build of the view: %v\n%s", err, stderr)
	}
	fields := strings.Fields(stdout)
	if len(fields) != 3 || fields[0] != "full" || fields[1] != "alice/app" {
		t.Fatalf("stdout = %q", stdout)
	}
}
