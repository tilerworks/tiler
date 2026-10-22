package image

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
)

// spaceMargin is extra room beyond the estimated payload. Packers
// add headers, and a compressed image of incompressible bytes can
// land a little above the source size.
const spaceMargin = 1 << 20

// spaceWrite is one file a build will create before it deletes any
// earlier copy. path is the directory that receives the file. It
// does not have to exist yet.
type spaceWrite struct {
	path  string
	bytes int64
}

// spaceInfo is free space on one volume and an id that groups paths
// on that volume.
type spaceInfo struct {
	avail int64
	dev   uint64
}

// probeSpace reads free space for path. Tests replace it.
var probeSpace = probeSpaceOS

// estimateTree is an upper bound for packing root. Incompressible
// input stays near the source size, so the source size plus a margin
// is the space the new image file can need.
func estimateTree(root string) (int64, error) {
	n, err := treeSize(root)
	if err != nil {
		return 0, fmt.Errorf("build: %w", err)
	}
	return withMargin(n), nil
}

// estimateFiles is an upper bound for a new image built from existing
// files, such as a shadow bake of a base image plus its shadow.
func estimateFiles(paths ...string) (int64, error) {
	var n int64
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return 0, fmt.Errorf("build: %w", err)
		}
		size := info.Size()
		if size < 0 {
			continue
		}
		if n > math.MaxInt64-size {
			return math.MaxInt64, nil
		}
		n += size
	}
	return withMargin(n), nil
}

// treeSize sums non-directory sizes under root. Symlinks contribute
// their own length, not the target.
func treeSize(root string) (int64, error) {
	var n int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		size := info.Size()
		if size < 0 {
			return nil
		}
		if n > math.MaxInt64-size {
			return errors.New("source size overflows")
		}
		n += size
		return nil
	})
	return n, err
}

func withMargin(n int64) int64 {
	if n < 0 {
		n = 0
	}
	if n > math.MaxInt64-spaceMargin {
		return math.MaxInt64
	}
	return n + spaceMargin
}

// ensureSpace refuses the build when the planned writes do not fit.
// Writes on the same volume are added together, because a blocks
// build keeps the staging file and the published copy at the same
// time. The check runs before any image bytes are written.
func ensureSpace(writes ...spaceWrite) error {
	type bucket struct {
		path  string
		need  int64
		avail int64
	}
	buckets := map[uint64]*bucket{}
	for _, w := range writes {
		if w.bytes <= 0 {
			continue
		}
		info, err := probeSpace(w.path)
		if err != nil {
			return fmt.Errorf("build: free space: %w", err)
		}
		b := buckets[info.dev]
		if b == nil {
			b = &bucket{path: w.path, avail: info.avail}
			buckets[info.dev] = b
		}
		if b.need > math.MaxInt64-w.bytes {
			b.need = math.MaxInt64
		} else {
			b.need += w.bytes
		}
		if b.need > b.avail {
			return fmt.Errorf(
				"build: not enough free space on %s: need %d bytes, %d available",
				b.path, b.need, b.avail)
		}
	}
	return nil
}

// existingPath is path when it exists, otherwise the nearest parent
// that does. Free-space checks happen before the repository
// directory is created.
func existingPath(path string) (string, error) {
	path = filepath.Clean(path)
	if path == "" || path == "." {
		return "", errors.New("empty path")
	}
	cur := path
	for {
		if _, err := os.Stat(cur); err == nil {
			return cur, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("%s: no existing directory", path)
		}
		cur = parent
	}
}
