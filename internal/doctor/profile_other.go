//go:build !darwin

package doctor

// applyHostProfile leaves the Linux FUSE probe set in place.
func applyHostProfile(*Seams) {}
