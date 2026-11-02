package container

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jiangxin/tiler/internal/dirroot"
)

const (
	erofsType        = "erofs"
	erofsComp        = "zstd"
	erofsMagicOffset = 1024
	// erofsNilUUID pins the superblock UUID. mkfs.erofs 1.8 accepts
	// the word "clear" for this value; 1.7 (Ubuntu 24.04) rejects
	// that word and prints its usage. The nil UUID is the same pin
	// on both.
	erofsNilUUID = "00000000-0000-0000-0000-000000000000"
)

// On-disk superblock magic at byte 1024: the little-endian encoding
// of 0xE0F5E1E2.
var erofsMagic = [4]byte{0xE2, 0xE1, 0xF5, 0xE0}

// errErofsRunner means a pack or subdir probe was called without a
// ToolRunner. This package never starts a process itself.
var errErofsRunner = errors.New("container: erofs tool runner is nil")

// erofs is the erofs Format. The zero value is ready to use.
type erofs struct{}

// Compile-time check that the zero value implements Format.
var _ Format = erofs{}

func init() {
	Register(erofs{})
}

func (erofs) Type() string { return erofsType }

func (erofs) DefaultCompression() string { return erofsComp }

// ErofsCompressors reads compressor ids from mkfs.erofs --help.
// The line looks like "Available compressors are: lz4, deflate".
// An unrecognized help text returns nil.
func ErofsCompressors(help string) []string {
	const marker = "Available compressors are:"
	idx := strings.Index(help, marker)
	if idx < 0 {
		return nil
	}
	rest := help[idx+len(marker):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	return strings.FieldsFunc(rest, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
}

// ErofsToolSupports reports whether mkfs.erofs can pack compression.
// gzip is the metadata name for the deflate compressor. lz4hc does
// not satisfy lz4.
func ErofsToolSupports(compression string, compressors []string) bool {
	has := map[string]bool{}
	for _, name := range compressors {
		has[name] = true
	}
	switch compression {
	case "zstd":
		return has["zstd"]
	case "lz4":
		return has["lz4"]
	case "gzip":
		return has["deflate"] || has["libdeflate"]
	default:
		return false
	}
}

// PickErofsCompression chooses the compression recorded in metadata
// when the user did not set one. zstd stays first. lz4, then gzip,
// cover mkfs.erofs builds that omit libzstd, such as Ubuntu 24.04.
func PickErofsCompression(compressors []string) (string, error) {
	for _, compression := range []string{"zstd", "lz4", "gzip"} {
		if ErofsToolSupports(compression, compressors) {
			return compression, nil
		}
	}
	return "", errors.New("container: mkfs.erofs has no usable compressor")
}

// AllowsCompression accepts zstd, lz4, and gzip. An empty name means
// the format default. xz is not an erofs compressor here.
func (e erofs) AllowsCompression(compression string) bool {
	_, err := e.compressionFlag(compression)
	return err == nil
}

func (e erofs) PackFull(run ToolRunner, src, dst, compression string) error {
	return e.pack(run, src, dst, compression)
}

func (e erofs) PackIncr(run ToolRunner, src, dst, compression string) error {
	return e.pack(run, src, dst, compression)
}

// pack runs mkfs.erofs. Metadata compression gzip is the deflate
// algorithm on the command line. An incremental upper uses the same
// command: mkfs.erofs stores extended attributes and device nodes
// unless xattrs are disabled, which keeps overlay whiteouts. The
// creation time and the filesystem UUID are pinned so the same tree
// packs to the same bytes. SOURCE_DATE_EPOCH only clamps timestamps;
// without -U the superblock UUID stays random and the checksum changes.
func (e erofs) pack(run ToolRunner, src, dst, compression string) error {
	flag, err := e.compressionFlag(compression)
	if err != nil {
		return err
	}
	if run == nil {
		return errErofsRunner
	}
	return run("mkfs.erofs", sourceDateEnv(src), "-U", erofsNilUUID, flag, dst, src)
}

// compressionFlag is the mkfs.erofs -z argument. gzip stays gzip in
// metadata and becomes -zdeflate only on the command line.
func (e erofs) compressionFlag(compression string) (string, error) {
	if compression == "" {
		compression = e.DefaultCompression()
	}
	switch compression {
	case "zstd", "lz4":
		return "-z" + compression, nil
	case "gzip":
		return "-zdeflate", nil
	default:
		return "", fmt.Errorf("container: erofs compression %q is not allowed", compression)
	}
}

// MountArgv is the erofsfuse command line for one lower. fsname
// keeps the original image path in the mount table. A non-empty
// subdir loads the FUSE subdir module. The format-specific options
// argument is ignored in this version.
func (erofs) MountArgv(fsname, subdir, _ string, imagePath, point string) []string {
	return []string{"erofsfuse", "-o", erofsMountOptions(fsname, subdir), imagePath, point}
}

func erofsMountOptions(fsname, subdir string) string {
	opts := "fsname=" + fsname
	if subdir == "" {
		return opts
	}
	return opts + ",modules=subdir,subdir=" + subdir
}

func (erofs) Tools() []string {
	return []string{"mkfs.erofs", "erofsfuse"}
}

// HasSubdir mounts the image read-only just long enough to stat
// subdir, then unmounts it. An empty subdir, or a path that cleans
// to the image root, is always present. The runner performs the
// mount and the unmount.
func (erofs) HasSubdir(run ToolRunner, imagePath, subdir string) (bool, error) {
	rel, err := normalizeErofsSubdir(subdir)
	if err != nil {
		return false, err
	}
	if rel == "" {
		return true, nil
	}
	if run == nil {
		return false, errErofsRunner
	}
	return probeErofsSubdir(run, imagePath, rel)
}

// probeErofsSubdir attaches imagePath at a temporary directory.
// erofsfuse daemonizes after the mount is in place, so the runner
// returns with the tree visible. The directory is removed only after
// unmount succeeds. A failed unmount names the mount point and is
// returned together with any stat failure. A missing path is a
// normal absence.
func probeErofsSubdir(run ToolRunner, imagePath, rel string) (bool, error) {
	point, err := os.MkdirTemp("", "tiler-erofs-")
	if err != nil {
		return false, err
	}

	err = run("erofsfuse", nil, "-o", "ro", imagePath, point)
	if err != nil {
		_ = os.RemoveAll(point)
		return false, err
	}
	present, statErr := statErofsSubdir(point, rel)
	unmountErr := run("fusermount3", nil, "-u", point)
	if unmountErr != nil {
		unmountErr = fmt.Errorf("container: unmount erofs %s: %w", point, unmountErr)
		return false, errors.Join(statErr, unmountErr)
	}
	_ = os.RemoveAll(point)
	if statErr != nil {
		return false, statErr
	}
	return present, nil
}

// statErofsSubdir reports whether rel is a directory inside the mount
// at point. The directory root keeps the lookup under that root, so a
// symlink cannot resolve to a host path. A missing name, or a path that
// walks through a non-directory, is absence. The root is closed before
// return so the caller can unmount.
func statErofsSubdir(point, rel string) (bool, error) {
	root, err := dirroot.Open(point)
	if err != nil {
		return false, err
	}
	defer func() { _ = root.Close() }()

	info, err := root.Stat(rel)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

// normalizeErofsSubdir returns the image-relative subdir, or "" when
// subdir names the image root. Absolute paths and paths that climb
// out of the image are errors.
func normalizeErofsSubdir(subdir string) (string, error) {
	if filepath.IsAbs(subdir) {
		return "", fmt.Errorf("container: erofs subdir %q is not relative", subdir)
	}
	cleaned := filepath.Clean(subdir)
	if cleaned == "." {
		return "", nil
	}
	parent := ".." + string(filepath.Separator)
	if cleaned == ".." || strings.HasPrefix(cleaned, parent) {
		return "", fmt.Errorf("container: erofs subdir %q escapes the image", subdir)
	}
	return cleaned, nil
}

// Detect reads the four-byte superblock magic at offset 1024. A file
// too short to hold the superblock is not erofs. An unreadable path
// is an error, not a mismatch.
func (erofs) Detect(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()

	if _, err := file.Seek(erofsMagicOffset, io.SeekStart); err != nil {
		return false, err
	}
	var magic [4]byte
	_, err = io.ReadFull(file, magic[:])
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return magic == erofsMagic, nil
}
