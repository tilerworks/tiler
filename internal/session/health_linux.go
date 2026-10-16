//go:build linux

package session

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/mountinfo"
)

// healthy is the real two-condition probe: the point is listed in
// the table or stats as a mount, and it still answers reads. A
// killed FUSE daemon can leave the mount attached with the kernel
// attribute cache still answering stat, so only the read probe
// distinguishes the listed-but-dead shape.
func healthy(table []mountinfo.Mount, point string) (bool, error) {
	return classify(listed(table, point), liveMount(point), answers(point)), nil
}

// liveMount reports whether point stats as a mount: the stat
// succeeds and the device differs from the parent directory's,
// which is how a FUSE mount shows up once its daemon attached.
func liveMount(point string) bool {
	dev, err := deviceOf(point)
	if err != nil {
		return false
	}
	parentDev, err := deviceOf(filepath.Dir(point))
	if err != nil {
		return false
	}
	return dev != parentDev
}

// stillAttached reports whether a point still carries a mount that
// must come down: it stats as a mount, or the stat itself fails
// with a dead FUSE connection, the shape a killed daemon leaves.
func stillAttached(point string) bool {
	if liveMount(point) {
		return true
	}
	_, err := os.Stat(point)
	return errors.Is(err, syscall.ENOTCONN)
}

// deviceOf reports the device number of one path.
func deviceOf(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("session: cannot read the device number")
	}
	return st.Dev, nil
}

// runtimeNameOf derives the runtime directory name of a point from
// its device and inode — the reverse of the name the mount engine
// gave the combination at mount time. A point that cannot be
// statted has no derivable name.
func runtimeNameOf(point string) (string, bool) {
	info, err := os.Stat(point)
	if err != nil {
		return "", false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return config.RuntimeDirName(st.Dev, st.Ino), true
}

// answers reports whether the connection behind point still serves
// requests. Opening the point forces a request that a dead FUSE
// connection refuses with ENOTCONN; a vanished point answers
// nothing. Any other refusal, including a permission error, means
// something is still serving the mount.
func answers(point string) bool {
	file, err := os.Open(point)
	if err == nil {
		_ = file.Close()
		return true
	}
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	return !errors.Is(err, syscall.ENOTCONN)
}
