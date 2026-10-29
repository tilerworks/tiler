package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/mountinfo"
)

func TestBuildImportFourForms(t *testing.T) {
	home, work := withWorkspace(t)
	images, lib := libFlags(home)
	refuseImportSideEffects(t)
	src := filepath.Join(work, "app.sqsh")
	writeImportPayload(t, src)
	sum := fileSHA256(t, src)
	dir := filepath.Join(images, "vendor", "app")

	importOK(t, lib, "link vendor/app "+image.UnknownSHA256+"\n",
		"build", "--link", src, "--image", "vendor/app")
	importOK(t, lib, "copy vendor/copy "+sum+"\n",
		"build", "--copy", src, "--image", "vendor/copy")
	importOK(t, lib, "link vendor/linked "+image.UnknownSHA256+"\n",
		"build", "--link", dir, "--hash", image.UnknownSHA256[:8], "--image", "vendor/linked")
	importOK(t, lib, "copy vendor/copied "+sum+"\n",
		"build", "--copy", dir, "--hash", "all", "--image", "vendor/copied")
	assertNoSession(t, home)
}

func TestBuildImportRejectsInvalidFlags(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	src := filepath.Join(work, "app.sqsh")
	writeImportPayload(t, src)
	cases := []struct {
		name string
		args []string
	}{
		{name: "hash without mode", args: []string{"build", "--hash", "abcd"}},
		{name: "both modes", args: []string{"build", "--link", src, "--copy", src, "--image", "vendor/app"}},
		{name: "missing image", args: []string{"build", "--link", src}},
		{name: "empty image", args: []string{"build", "--link", src, "--image="}},
		{name: "empty link", args: []string{"build", "--link=", "--image", "vendor/app"}},
		{name: "positional", args: []string{"build", "--link", src, "--image", "vendor/app", "."}},
		{name: "full", args: []string{"build", "--link", src, "--image", "vendor/app", "--full"}},
		{name: "incremental", args: []string{"build", "--copy", src, "--image", "vendor/app", "-i"}},
		{name: "split", args: []string{"build", "--link", src, "--image", "vendor/app", "--split", "0"}},
		{name: "format", args: []string{"build", "--link", src, "--image", "vendor/app", "--format", "squashfs"}},
		{name: "compression", args: []string{"build", "--link", src, "--image", "vendor/app", "--compression", "gzip"}},
		{name: "message", args: []string{"build", "--link", src, "--image", "vendor/app", "-m", "hi"}},
		{name: "description file", args: []string{"build", "--link", src, "--image", "vendor/app", "-F", src}},
		{name: "hash on file", args: []string{"build", "--link", src, "--hash", "all", "--image", "vendor/app"}},
		{name: "empty hash on file", args: []string{"build", "--copy", src, "--hash=", "--image", "vendor/app"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, _, err := runCmd(t, append(append([]string{}, lib...), tc.args...)...)
			if ExitCode(err) != 2 {
				t.Fatalf("ExitCode(%v) = %d, want 2\n%s", err, ExitCode(err), stderr)
			}
		})
	}
}

func TestBuildImportDirectoryErrors(t *testing.T) {
	home, work := withWorkspace(t)
	images, lib := libFlags(home)
	refuseImportSideEffects(t)
	plain := t.TempDir()
	_, stderr, err := runImport(t, lib, "build", "--link", plain, "--image", "vendor/app")
	if ExitCode(err) != 1 || !strings.Contains(stderr, "vendor/app") || !strings.Contains(stderr, plain) {
		t.Fatalf("plain dir: %v\n%s", err, stderr)
	}

	src := filepath.Join(work, "app.sqsh")
	writeImportPayload(t, src)
	importOK(t, lib, "link vendor/app "+image.UnknownSHA256+"\n",
		"build", "--link", src, "--image", "vendor/app")
	dir := filepath.Join(images, "vendor", "app")
	_, stderr, err = runImport(t, lib, "build", "--link", dir, "--hash", "ALL", "--image", "vendor/other")
	if ExitCode(err) != 2 {
		t.Fatalf("ALL hash: %v\n%s", err, stderr)
	}
	_, stderr, err = runImport(t, lib, "build", "--copy", dir, "--hash", "ffff", "--image", "vendor/miss")
	if ExitCode(err) != 1 || !strings.Contains(stderr, "no matching image") {
		t.Fatalf("missing hash: %v\n%s", err, stderr)
	}
	assertNoSession(t, home)
}

