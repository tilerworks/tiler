package checkpoint

import (
	"errors"
	"fmt"
	"io"
	"os"
)

var errCloneUnsupported = errors.New("file clone unsupported")

// copyFileData copies between opened regular files. The caller must create an
// empty, private destination and separately preserve metadata and sync it.
func copyFileData(dst, src *os.File, clone func(*os.File, *os.File) error) error {
	if err := clone(dst, src); err == nil {
		return nil
	} else if !errors.Is(err, errCloneUnsupported) {
		return fmt.Errorf("clone file: %w", err)
	}
	if err := dst.Truncate(0); err != nil {
		return fmt.Errorf("reset destination: %w", err)
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek destination: %w", err)
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek source: %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("copy file: %w", err)
	}
	return nil
}
