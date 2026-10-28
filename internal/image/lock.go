package image

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jiangxin/tiler/internal/filelock"
)

// DefaultLibraryLockTimeout bounds normal library and reference coordination.
const DefaultLibraryLockTimeout = 30 * time.Second

// LockLibrary serializes version and reference changes across the image library.
// Acquire this before repository locks, then session locks in name order. The
// lock lives outside repositories and must never be unlinked during deletion.
// Nonpositive timeouts make one attempt; callers must release a successful lock.
func LockLibrary(imagesDir string, timeout time.Duration) (func(), error) {
	if strings.TrimSpace(imagesDir) == "" {
		return nil, fmt.Errorf("image library lock: %w", ErrInvalidPath)
	}
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(imagesDir, ".library.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := filelock.Acquire(file, timeout); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("image library lock: %w", err)
	}
	return func() {
		_ = filelock.Release(file)
		_ = file.Close()
	}, nil
}
