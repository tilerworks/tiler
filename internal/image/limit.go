package image

import (
	"fmt"
	"strings"
)

// sizeLimitMarkers are tool output texts that mean the target
// storage refused a large single file. They mirror the errno names
// of the single-file size limit across common toolchains.
var sizeLimitMarkers = []string{
	"file too large",
	"value too large",
	"result too large",
}

// toolOutputHitsSizeLimit reports whether the combined output of a
// failed external tool carries a single-file size limit marker. It
// is a pure string check, so every platform classifies the same
// text.
func toolOutputHitsSizeLimit(out string) bool {
	text := strings.ToLower(out)
	for _, marker := range sizeLimitMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// sizeLimitError names the failed write, explains that the target
// storage refused a file this large, and guides to the split_size
// surface or the doctor probe. It never cites external documents.
func sizeLimitError(what string, err error) error {
	return fmt.Errorf("build: %s: %w: the target storage refused a file this large; "+
		"set split_size (or build --split <size>) to store as byte blocks and retry, "+
		"or check the doctor single-file size limit probe", what, err)
}

// failWrite wraps a Go-side write failure. Only the single-file size
// limit earns the split guidance; every other failure keeps its
// plain meaning and still names the write.
func failWrite(what string, err error) error {
	if isSizeLimitWrite(err) {
		return sizeLimitError(what, err)
	}
	return fmt.Errorf("build: %s: %w", what, err)
}

// failTool wraps an external tool failure classified by its output
// text. Unclassified failures pass through unchanged.
func failTool(what string, err error) error {
	if toolOutputHitsSizeLimit(err.Error()) {
		return sizeLimitError(what, err)
	}
	return err
}
