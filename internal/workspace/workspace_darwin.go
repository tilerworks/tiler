//go:build darwin

package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/jiangxin/tiler/internal/container"
)

// available reports that this host can mount disk images. hdiutil
// does not need /dev/fuse. A FUSE request still fails later when
// its tools are missing.
func available() error {
	return nil
}

// devIno reports the device and inode of a directory.
func devIno(dir string) (uint64, uint64, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return 0, 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, errors.New("workspace: cannot read device and inode")
	}
	return uint64(st.Dev), st.Ino, nil
}

// liveMount reports whether point currently behaves as a mount:
// stat succeeds and the device differs from the parent directory.
func liveMount(point string) bool {
	dev, _, err := devIno(point)
	if err != nil {
		return false
	}
	parentDev, _, err := devIno(filepath.Dir(point))
	if err != nil {
		return false
	}
	return dev != parentDev
}

// answers reports whether point still serves requests. Opening it
// forces a request that a dead connection refuses with ENOTCONN.
func answers(point string) bool {
	file, err := os.Open(point)
	if err == nil {
		_ = file.Close()
		return true
	}
	return !errors.Is(err, syscall.ENOTCONN)
}

// fileUID is the owner of an already-statted file.
func fileUID(info os.FileInfo) uint32 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Uid
	}
	return uint32(os.Geteuid())
}

// newHdiutil builds an unstarted command for /usr/bin/hdiutil.
func newHdiutil(argv []string, consErr error) (*exec.Cmd, error) {
	return container.NewHdiutilCommand(argv, consErr)
}

// ReadHdiutilInfo runs hdiutil info -plist and returns stdout.
// Callers parse the document with container.ParseHdiutilInfo.
func ReadHdiutilInfo() ([]byte, error) {
	return execHdiutil(container.HdiutilInfoArgv(), nil)
}

// execHdiutil runs /usr/bin/hdiutil. A constructor error is returned
// without starting the process.
func execHdiutil(argv []string, consErr error) ([]byte, error) {
	cmd, err := newHdiutil(argv, consErr)
	if err != nil {
		return nil, err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("hdiutil: %w: %s", err, bytes.TrimSpace(out))
	}
	return out, nil
}
