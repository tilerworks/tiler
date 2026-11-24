//go:build linux || darwin

package checkpoint

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// probeClone creates private ordinary files inside the unpublished destination.
// Only verified objects are removed, and cleanup failures abort the copy.
func probeClone(dir, original *os.File, clone func(*os.File, *os.File, string) error) (unsupported bool, retErr error) {
	var sourceStat, destStat unix.Stat_t
	if err := unix.Fstat(int(original.Fd()), &sourceStat); err != nil {
		return false, nil
	}
	if err := unix.Fstat(int(dir.Fd()), &destStat); err != nil || sourceStat.Dev != destStat.Dev {
		return false, nil
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return false, nil
	}
	prefix := ".tiler-checkpoint-clone-probe-" + hex.EncodeToString(random[:])
	source, err := openProbeFile(dir, prefix+"-src")
	if err != nil {
		return false, nil
	}
	defer func() {
		if err := removeProbeFile(dir, prefix+"-src", source); err != nil {
			retErr = errors.Join(retErr, err)
		}
		_ = source.Close()
	}()
	// A nonempty file exercises data cloning instead of an empty-file shortcut.
	if _, err = source.Write([]byte("clone probe")); err != nil {
		return false, nil
	}
	return probeCloneDestination(dir, source, prefix+"-dst", clone)
}

func openProbeFile(dir *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func removeProbeFile(dir *os.File, name string, file *os.File) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &st); err != nil {
		return err
	}
	if err := verifyNamed(dir, name, &st); err != nil {
		return fmt.Errorf("clone probe identity: %w", err)
	}
	if err := unlinkAt(dir, name, 0); err != nil {
		return fmt.Errorf("remove clone probe: %w", err)
	}
	return nil
}
