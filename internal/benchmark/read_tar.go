package benchmark

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ReadTar streams regular-file bytes from a gzip tar. A member whose
// final path component starts with "." is skipped. Nothing is written
// and the archive is not mounted. The duration includes a failed read.
func ReadTar(path string) (int64, time.Duration, error) {
	start := time.Now()
	n, err := readGzipTar(path)
	return n, time.Since(start), err
}

func readGzipTar(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("benchmark: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, fmt.Errorf("benchmark: gzip %s: %w", path, err)
	}
	defer func() { _ = gz.Close() }()
	return readTarMembers(tar.NewReader(gz), path)
}

func readTarMembers(tr *tar.Reader, path string) (int64, error) {
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return total, nil
		}
		if err != nil {
			return total, fmt.Errorf("benchmark: tar %s: %w", path, err)
		}
		n, err := readTarMember(tr, hdr)
		total += n
		if err != nil {
			return total, err
		}
	}
}

func readTarMember(tr *tar.Reader, hdr *tar.Header) (int64, error) {
	if skipTarMember(hdr) {
		return 0, nil
	}
	return copyChunks(tr, hdr.Name)
}

func skipTarMember(hdr *tar.Header) bool {
	if hdr.Typeflag != tar.TypeReg {
		return true
	}
	return strings.HasPrefix(filepath.Base(hdr.Name), ".")
}
