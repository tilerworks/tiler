//go:build linux || darwin

package checkpoint

import (
	"errors"
	"io"
	"os"
	"syscall"

	"github.com/jiangxin/tiler/internal/durable"
	"golang.org/x/sys/unix"
)

// exchangeMarkerOperation allows interruption injection around durable boundaries.
var exchangeMarkerOperation = func(_ string, operation func() error) error { return operation() }

func readExchangeMarker(dir *os.File, target string) (exchangeMarker, error) {
	fd, err := unix.Openat(int(dir.Fd()), MarkerName, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return exchangeMarker{}, err
	}
	f := os.NewFile(uintptr(fd), MarkerName)
	defer func() { _ = f.Close() }()
	return readExchangeMarkerFile(f, target)
}

func readExchangeMarkerFile(f *os.File, target string) (exchangeMarker, error) {
	info, err := f.Stat()
	if err != nil {
		return exchangeMarker{}, err
	}
	if !info.Mode().IsRegular() {
		return exchangeMarker{}, errors.New("checkpoint marker is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return exchangeMarker{}, err
	}
	if len(data) > 4096 {
		return exchangeMarker{}, errors.New("checkpoint marker too large")
	}
	return parseExchangeMarker(data, target)
}

func removeExchangeMarker(dir *os.File, marker exchangeMarker) error {
	fd, err := unix.Openat(int(dir.Fd()), MarkerName, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		return exchangeMarkerOperation("marker:sync-dir", func() error { return durable.Sync(dir) })
	}
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), MarkerName)
	defer func() { _ = f.Close() }()
	existing, err := readExchangeMarkerFile(f, marker.Target)
	if err != nil {
		return err
	}
	if !existing.CreatedAt.Equal(marker.CreatedAt) {
		return errors.New("checkpoint marker differs")
	}
	var opened unix.Stat_t
	if err = unix.Fstat(fd, &opened); err != nil {
		return err
	}
	if err = exchangeMarkerOperation("marker:unlink", func() error {
		var current unix.Stat_t
		if e := unix.Fstatat(int(dir.Fd()), MarkerName, &current, unix.AT_SYMLINK_NOFOLLOW); e != nil {
			return e
		}
		if opened.Dev != current.Dev || opened.Ino != current.Ino {
			return errors.New("checkpoint marker changed")
		}
		return unlinkAt(dir, MarkerName, 0)
	}); err != nil {
		return err
	}
	return exchangeMarkerOperation("marker:sync-dir", func() error { return durable.Sync(dir) })
}
