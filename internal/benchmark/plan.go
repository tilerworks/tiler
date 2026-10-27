// Package benchmark plans and measures one path-level image benchmark.
// Plan is pure: directory facts come from an injected seam. Run
// prepares directories, prints metadata, probes, packs, reads, and
// cleans up. Payload trees, the 4 KiB probe, tree reads, and the step
// table do not start a process. Pack, attach, and purge use injected
// seams so tests never start host image tools or sudo.
package benchmark

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"runtime"
)

// Options are the benchmark flags after the command has read them.
// Size and FileSize are the raw flag text; Plan parses them.
// Empty Mode means both. Empty Type means dmg on darwin and a
// usage error on every other host. BenchDir and Src are relative
// directory names that Plan joins under the benchmark path and the
// runtime dir. BenchDirSet and SrcSet report whether the user passed
// those flags explicitly; Plan refuses an explicit value it cannot
// use and silently drops the default instead.
type Options struct {
	Path        string
	ReadOnly    bool
	Image       string
	BenchDir    string
	BenchDirSet bool
	RuntimeDir  string
	Mode        string
	Type        string
	Size        string
	FileSize    string
	Compress    string
	Src         string
	SrcSet      bool
	ProbeOps    int
	NoProbe     bool
	Keep        bool
	Format      string
}

// Decision is what a later run will do. Create packs an image,
// Read measures two reads, and Probe samples 4 KiB random I/O.
// BenchDir is the absolute directory Plan joined under Path; it is
// empty when the run does not use one. CreatePath records that Path
// did not exist at plan time; the run creates it and prints a
// warning. Src is the payload directory name relative to the
// runtime dir, non-empty only when Create is true; Run joins it
// with the runtime dir. OwnRuntime is true when RuntimeDir is empty
// and the run must create a private directory under the system temp
// dir. Otherwise RuntimeDir is the runtimeSubdir directory under the
// user-passed --runtime-dir (RuntimeParent) that holds mount, logs,
// and payload; the run removes it at the end. RuntimeDirCreated
// records that RuntimeParent was absent at plan time, so the run
// also removes RuntimeParent when it ends up empty. Compress is a
// container token: ulfo, ulmo, udro, or a squashfs or erofs name.
// It stays empty for tar, and for erofs when --compress was unset,
// so pack can choose from the mkfs.erofs help text.
type Decision struct {
	Create            bool
	Read              bool
	Probe             bool
	Path              string
	CreatePath        bool
	BenchDir          string
	RuntimeDir        string
	RuntimeParent     string
	RuntimeDirCreated bool
	OwnRuntime        bool
	Image             string
	Src               string
	Type              string
	Compress          string
	Mode              string
	Size              int64
	FileSize          int64
	ProbeOps          int
	ReadOnly          bool
	Keep              bool
	Format            string
}

// UsageError is a flag or directory policy problem. Callers map it
// to exit code 2. Other errors are runtime failures.
type UsageError struct {
	err error
}

// Error returns the usage message.
func (e *UsageError) Error() string { return e.err.Error() }

// Unwrap returns the wrapped error.
func (e *UsageError) Unwrap() error { return e.err }

// FS is the stat, access, and directory-list seam Plan uses.
// Tests substitute the funcs so directory policy does not depend
// on host mounts. A nil func is a programming error. An empty
// GOOS means runtime.GOOS. Access returns nil when the directory
// is writable.
type FS struct {
	Stat    func(string) (fs.FileInfo, error)
	Access  func(string) error
	ReadDir func(string) ([]fs.DirEntry, error)
	GOOS    string
}

// Plan decides create, read, and probe. It returns a UsageError for
// a refused flag combination or directory layout. It does not create
// directories or touch image tools.
func Plan(opts Options, files FS) (Decision, error) {
	mode, err := parseMode(opts.Mode)
	if err != nil {
		return Decision{}, err
	}
	size, fileSize, err := parseSizes(opts.Size, opts.FileSize)
	if err != nil {
		return Decision{}, err
	}
	typ, compress, err := parseType(opts.Type, opts.Compress, files.goos())
	if err != nil {
		return Decision{}, err
	}
	path, pathMissing, err := requireVolume(files, opts.Path)
	if err != nil {
		return Decision{}, err
	}
	act, err := decide(mode, opts)
	if err != nil {
		return Decision{}, err
	}
	// A missing path is created by the run, so the writable check
	// has nothing to stat yet; a failed create surfaces at run time.
	if !pathMissing {
		if err := ensureWritable(files, path, opts.ReadOnly); err != nil {
			return Decision{}, err
		}
	}
	bench, err := placeBench(files, path, opts.BenchDir, opts.BenchDirSet, act)
	if err != nil {
		return Decision{}, err
	}
	rt, err := placeRuntime(files, path, opts.RuntimeDir)
	if err != nil {
		return Decision{}, err
	}
	src, err := placeSrc(opts.Src, opts.SrcSet, act.Create)
	if err != nil {
		return Decision{}, err
	}
	image, err := placeImage(opts.Image)
	if err != nil {
		return Decision{}, err
	}
	ops, err := probeOps(opts.ProbeOps)
	if err != nil {
		return Decision{}, err
	}
	format, err := parseFormat(opts.Format)
	if err != nil {
		return Decision{}, err
	}
	return Decision{
		Create:            act.Create,
		Read:              act.Read,
		Probe:             act.Probe,
		Path:              path,
		CreatePath:        pathMissing,
		BenchDir:          bench,
		RuntimeDir:        rt.root,
		RuntimeParent:     rt.parent,
		RuntimeDirCreated: rt.parentCreated,
		OwnRuntime:        rt.owned,
		Image:             image,
		Src:               src,
		Type:              typ,
		Compress:          compress,
		Mode:              mode,
		Size:              size,
		FileSize:          fileSize,
		ProbeOps:          ops,
		ReadOnly:          opts.ReadOnly,
		Keep:              opts.Keep,
		Format:            format,
	}, nil
}

func (f FS) goos() string {
	if f.GOOS != "" {
		return f.GOOS
	}
	return runtime.GOOS
}

func (f FS) stat(path string) (fs.FileInfo, error) {
	if f.Stat == nil {
		return nil, errNilSeam
	}
	return f.Stat(path)
}

func (f FS) access(path string) error {
	if f.Access == nil {
		return errNilSeam
	}
	return f.Access(path)
}

func (f FS) readDir(path string) ([]fs.DirEntry, error) {
	if f.ReadDir == nil {
		return nil, errNilSeam
	}
	return f.ReadDir(path)
}

var errNilSeam = errors.New("benchmark: filesystem seam is nil")

func usagef(format string, args ...any) error {
	return &UsageError{err: fmt.Errorf(format, args...)}
}

func absPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("benchmark: abs %s: %w", path, err)
	}
	return filepath.Clean(abs), nil
}
