//go:build darwin

package image

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/jiangxin/tiler/internal/container"
)

// defaultDiskTool runs /usr/bin/hdiutil. Tests pass their own runner
// and do not call this function.
func defaultDiskTool(name string, env []string, args ...string) error {
	if name != "hdiutil" {
		return fmt.Errorf("build: unexpected tool %s", name)
	}
	args, cleanup, err := convertFromCopies(args)
	if err != nil {
		return err
	}
	defer cleanup()
	return runTool(container.HdiutilPath, env, args...)
}

// convertFromCopies rewrites convert -shadow to read temporary
// copies. hdiutil refuses that command when the shadow is attached
// and reports that the file already exists. The live image and the
// runtime shadow stay where they are.
func convertFromCopies(args []string) ([]string, func(), error) {
	noop := func() {}
	imagePath, shadow, ok := convertShadowArgs(args)
	if !ok {
		return args, noop, nil
	}
	dir, err := os.MkdirTemp("", "tiler-dmg-bake-*")
	if err != nil {
		return nil, noop, fmt.Errorf("build: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	// Recent writes sit in the disk-image cache until the mounted
	// volume is synced or the image is detached. Copying the shadow
	// before that sync publishes an image that dropped them, while
	// detach later still stores them in the live shadow. Detach is
	// not used, and the original shadow is not renamed or removed.
	if err := flushShadowWrites(shadow); err != nil {
		cleanup()
		return nil, noop, err
	}
	imageCopy := filepath.Join(dir, "image.dmg")
	shadowCopy := filepath.Join(dir, "upper.shadow")
	if err := copyDiskFile(imagePath, imageCopy); err != nil {
		cleanup()
		return nil, noop, err
	}
	if err := copyDiskFile(shadow, shadowCopy); err != nil {
		cleanup()
		return nil, noop, err
	}
	rewritten := append([]string(nil), args...)
	rewritten[1] = imageCopy
	for i := 0; i < len(rewritten)-1; i++ {
		if rewritten[i] == "-shadow" {
			rewritten[i+1] = shadowCopy
			break
		}
	}
	return rewritten, cleanup, nil
}

func convertShadowArgs(args []string) (imagePath, shadow string, ok bool) {
	if len(args) < 2 || args[0] != "convert" {
		return "", "", false
	}
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-shadow" {
			return args[1], args[i+1], true
		}
	}
	return "", "", false
}

// flushShadowWrites pushes the mounted volume's cache into the
// shadow file. Syncing that file alone does not: the bytes are not
// in it yet. The following sync of the shadow waits until those
// bytes are durable, so the copy reads them.
func flushShadowWrites(shadow string) error {
	if err := syscall.Sync(); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	if err := syncFile(shadow); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	return nil
}

func copyDiskFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return fmt.Errorf("build: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("build: %w", closeErr)
	}
	return nil
}
