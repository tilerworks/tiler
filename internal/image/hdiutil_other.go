//go:build !darwin

package image

import "fmt"

// defaultDiskTool reports that the real disk-image tool is
// macOS-only. It does not look up or execute hdiutil. Tests pass a
// fake runner instead.
func defaultDiskTool(string, []string, ...string) error {
	return fmt.Errorf("build: hdiutil is macOS-only")
}
