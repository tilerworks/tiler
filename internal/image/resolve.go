package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotImageFile reports a direct path that is not a regular image file.
var ErrNotImageFile = errors.New("not an image file")

// ErrUnknownImage reports an image selector with no matching version.
var ErrUnknownImage = errors.New("unknown image")

// Target is one resolved immutable version stack or direct image file.
type Target struct {
	Library    bool
	Name       string
	ID         string
	Tip        string
	Format     string
	Members    []Member
	Form       string
	Size       int64
	SHA256     string
	BlocksDir  string
	ConcatView string
	Blocks     []BlockFile
}

// BlockFile is one manifest block paired with its absolute object path.
type BlockFile struct {
	Path      string
	Name      string
	Size      int64
	MtimeUnix int64
	SHA256    string
}

// Member is one immutable container in root-to-tip mount order.
type Member struct {
	Path      string
	ID        string
	Format    string
	Size      int64
	MtimeUnix int64
	SHA256    string
	Blocks    []BlockFile
}

func resolveFile(arg string) (Target, error) {
	abs, err := filepath.Abs(arg)
	if err != nil {
		return Target{}, fmt.Errorf("mount: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Target{}, fileFormError(arg, err)
	}
	if !info.Mode().IsRegular() {
		return Target{}, fileFormError(arg, fmt.Errorf("%s is not a regular file", abs))
	}
	return Target{Members: []Member{{Path: abs}}}, nil
}

func fileFormError(arg string, err error) error {
	base := fmt.Errorf("mount: image file %q: %w: %w", arg, ErrNotImageFile, err)
	if !strings.Contains(arg, "/") || filepath.IsAbs(arg) {
		return base
	}
	_, nameErr := ParseName(arg)
	return fmt.Errorf("%w; as a library reference: %w", base, nameErr)
}
