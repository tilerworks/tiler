// Package container defines the read-only image container format
// abstraction and its process-wide registry. Concrete formats
// register a Format and are resolved by type name or by file magic.
// Packing and mounting go through ToolRunner so this package never
// starts a process itself.
package container

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ToolRunner executes one external tool with an argument list and
// extra environment variables. The arguments go straight to exec,
// never through a shell; the env entries are appended to the
// inherited environment, and an empty env changes nothing.
type ToolRunner func(name string, env []string, args ...string) error

// Format is one read-only image container. Squashfs and erofs are
// the first implementations; later formats register the same way.
type Format interface {
	// Type is the meta.format.type string, for example "squashfs".
	// The value is fixed for the life of the registered value.
	Type() string

	// AllowsCompression reports whether compression is valid for
	// this container. An empty compression means the format default.
	AllowsCompression(compression string) bool

	// DefaultCompression is used when compression is unset.
	DefaultCompression() string

	// Detect reports whether the file at path is this container,
	// based on magic. A non-nil error means detection failed and
	// the bool is ignored. Callers use Detect for file-path mounts
	// and for meta/file consistency.
	Detect(path string) (bool, error)

	// PackFull packs the src directory into the dst image file.
	PackFull(run ToolRunner, src, dst, compression string) error

	// PackIncr packs an overlay upper, including whiteouts and
	// extended attributes when the format needs them.
	PackIncr(run ToolRunner, src, dst, compression string) error

	// MountArgv is the FUSE argument vector that mounts one lower
	// at point. options is the format-specific config string.
	MountArgv(fsname, subdir, options, imagePath, point string) []string

	// HasSubdir reports whether imagePath contains subdir before
	// any mount.
	HasSubdir(run ToolRunner, imagePath, subdir string) (bool, error)

	// Tools are the binaries that must all exist for this stack to
	// be complete: the packer and the FUSE client. Doctor aggregates
	// these.
	Tools() []string
}

// ErrUnknownType means Lookup was asked for a type that is not
// registered. An empty registry yields the same error.
var ErrUnknownType = errors.New("unknown container type")

// ErrNotDetected means no registered format claims the file. An
// empty registry yields the same error.
var ErrNotDetected = errors.New("container format not detected")

// ErrAmbiguous means more than one registered format claims the
// file.
var ErrAmbiguous = errors.New("ambiguous container format")

var (
	mu     sync.RWMutex
	byType = map[string]Format{}
	order  []Format
)

// Register adds f to the process registry. It panics when f is nil,
// when Type is empty, or when that type is already registered.
// Formats register from init; Type must stay stable afterwards.
func Register(f Format) {
	if f == nil {
		panic("container: register nil format")
	}
	typ := f.Type()
	if typ == "" {
		panic("container: register empty type")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, exists := byType[typ]; exists {
		panic("container: duplicate format " + typ)
	}
	byType[typ] = f
	order = append(order, f)
}

// Lookup returns the format registered under typ.
func Lookup(typ string) (Format, error) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := byType[typ]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, typ)
	}
	return f, nil
}

// DetectFile returns the single registered format whose Detect
// claims path. Formats are probed in registration order. The first
// Detect error is returned and later formats are not probed. Zero
// matches yield ErrNotDetected. Two or more matches yield
// ErrAmbiguous.
func DetectFile(path string) (Format, error) {
	mu.RLock()
	list := make([]Format, len(order))
	copy(list, order)
	mu.RUnlock()

	if len(list) == 0 {
		return nil, fmt.Errorf("%w: %q; registry is empty", ErrNotDetected, path)
	}

	var matched []Format
	for _, f := range list {
		ok, err := f.Detect(path)
		if err != nil {
			return nil, fmt.Errorf("detect %s: %w", f.Type(), err)
		}
		if ok {
			matched = append(matched, f)
		}
	}
	switch len(matched) {
	case 0:
		return nil, fmt.Errorf("%w: %q", ErrNotDetected, path)
	case 1:
		return matched[0], nil
	default:
		names := make([]string, len(matched))
		for i, f := range matched {
			names[i] = f.Type()
		}
		return nil, fmt.Errorf("%w: %q matches %s", ErrAmbiguous, path, strings.Join(names, ", "))
	}
}

// All returns the registered formats in registration order. The
// slice is a copy; the caller may reorder it.
func All() []Format {
	mu.RLock()
	defer mu.RUnlock()
	if len(order) == 0 {
		return nil
	}
	out := make([]Format, len(order))
	copy(out, order)
	return out
}
