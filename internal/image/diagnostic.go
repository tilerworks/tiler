package image

import (
	"fmt"
	"os"
)

// MissingManifestError distinguishes an absent repository from missing metadata
// in an existing repository. It retains filesystem error matching for callers.
type MissingManifestError struct {
	Name             string
	Library          string
	Path             string
	RepositoryExists bool
}

func (e *MissingManifestError) Error() string {
	if e.RepositoryExists {
		return fmt.Sprintf("image %q: metadata is missing at %q; restore the image repository before retrying", e.Name, e.Path)
	}
	return fmt.Sprintf("image %q was not found in library %q\n  Hint: run 'tiler images list' to see available images", e.Name, e.Library)
}

func (e *MissingManifestError) Unwrap() error { return os.ErrNotExist }

// TerminalDiagnostic preserves the hint layout; names and paths use %q.
func (e *MissingManifestError) TerminalDiagnostic() string { return e.Error() }
