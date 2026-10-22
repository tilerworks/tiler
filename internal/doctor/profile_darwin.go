//go:build darwin

package doctor

import "github.com/jiangxin/tiler/internal/host"

// applyHostProfile selects the macOS probe set and the system
// hdiutil stat. The stat does not execute the binary.
func applyHostProfile(seams *Seams) {
	seams.Profile = profileDarwin
	seams.Hdiutil = host.SystemHdiutil
}
