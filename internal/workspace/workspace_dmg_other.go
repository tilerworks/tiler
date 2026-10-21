//go:build !darwin

package workspace

import (
	"errors"
	"fmt"
)

// execHdiutil reports that the real disk-image tool is macOS-only.
// Callers on other hosts pass a fake runner. This function does not
// look up or execute hdiutil.
func execHdiutil(argv []string, consErr error) ([]byte, error) {
	if consErr != nil {
		return nil, consErr
	}
	if len(argv) == 0 {
		return nil, errors.New("workspace: hdiutil argv is empty")
	}
	return nil, fmt.Errorf("%w: hdiutil", ErrUnavailable)
}
