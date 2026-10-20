package container

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

const (
	squashfsType = "squashfs"
	squashfsComp = "zstd"
)

// Host-endian superblock magic at offset 0: little-endian "hsqs",
// big-endian "sqsh".
var (
	magicHSQS = [4]byte{'h', 's', 'q', 's'}
	magicSQSH = [4]byte{'s', 'q', 's', 'h'}
)

// errSquashfsRunner means a pack or subdir probe was called without
// a ToolRunner. This package never starts a process itself.
var errSquashfsRunner = errors.New("container: squashfs tool runner is nil")

// squashfs is the squashfs Format. The zero value is ready to use.
type squashfs struct{}

// Compile-time check that the zero value implements Format.
var _ Format = squashfs{}

func init() {
	Register(squashfs{})
}

func (squashfs) Type() string { return squashfsType }

func (squashfs) DefaultCompression() string { return squashfsComp }

// AllowsCompression accepts the mksquashfs compressors this build
// exposes. An empty name means the format default.
func (squashfs) AllowsCompression(compression string) bool {
	switch compression {
	case "", "zstd", "gzip", "lz4", "xz":
		return true
	default:
		return false
	}
}

func (s squashfs) PackFull(run ToolRunner, src, dst, compression string) error {
	return s.pack(run, src, dst, compression, false)
}

func (s squashfs) PackIncr(run ToolRunner, src, dst, compression string) error {
	return s.pack(run, src, dst, compression, true)
}

// pack runs mksquashfs. An incremental layer passes -xattrs so
// overlay whiteouts in the user namespace survive. The creation
// time is pinned so the same tree packs to the same bytes.
func (s squashfs) pack(run ToolRunner, src, dst, compression string, incr bool) error {
	comp, err := s.packCompression(compression)
	if err != nil {
		return err
	}
	if run == nil {
		return errSquashfsRunner
	}
	args := []string{src, dst, "-comp", comp, "-noappend"}
	if incr {
		args = append(args, "-xattrs")
	}
	return run("mksquashfs", sourceDateEnv(src), args...)
}

func (s squashfs) packCompression(compression string) (string, error) {
	if compression == "" {
		return s.DefaultCompression(), nil
	}
	if !s.AllowsCompression(compression) {
		return "", fmt.Errorf("container: squashfs compression %q is not allowed", compression)
	}
	return compression, nil
}

// MountArgv is the squashfuse_ll command line for one lower. fsname
// keeps the original image path in the mount table; subdir and the
// configured options follow the built-in fsname option.
func (squashfs) MountArgv(fsname, subdir, options, imagePath, point string) []string {
	opts := "fsname=" + fsname
	if subdir != "" {
		opts += ",subdir=" + subdir
	}
	if options != "" {
		opts += "," + options
	}
	return []string{"squashfuse_ll", "-o", opts, imagePath, point}
}

// HasSubdir asks unsquashfs whether subdir exists. -match makes the
// exit status the answer, so a non-zero status means the path is
// absent. An empty subdir is the image root and is always present.
func (squashfs) HasSubdir(run ToolRunner, imagePath, subdir string) (bool, error) {
	if subdir == "" {
		return true, nil
	}
	if run == nil {
		return false, errSquashfsRunner
	}
	err := run("unsquashfs", nil, "-ls", "-match", imagePath, subdir)
	if err != nil {
		return false, nil
	}
	return true, nil
}

// Tools are the packer and the FUSE client. unsquashfs is required
// only when a mount probes a subdir, so it is not part of the stack.
func (squashfs) Tools() []string {
	return []string{"mksquashfs", "squashfuse_ll"}
}

// Detect reads the four-byte superblock magic. A short file is not
// squashfs. An unreadable path is an error, not a mismatch.
func (squashfs) Detect(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()

	var magic [4]byte
	_, err = io.ReadFull(file, magic[:])
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return magic == magicHSQS || magic == magicSQSH, nil
}

// sourceDateEnv pins the image build time the packer embeds, so an
// unchanged tree packs to byte-identical images. A SOURCE_DATE_EPOCH
// already in the environment is honored; otherwise the epoch is the
// newest mtime in the tree about to be packed.
func sourceDateEnv(src string) []string {
	epoch := sourceDateEpoch(src)
	return []string{"SOURCE_DATE_EPOCH=" + strconv.FormatInt(epoch, 10)}
}

// sourceDateEpoch is the deterministic build timestamp of one source
// tree. Unreadable entries never abort packing; a tree with no
// readable mtime falls back to the zero epoch.
func sourceDateEpoch(src string) int64 {
	if value, ok := os.LookupEnv("SOURCE_DATE_EPOCH"); ok {
		if n, err := strconv.ParseInt(value, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	var newest int64
	_ = filepath.WalkDir(src, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		if m := info.ModTime().Unix(); m > newest {
			newest = m
		}
		return nil
	})
	return newest
}
