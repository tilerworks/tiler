package image

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// PrepareImageLocked finishes this image's pending journals. The caller holds
// that image's lock. Version removal runs first so an import cannot
// interleave with it. A leftover library-root journal whose name matches is
// claimed before replay. Source libraries are not opened. Garbage collection
// must not call this: it displays a pending removal journal instead of
// replaying it. Reads do not call it.
func PrepareImageLocked(imagesDir, name string) error {
	if strings.TrimSpace(imagesDir) == "" {
		return fmt.Errorf("image library: %w", ErrInvalidPath)
	}
	if err := claimLegacyJournals(imagesDir, name); err != nil {
		return err
	}
	if err := recoverVersionRemoval(imagesDir, name, hooks{}); err != nil {
		return err
	}
	return finishPreparedImport(imagesDir, name)
}

// PrepareImagesLocked finishes journals for names the caller already locked.
func PrepareImagesLocked(imagesDir string, names []string) error {
	for _, name := range uniqueNames(names) {
		if err := PrepareImageLocked(imagesDir, name); err != nil {
			return err
		}
	}
	return nil
}

// finishPreparedImport is the import half of target preparation. Tests
// replace it to keep a cleanup failure pending.
var finishPreparedImport = func(imagesDir, name string) error {
	return recoverImportTxn(imagesDir, name, hooks{})
}

// WithPreparedTarget reads an image library without locking it and without
// replaying journals. A missing library is not created. Writers recover an
// image's journals under that image's lock.
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
	return read()
}

func importTxnPending(imagesDir, name string) error {
	pending, err := journalExists(imagesDir, name, importTxnJournal)
	if err != nil || !pending {
		return err
	}
	return errors.New("version removal: import recovery is pending")
}

func journalExists(imagesDir, name, suffix string) (bool, error) {
	path, err := controlFile(imagesDir, name, suffix)
	if err != nil {
		return false, err
	}
	_, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
