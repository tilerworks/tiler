//go:build darwin

package container

import (
	"os"
	"path/filepath"
	"sync"
)

var (
	dmgFSOnce sync.Once
	dmgFS     = "APFS"
)

// hostDMGFilesystem probes attach -shadow against a throwaway APFS
// image once per process. A failed create keeps APFS, because the
// tool could not answer. A failed attach means this OS cannot use
// a shadow on APFS, so every new image uses HFS+.
func hostDMGFilesystem() string {
	dmgFSOnce.Do(func() {
		if apfsShadowUnsupported() {
			dmgFS = "HFS+"
		}
	})
	return dmgFS
}

func apfsShadowUnsupported() bool {
	dir, err := os.MkdirTemp("", "tiler-dmg-fs-*")
	if err != nil {
		return false
	}
	defer func() { _ = os.RemoveAll(dir) }()
	image, mnt, shadow := dmgProbePaths(dir)
	if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
		return false
	}
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return false
	}
	if err := runHdiutilProbe(dmgProbeCreateArgv(image)); err != nil {
		return false
	}
	return dmgProbeAttach(image, mnt, shadow)
}

func dmgProbePaths(dir string) (image, mnt, shadow string) {
	return filepath.Join(dir, "probe.dmg"),
		filepath.Join(dir, "mnt"),
		filepath.Join(dir, "sess", "runtime", "m1", "upper.shadow")
}

// dmgProbeCreateArgv uses a literal APFS filesystem. Calling
// HdiutilCreateArgv here would ask for the decision this probe is
// still computing.
func dmgProbeCreateArgv(image string) []string {
	return []string{
		hdiutilName, "create",
		"-size", "1m",
		"-fs", "APFS",
		"-volname", "probe",
		image,
	}
}

// dmgProbeAttach reports whether the shadow attach failed. A
// successful attach is detached before the temp directory is removed.
func dmgProbeAttach(image, mnt, shadow string) bool {
	attach, err := HdiutilAttachWritableArgv(image, mnt, shadow)
	if err != nil {
		return false
	}
	if err := runHdiutilProbe(attach); err != nil {
		return true
	}
	detach, err := HdiutilDetachArgv(mnt)
	if err == nil {
		_ = runHdiutilProbe(detach)
	}
	return false
}

func runHdiutilProbe(argv []string) error {
	cmd, err := NewHdiutilCommand(argv, nil)
	if err != nil {
		return err
	}
	_, err = cmd.CombinedOutput()
	return err
}
