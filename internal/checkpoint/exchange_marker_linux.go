//go:build linux

package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/jiangxin/tiler/internal/durable"
	"golang.org/x/sys/unix"
)

// probeExchangeMarkerWrite verifies that the original directory can accept a
// marker before exchanging it. It does not guarantee a later write will work.
// errAnonymousMarkerUnsupported lets the lifecycle select a recoverable fallback.
var errAnonymousMarkerUnsupported = errors.New("anonymous marker files unsupported")

func openAnonymousMarker(dir *os.File) (int, error) {
	fd, err := unix.Openat(int(dir.Fd()), ".", unix.O_TMPFILE|unix.O_WRONLY|unix.O_CLOEXEC, 0600)
	if errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EISDIR) {
		return -1, fmt.Errorf("%w: %v", errAnonymousMarkerUnsupported, err)
	}
	return fd, err
}

func probeExchangeMarkerWrite(dir *os.File) error {
	var fd int
	err := exchangeMarkerOperation("probe:create", func() error {
		var e error
		fd, e = openAnonymousMarker(dir)
		return e
	})
	if err != nil {
		return fmt.Errorf("probe checkpoint marker write: %w", err)
	}
	f := os.NewFile(uintptr(fd), "checkpoint marker probe")
	defer func() { _ = f.Close() }()
	if err = exchangeMarkerOperation("probe:created", func() error { return nil }); err != nil {
		return err
	}
	return exchangeMarkerOperation("probe:sync", func() error { return durable.Sync(dir) })
}

// writeExchangeMarker publishes a complete, synced marker without replacement.
// A valid matching marker is accepted for a retry after interruption.
func writeExchangeMarker(dir *os.File, marker exchangeMarker) error {
	data, err := marshalExchangeMarker(marker)
	if err != nil {
		return err
	}
	var fd int
	err = exchangeMarkerOperation("marker:create", func() error {
		var e error
		fd, e = openAnonymousMarker(dir)
		return e
	})
	if err != nil {
		return fmt.Errorf("create anonymous checkpoint marker: %w", err)
	}
	f := os.NewFile(uintptr(fd), "checkpoint marker")
	defer func() { _ = f.Close() }()
	if err = exchangeMarkerOperation("marker:created", func() error { return nil }); err != nil {
		return err
	}
	err = exchangeMarkerOperation("marker:write", func() error {
		_, e := f.Write(data)
		return e
	})
	if err == nil {
		err = exchangeMarkerOperation("marker:sync-file", func() error { return durable.Sync(f) })
	}
	if err != nil {
		return err
	}
	err = exchangeMarkerOperation("marker:publish", func() error {
		e := unix.Linkat(fd, "", int(dir.Fd()), MarkerName, unix.AT_EMPTY_PATH)
		if errors.Is(e, syscall.ENOENT) || errors.Is(e, syscall.EPERM) {
			e = unix.Linkat(unix.AT_FDCWD, fmt.Sprintf("/proc/self/fd/%d", fd), int(dir.Fd()), MarkerName, unix.AT_SYMLINK_FOLLOW)
		}
		if errors.Is(e, syscall.ENOENT) || errors.Is(e, syscall.EPERM) || errors.Is(e, syscall.EOPNOTSUPP) || errors.Is(e, syscall.ENOSYS) {
			return fmt.Errorf("%w: publish: %v", errAnonymousMarkerUnsupported, e)
		}
		return e
	})
	if errors.Is(err, syscall.EEXIST) {
		fd, openErr := syscall.Openat(int(dir.Fd()), MarkerName, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		if openErr != nil {
			return openErr
		}
		existingFile := os.NewFile(uintptr(fd), MarkerName)
		existing, readErr := readExchangeMarkerFile(existingFile, marker.Target)
		if readErr == nil && !existing.CreatedAt.Equal(marker.CreatedAt) {
			readErr = errors.New("existing checkpoint marker differs")
		}
		if readErr == nil {
			readErr = exchangeMarkerOperation("marker:sync-file", func() error { return durable.Sync(existingFile) })
		}
		closeErr := existingFile.Close()
		if readErr != nil {
			return fmt.Errorf("existing checkpoint marker: %w", readErr)
		}
		if closeErr != nil {
			return closeErr
		}
	} else if err != nil {
		return err
	}
	return exchangeMarkerOperation("marker:sync-dir", func() error { return durable.Sync(dir) })
}
