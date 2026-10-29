package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PrepareTargetLocked finishes pending target journals before a read, mount,
// replay, or mutation. The caller holds the library lock. Version removal
// runs first so an import cannot interleave with it. Source libraries are
// not opened. Garbage collection must not call this: it displays a pending
// removal journal instead of replaying it.
func PrepareTargetLocked(imagesDir string) error {
	if strings.TrimSpace(imagesDir) == "" {
		return fmt.Errorf("image library: %w", ErrInvalidPath)
	}
	if err := recoverVersionRemoval(imagesDir, hooks{}); err != nil {
		return err
	}
	return finishPreparedImport(imagesDir)
}

// finishPreparedImport is the import half of target preparation. Tests
// replace it to keep a cleanup failure pending across a reader.
var finishPreparedImport = RecoverImportTxn

// WithPreparedTarget locks the target library, finishes its journals, and
// runs read while that lock is still held. Callers that already hold the
// lock must use PrepareTargetLocked instead. A missing library has nothing
// to recover and is not created.
func WithPreparedTarget(imagesDir string, read func() error) error {
	if read == nil {
		return errors.New("image library: missing read")
	}
	if strings.TrimSpace(imagesDir) == "" {
		return read()
	}
	if _, err := os.Stat(imagesDir); errors.Is(err, os.ErrNotExist) {
		return read()
	} else if err != nil {
		return err
	}
	release, err := LockLibrary(imagesDir, DefaultLibraryLockTimeout)
	if err != nil {
		return err
	}
	defer release()
	if err := PrepareTargetLocked(imagesDir); err != nil {
		return err
	}
	return read()
}

func importTxnPending(imagesDir string) error {
	_, err := os.Lstat(filepath.Join(imagesDir, importTxnJournal))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("version removal: import recovery is pending")
}
