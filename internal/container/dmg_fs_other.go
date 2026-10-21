//go:build !darwin

package container

// hostDMGFilesystem keeps APFS. Non-macOS hosts do not attach
// disk images, and Linux tests must not require hdiutil.
func hostDMGFilesystem() string { return "APFS" }
