//go:build unix

package image

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestPlanImportSourceRejectsFIFO(t *testing.T) {
	withLinuxLinkPolicy(t)
	nas := t.TempDir()
	fifo := filepath.Join(nas, "pipe.sqsh")
	if err := syscall.Mknod(fifo, syscall.S_IFIFO|0o644, 0); err != nil {
		t.Fatal(err)
	}
	_, err := PlanImportSource(ImportRequest{
		Mode: ImportLink, ImagesDir: t.TempDir(), Name: "vendor/app", Source: fifo,
	})
	if err == nil || !strings.Contains(err.Error(), "not a file or directory") {
		t.Fatalf("fifo: %v", err)
	}
	info, statErr := os.Lstat(fifo)
	if statErr != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("fifo changed: %v %+v", statErr, info)
	}
}
