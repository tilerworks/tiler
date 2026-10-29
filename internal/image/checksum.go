package image

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// UnknownSHA256 is sixty-four 0 digits: the reserved marker for a
// digest that was not computed. It is not the SHA-256 of empty
// content and must not be used as a content address for dedup or a
// shared cache.
const UnknownSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"

// IsUnknownSHA256 reports the reserved unknown-digest marker.
// sum must already be lowercase; the sidecar parser normalizes.
func IsUnknownSHA256(sum string) bool {
	return sum == UnknownSHA256
}

// ErrChecksumSidecar is a present sidecar that is empty, malformed,
// oversized, duplicated, named for a different file, or not a
// regular file. A missing sidecar is not this error.
var ErrChecksumSidecar = errors.New("invalid checksum sidecar")

// maxChecksumSidecar is the largest accepted sidecar. One extra byte
// is read so a larger file fails without scanning the rest.
const maxChecksumSidecar = 8 << 10

// ReadChecksumSidecar reads the literal path imagePath+".sha256".
// A missing sidecar returns UnknownSHA256. The image path is not
// resolved, so a symlink still selects the sidecar beside the path
// the caller passed. A dangling sidecar is an error. A symlink is
// read only when its final target is a regular file. Directory,
// FIFO, socket, and device nodes fail with ErrChecksumSidecar and
// are not opened. The recorded digest is lowercase. An all-zero
// record stays the unknown marker.
func ReadChecksumSidecar(imagePath string) (string, error) {
	path := imagePath + ".sha256"
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return UnknownSHA256, nil
		}
		return "", fmt.Errorf("checksum sidecar: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Stat follows the link. It does not open the target, so a
		// FIFO or device is classified without blocking in open.
		info, err = os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("checksum sidecar: %w", err)
		}
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("checksum sidecar: %w", ErrChecksumSidecar)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("checksum sidecar: %w", err)
	}
	data, readErr := readBounded(file, maxChecksumSidecar)
	closeErr := file.Close()
	if err := joinWriteErr(readErr, closeErr); err != nil {
		return "", fmt.Errorf("checksum sidecar: %w", err)
	}
	sum, err := ParseChecksumSidecar(data, filepath.Base(imagePath))
	if err != nil {
		return "", fmt.Errorf("checksum sidecar %s: %w", path, err)
	}
	return sum, nil
}

// ParseChecksumSidecar accepts one bare SHA-256 or one sha256sum
// record (text "digest  name" or binary "digest *name"), with one
// trailing newline. filename must equal a recorded name. Digests
// are returned lowercase. An all-zero digest is the unknown marker,
// not a verified sum.
func ParseChecksumSidecar(data []byte, filename string) (string, error) {
	if len(data) > maxChecksumSidecar {
		return "", fmt.Errorf("%w: exceeds %d bytes", ErrChecksumSidecar, maxChecksumSidecar)
	}
	line, err := oneChecksumLine(data)
	if err != nil {
		return "", err
	}
	return parseChecksumLine(line, filename)
}

func readBounded(r io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("%w: exceeds %d bytes", ErrChecksumSidecar, limit)
	}
	return data, nil
}

func oneChecksumLine(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("%w: empty", ErrChecksumSidecar)
	}
	body := bytes.TrimSuffix(data, []byte("\n"))
	body = bytes.TrimSuffix(body, []byte("\r"))
	if len(body) == 0 {
		return "", fmt.Errorf("%w: empty", ErrChecksumSidecar)
	}
	if bytes.IndexByte(body, 0) >= 0 {
		return "", fmt.Errorf("%w: malformed", ErrChecksumSidecar)
	}
	if bytes.IndexByte(body, '\n') >= 0 || bytes.IndexByte(body, '\r') >= 0 {
		return "", fmt.Errorf("%w: duplicate records", ErrChecksumSidecar)
	}
	return string(body), nil
}

func parseChecksumLine(line, filename string) (string, error) {
	if len(line) < sumHexLen {
		return "", fmt.Errorf("%w: malformed", ErrChecksumSidecar)
	}
	sum, ok := normalizeSHA256(line[:sumHexLen])
	if !ok {
		return "", fmt.Errorf("%w: malformed", ErrChecksumSidecar)
	}
	rest := line[sumHexLen:]
	if rest == "" {
		return sum, nil
	}
	name, ok := checksumRecordName(rest)
	if !ok || name == "" {
		return "", fmt.Errorf("%w: malformed", ErrChecksumSidecar)
	}
	if name != filename {
		return "", fmt.Errorf("%w: mismatched name", ErrChecksumSidecar)
	}
	return sum, nil
}

func checksumRecordName(rest string) (string, bool) {
	if strings.HasPrefix(rest, "  ") || strings.HasPrefix(rest, " *") {
		return rest[2:], true
	}
	return "", false
}

func normalizeSHA256(text string) (string, bool) {
	if len(text) != sumHexLen {
		return "", false
	}
	raw, err := hex.DecodeString(text)
	if err != nil {
		return "", false
	}
	return hex.EncodeToString(raw), true
}
