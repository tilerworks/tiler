package benchmark

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// chunkSize is the payload write size and the tree read size.
const chunkSize = 1 << 20

// payloadEntropy fills generated payload files. Tests replace it
// to force an all-zero tree.
var payloadEntropy = rand.Reader

// WritePayload writes size bytes under dest as files of at most
// fileSize bytes. The last file is shorter when size is not a
// multiple of fileSize. Names are f00000, f00001, and so on.
// Bytes come from cryptographic randomness. An all-zero tree is
// removed and refused. The returned count is the number of files.
func WritePayload(dest string, size, fileSize int64) (int, error) {
	if err := payloadSizes(size, fileSize); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return 0, fmt.Errorf("benchmark: mkdir %s: %w", dest, err)
	}
	names, nonzero, err := writePayloadFiles(dest, size, fileSize)
	if err != nil {
		removeNamed(dest, names)
		return 0, err
	}
	if nonzero {
		return len(names), nil
	}
	removeNamed(dest, names)
	return 0, errors.New("benchmark: payload is all zeros")
}

func payloadSizes(size, fileSize int64) error {
	if size < 1 || fileSize < 1 {
		return errors.New("benchmark: payload size and file size must be at least 1 byte")
	}
	return nil
}

func writePayloadFiles(dest string, size, fileSize int64) ([]string, bool, error) {
	var names []string
	var nonzero bool
	var index int
	for left := size; left > 0; index++ {
		n := fileSize
		if left < n {
			n = left
		}
		name := fmt.Sprintf("f%05d", index)
		saw, err := writePayloadFile(filepath.Join(dest, name), n)
		names = append(names, name)
		if err != nil {
			return names, false, err
		}
		nonzero = nonzero || saw
		left -= n
	}
	return names, nonzero, nil
}

func writePayloadFile(path string, n int64) (bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return false, fmt.Errorf("benchmark: create %s: %w", path, err)
	}
	nonzero, copyErr := copyEntropy(f, n)
	closeErr := f.Close()
	if copyErr != nil {
		return false, copyErr
	}
	if closeErr != nil {
		return false, fmt.Errorf("benchmark: close %s: %w", path, closeErr)
	}
	return nonzero, nil
}

func copyEntropy(w io.Writer, n int64) (bool, error) {
	buf := make([]byte, chunkSize)
	var nonzero bool
	for n > 0 {
		want := chunkLimit(n, len(buf))
		chunk := buf[:want]
		if _, err := io.ReadFull(payloadEntropy, chunk); err != nil {
			return false, fmt.Errorf("benchmark: payload urandom: %w", err)
		}
		if _, err := w.Write(chunk); err != nil {
			return false, fmt.Errorf("benchmark: write payload: %w", err)
		}
		nonzero = nonzero || nonZero(chunk)
		n -= want
	}
	return nonzero, nil
}

func chunkLimit(n int64, buf int) int64 {
	if n < int64(buf) {
		return n
	}
	return int64(buf)
}

func nonZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return true
		}
	}
	return false
}

func removeNamed(dir string, names []string) {
	for _, name := range names {
		_ = os.Remove(filepath.Join(dir, name))
	}
}
