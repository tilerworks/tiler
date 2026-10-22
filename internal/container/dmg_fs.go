package container

// dmgCreateFilesystem is the -fs value for every new image. The
// default is APFS so tests do not attach a volume. The process
// entry enables the host probe. Tests replace the function. The
// choice is not a configuration key.
var dmgCreateFilesystem = func() string { return "APFS" }

// DMGCreateFilesystem is APFS, or HFS+ when attach -shadow cannot
// use an APFS image. Any other decision stays APFS.
func DMGCreateFilesystem() string {
	if dmgCreateFilesystem() == "HFS+" {
		return "HFS+"
	}
	return "APFS"
}

// SetDMGCreateFilesystem installs the filesystem decision and
// returns a restore function. Tests use it so they do not attach a
// volume. A nil decision selects APFS.
func SetDMGCreateFilesystem(decide func() string) (restore func()) {
	prev := dmgCreateFilesystem
	if decide == nil {
		decide = func() string { return "APFS" }
	}
	dmgCreateFilesystem = decide
	return func() { dmgCreateFilesystem = prev }
}

// EnableHostDMGFilesystemProbe asks the host which filesystem every
// new image should use. The tiler process calls it at startup.
// Unit tests do not, and they do not execute hdiutil.
func EnableHostDMGFilesystemProbe() {
	dmgCreateFilesystem = hostDMGFilesystem
}
