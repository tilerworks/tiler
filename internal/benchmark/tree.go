package benchmark

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ReadTree reads file bytes under root in 1 MiB chunks. File names
// that start with "." are skipped. Directories are still walked,
// including directories whose names start with ".". It returns the
// number of bytes read and the elapsed time.
func ReadTree(root string) (int64, time.Duration, error) {
	start := time.Now()
	n, err := readTree(root)
	return n, time.Since(start), err
}

func readTree(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skipTreeEntry(d) {
			return nil
		}
		n, readErr := readChunks(path)
		total += n
		return readErr
	})
	return total, err
}

func skipTreeEntry(d os.DirEntry) bool {
	return d.IsDir() || strings.HasPrefix(d.Name(), ".")
}

func readChunks(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("benchmark: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return copyChunks(f, path)
}

func copyChunks(r io.Reader, path string) (int64, error) {
	buf := make([]byte, chunkSize)
	var n int64
	for {
		got, err := r.Read(buf)
		n += int64(got)
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, fmt.Errorf("benchmark: read %s: %w", path, err)
		}
	}
}
