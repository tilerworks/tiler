//go:build unix

package image

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReadChecksumSidecarRejectsFIFOWithoutBlocking(t *testing.T) {
	t.Run("named pipe", func(t *testing.T) {
		image := checksumImage(t)
		if err := syscall.Mknod(image+".sha256", syscall.S_IFIFO|0o644, 0); err != nil {
			t.Fatalf("mkfifo: %v", err)
		}
		wantNonRegularSidecar(t, image)
	})
	t.Run("symlink to named pipe", func(t *testing.T) {
		image := checksumImage(t)
		fifo := filepath.Join(t.TempDir(), "pipe")
		if err := syscall.Mknod(fifo, syscall.S_IFIFO|0o644, 0); err != nil {
			t.Fatalf("mkfifo: %v", err)
		}
		if err := os.Symlink(fifo, image+".sha256"); err != nil {
			t.Fatal(err)
		}
		wantNonRegularSidecar(t, image)
	})
}

func checksumImage(t *testing.T) string {
	t.Helper()
	image := filepath.Join(t.TempDir(), "app.sqsh")
	if err := os.WriteFile(image, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return image
}

// wantNonRegularSidecar fails the test if the read blocks. Opening a
// FIFO waits for the other end, so a type check must reject it first.
func wantNonRegularSidecar(t *testing.T, image string) {
	t.Helper()
	errc := make(chan error, 1)
	go func() {
		_, err := ReadChecksumSidecar(image)
		errc <- err
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrChecksumSidecar) {
			t.Fatalf("non-regular sidecar: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("non-regular sidecar blocked")
	}
}
