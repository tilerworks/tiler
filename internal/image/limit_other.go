//go:build !linux

package image

// isSizeLimitWrite cannot inspect errno values on this platform, so
// a write failure keeps its plain meaning and never carries the
// split guidance.
func isSizeLimitWrite(error) bool { return false }
