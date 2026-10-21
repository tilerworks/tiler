package container

import (
	"errors"
	"io"
	"os"
)

const (
	dmgType = "dmg"
	dmgComp = "ulfo"
	// dmgTrailerLen is the UDIF trailer. Its first four bytes are
	// the koly magic, and the trailer is the last 512 bytes of the
	// file.
	dmgTrailerLen = 512
)

var kolyMagic = [4]byte{'k', 'o', 'l', 'y'}

// errDmgNoIncr means a caller asked to pack a stackable layer.
// A dmg has no incremental file to append.
var errDmgNoIncr = errors.New("container: dmg has no stackable incremental layer")

// dmg is the UDIF disk-image Format. The zero value is ready to
// use. Registration lives in a darwin init, so a Linux process
// keeps the squashfs and erofs registry.
type dmg struct{}

var _ Format = dmg{}

func (dmg) Type() string { return dmgType }

func (dmg) DefaultCompression() string { return dmgComp }

// AllowsCompression accepts ulfo, ulmo, and udro. An empty name
// means the format default.
func (dmg) AllowsCompression(compression string) bool {
	switch compression {
	case "", "ulfo", "ulmo", "udro":
		return true
	default:
		return false
	}
}

func (dmg) Tools() []string {
	return []string{"hdiutil"}
}

// MountArgv is empty. A dmg mount does not use a FUSE argument
// vector.
func (dmg) MountArgv(string, string, string, string, string) []string {
	return nil
}

// PackFull is not the dmg publish path. The image is created by
// hdiutil outside this method.
func (dmg) PackFull(ToolRunner, string, string, string) error {
	return errors.New("container: dmg is not packed through PackFull")
}

func (dmg) PackIncr(ToolRunner, string, string, string) error {
	return errDmgNoIncr
}

// HasSubdir reports the image root as present. Any other subdir
// is rejected: a dmg has no subdir member.
func (dmg) HasSubdir(_ ToolRunner, _, subdir string) (bool, error) {
	if subdir == "" {
		return true, nil
	}
	return false, errors.New("container: dmg has no subdir")
}

// Detect reports whether path ends in a 512-byte trailer that
// starts with koly. A short file or a read error is not a dmg.
func (dmg) Detect(path string) (bool, error) {
	return kolyTrailer(path), nil
}

// kolyTrailer reads the UDIF magic. It has no build tag, so Linux
// tests can feed bytes without a darwin registry.
func kolyTrailer(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return false
	}
	size := info.Size()
	if size < dmgTrailerLen {
		return false
	}
	if _, err := file.Seek(size-dmgTrailerLen, io.SeekStart); err != nil {
		return false
	}
	var magic [4]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil {
		return false
	}
	return magic == kolyMagic
}