func TestBuildImportAmbiguousHash(t *testing.T) {
	home, work := withWorkspace(t)
	images, lib := libFlags(home)
	refuseImportSideEffects(t)
	left, right := filepath.Join(work, "left.sqsh"), filepath.Join(work, "right.sqsh")
	writeImportPayload(t, left)
	writeImportPayload(t, right)
	idA := "abcd" + strings.Repeat("0", 60)
	idB := "abcd" + strings.Repeat("1", 60)
	writeTestFile(t, left+".sha256", idA+"\n")
	writeTestFile(t, right+".sha256", idB+"  right.sqsh\n")
	importOK(t, lib, "link vendor/app "+idA+"\n", "build", "--link", left, "--image", "vendor/app")
	importOK(t, lib, "link vendor/app "+idB+"\n", "build", "--link", right, "--image", "vendor/app")
	dir := filepath.Join(images, "vendor", "app")
	_, stderr, err := runImport(t, lib, "build", "--link", dir, "--hash", "ABCD", "--image", "vendor/other")
	if ExitCode(err) != 1 || !strings.Contains(stderr, idA) || !strings.Contains(stderr, idB) {
		t.Fatalf("ambiguous: %v\n%s", err, stderr)
	}
	assertNoSession(t, home)
}

func TestMountFileFlagRejected(t *testing.T) {
	home, work := withWorkspace(t)
	images, lib := libFlags(home)
	src := filepath.Join(work, "app.sqsh")
	writeImportPayload(t, src)
	view := filepath.Join(work, "view")
	if err := os.Mkdir(view, 0o755); err != nil {
		t.Fatal(err)
	}
	_, stderr, _, err := runCmd(t, append(lib, "mount", "--file", src, "vendor/app", view)...)
	if ExitCode(err) != 2 {
		t.Fatalf("ExitCode(%v) = %d, want 2\n%s", err, ExitCode(err), stderr)
	}
	if _, err := os.Stat(filepath.Join(images, "vendor")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mount --file wrote a library: %v", err)
	}
	assertNoSession(t, home)
	stdout, stderr, _, err := runCmd(t, "mount", "--help")
	if err != nil {
		t.Fatalf("mount --help: %v\n%s", err, stderr)
	}
	if strings.Contains(stdout, "--file") {
		t.Fatalf("mount help still documents --file:\n%s", stdout)
	}
	for _, want := range []string{"build --link", "64 zeros is unknown", "reports the skip"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("mount help missing %s:\n%s", want, stdout)
		}
	}
	stdout, stderr, _, err = runCmd(t, "build", "--help")
	if err != nil {
		t.Fatalf("build --help: %v\n%s", err, stderr)
	}
	for _, want := range []string{
		"--link", "--copy", "--hash",
		"tiler build --link /mnt/nas/vendor/app.sqsh --image vendor/app",
		"tiler mount vendor/app work",
		"latest directory version",
		"unknown digest of 64 zeros",
		"source conflict",
		"keeps that id",
		"read image description from file (use - for stdin)",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("build help missing %s:\n%s", want, stdout)
		}
	}
}

func importOK(t *testing.T, lib []string, want string, args ...string) {
	t.Helper()
	stdout, stderr, err := runImport(t, lib, args...)
	if err != nil || stdout != want {
		t.Fatalf("build %v: %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout, stderr)
	}
}

func runImport(t *testing.T, lib []string, args ...string) (string, string, error) {
	t.Helper()
	full := append(append([]string{}, lib...), args...)
	return runCmdReader(t, failReader{errors.New("import read stdin")}, full...)
}

func refuseImportSideEffects(t *testing.T) {
	t.Helper()
	origTable, origPack, origHdi := loadMountTable, packTool, hdiutilTool
	origSmoke, origRebase, origLook := incrSmoke, rebaseMountedStack, toolLookPath
	t.Cleanup(func() {
		loadMountTable, packTool, hdiutilTool = origTable, origPack, origHdi
		incrSmoke, rebaseMountedStack, toolLookPath = origSmoke, origRebase, origLook
	})
	loadMountTable = func() ([]mountinfo.Mount, error) {
		t.Fatal("import read the mount table")
		return nil, nil
	}
	packTool = func(string, []string, ...string) error {
		t.Fatal("import ran a pack tool")
		return nil
	}
	hdiutilTool = func(string, []string, ...string) error {
		t.Fatal("import ran hdiutil")
		return nil
	}
	incrSmoke = func(string) error {
		t.Fatal("import ran the mount smoke gate")
		return nil
	}
	rebaseMountedStack = func(config.Config, string, string, string, string) error {
		t.Fatal("import rebased a session")
		return nil
	}
	toolLookPath = func(string) (string, error) {
		t.Fatal("import looked up a toolchain binary")
		return "", errors.New("tool")
	}
}

func assertNoSession(t *testing.T, home string) {
	t.Helper()
	entries, err := os.ReadDir(sessionsDir(home))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil || len(entries) != 0 {
		t.Fatalf("sessions = %v, %v", entries, err)
	}
}

func writeImportPayload(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		writeKolyFile(t, path)
		return
	}
	writeSquashFile(t, path)
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
