//go:build !linux

package doctor

// isSizeLimitErr cannot inspect errno values on this platform, so
// limit detection degrades: a refused truncate surfaces as a warn
// carrying the error text instead of a measured limit.
func isSizeLimitErr(error) bool { return false }
