package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/image"
)

func TestMissingMountImageDiagnostic(t *testing.T) {
	home, _ := withWorkspace(t)
	stdout, stderr, _, err := runCmd(t, "mount", "--rm", "my/images", "mnt1")
	if ExitCode(err) != 1 || stdout != "" {
		t.Fatalf("stdout=%q, error=%v", stdout, err)
	}
	for _, want := range []string{`image "my/images"`, "was not found", filepath.Join(home, ".tiler", "images"), "tiler images list"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("diagnostic %q missing %q", stderr, want)
		}
	}
}

func TestWarningPreservesMissingImageHintLayout(t *testing.T) {
	var out strings.Builder
	err := fmt.Errorf("lookup: %w", &image.MissingManifestError{Name: "my/images", Library: "/bad\npath"})
	if _, err := writeWarning(&out, "session", "%v\n", err); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\n  Hint:") || strings.Contains(out.String(), "\npath") {
		t.Fatalf("warning layout = %q", out.String())
	}
}

func TestMissingMetadataIsNotMissingRepository(t *testing.T) {
	home, _ := withWorkspace(t)
	dir := filepath.Join(home, ".tiler", "images", "my", "images")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	_, stderr, _, err := runCmd(t, "mount", "my/images", "mnt1")
	if ExitCode(err) != 1 || !strings.Contains(stderr, "metadata is missing") || !strings.Contains(stderr, filepath.Join(dir, "meta.yaml")) || strings.Contains(stderr, "was not found") {
		t.Fatalf("diagnostic=%q, error=%v", stderr, err)
	}
}

func TestWarningEscapesDynamicControls(t *testing.T) {
	var out strings.Builder
	if _, err := writeWarning(&out, "build", "image %s: %v\n  Hint: retry after fixing the source.\n", "bad\nname", os.ErrPermission); err != nil {
		t.Fatal(err)
	}
	want := "Warning: build: image bad\\u000aname: permission denied\n  Hint: retry after fixing the source.\n"
	if out.String() != want {
		t.Fatalf("warning=%q, want %q", out.String(), want)
	}
}

func TestMissingVersionRemainsRuntimeFailure(t *testing.T) {
	home, _ := withWorkspace(t)
	writeVersionForest(t, filepath.Join(home, ".tiler", "images"), "my/images", versionNode("a", image.KindFull, "", time.Now().UTC()))
	_, stderr, _, err := runCmd(t, "mount", "my/images:dddd", "mnt1")
	if ExitCode(err) != 1 || !errors.Is(err, image.ErrVersionNotFound) || !strings.Contains(stderr, "tiler images show my/images") {
		t.Fatalf("diagnostic=%q, error=%v", stderr, err)
	}
}
