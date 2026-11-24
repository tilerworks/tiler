//go:build linux || darwin

package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/jiangxin/tiler/internal/durable"
	"golang.org/x/sys/unix"
)

// createExchangeStage creates an exclusive sibling file. The caller must
// persist its identity before using it, and must preserve an unrecorded file
// after an interruption between creation and recording.
func createExchangeStage(p *Parent, name string, data []byte) (restoreIdentity, error) {
	var empty restoreIdentity
	if err := safeName(name); err != nil {
		return empty, err
	}
	if err := p.Verify(); err != nil {
		return empty, err
	}
	fd, err := unix.Openat(int(p.File.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return empty, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	id, err := identity(f)
	if err != nil {
		return empty, err
	}
	if _, err = f.Write(data); err != nil {
		return empty, err
	}
	if err = durable.Sync(f); err != nil {
		return empty, err
	}
	if err = durable.Sync(p.File); err != nil {
		return empty, err
	}
	return id, nil
}

func openExchangeStage(dir *os.File, name string, expected restoreIdentity) (*os.File, error) {
	if err := safeName(name); err != nil {
		return nil, err
	}
	if expected.Inode == 0 {
		return nil, errors.New("unrecorded exchange stage requires manual inspection")
	}
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("exchange stage is not a regular file")
	}
	if err == nil {
		err = requireIdentity(f, expected)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// linkExchangeStage publishes the recorded stage without replacing an entry.
// The caller must hold the transaction lock and persist the stage identity
// before calling this function, including for a disposable write probe.
func linkExchangeStage(p *Parent, stage string, dest *os.File, name string, expected restoreIdentity) error {
	if err := safeName(name); err != nil {
		return err
	}
	if existing, err := openExchangeStage(dest, name, expected); err == nil {
		_ = existing.Close()
		return durable.Sync(dest)
	} else if !errors.Is(err, syscall.ENOENT) {
		return err
	}
	f, err := openExchangeStage(p.File, stage, expected)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err = p.Verify(); err != nil {
		return err
	}
	var source unix.Stat_t
	if err = unix.Fstatat(int(p.File.Fd()), stage, &source, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if uint64(source.Dev) != expected.Device || source.Ino != expected.Inode {
		return errors.New("exchange stage changed")
	}
	// Publish with the platform's exclusive operation, then verify the inode.
	if err = publishExchangeStage(p, stage, f, dest, name); err != nil {
		if !errors.Is(err, syscall.EEXIST) {
			return err
		}
	}
	linked, err := openExchangeStage(dest, name, expected)
	if err != nil {
		return fmt.Errorf("published exchange stage: %w", err)
	}
	_ = linked.Close()
	return durable.Sync(dest)
}

// removeExchangeStage removes only the recorded inode, never an unknown entry.
func removeExchangeStage(dir *os.File, name string, expected restoreIdentity) error {
	f, err := openExchangeStage(dir, name, expected)
	if errors.Is(err, syscall.ENOENT) {
		return durable.Sync(dir)
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	var st unix.Stat_t
	if err = unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if uint64(st.Dev) != expected.Device || st.Ino != expected.Inode {
		return errors.New("exchange stage changed")
	}
	if err = unlinkAt(dir, name, 0); err != nil {
		return err
	}
	return durable.Sync(dir)
}
