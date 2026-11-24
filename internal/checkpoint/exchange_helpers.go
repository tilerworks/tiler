//go:build linux || darwin

package checkpoint

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func openMaybe(p *Parent, name string) (*os.File, error) {
	f, err := openEntry(p.File, name, syscall.O_RDONLY|syscall.O_DIRECTORY)
	if errors.Is(err, syscall.ENOENT) {
		return nil, nil
	}
	return f, err
}

func matches(f *os.File, w restoreIdentity) bool { return f != nil && requireIdentity(f, w) == nil }
func readMarker(dir *os.File, target string) (Marker, error) {
	fd, err := unix.Openat(int(dir.Fd()), MarkerName, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return Marker{}, err
	}
	f := os.NewFile(uintptr(fd), MarkerName)
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return Marker{}, err
	}
	if !st.Mode().IsRegular() {
		return Marker{}, errors.New("checkpoint marker is not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return Marker{}, err
	}
	if len(b) > 4096 {
		return Marker{}, errors.New("checkpoint marker too large")
	}
	return ParseMarker(b, target)
}

func rejectCheckpointSource(target string) error {
	for path := target; path != "/"; path = filepath.Dir(path) {
		if !strings.HasSuffix(path, ".checkpoint") {
			continue
		}
		dir, err := openDirectory(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		_, markerErr := readMarker(dir, strings.TrimSuffix(path, ".checkpoint"))
		_ = dir.Close()
		if markerErr == nil {
			return fmt.Errorf("source is inside existing checkpoint %s", path)
		}
		dir, err = openDirectory(path)
		if err != nil {
			return err
		}
		_, markerErr = readExchangeMarker(dir, strings.TrimSuffix(path, ".checkpoint"))
		_ = dir.Close()
		if markerErr == nil {
			return fmt.Errorf("source is inside existing checkpoint %s", path)
		}
	}
	return nil
}
