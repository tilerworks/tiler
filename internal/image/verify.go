package image

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// VerifySHA256 re-hashes a published file and compares the digest
// with meta. Builds call it after publishing so a torn write cannot
// pass as a good image.
func VerifySHA256(meta Meta, path string) error {
	sum, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if hex.EncodeToString(sum[:]) != meta.SHA256 {
		return fmt.Errorf("image: %s does not match meta sha256", path)
	}
	return nil
}

// QuickCheck compares the recorded size and mtime with the file on
// disk. Later mount commands use it as a cheap staleness probe.
func QuickCheck(meta Meta, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() != meta.Size {
		return fmt.Errorf("image: %s size %d does not match meta %d",
			path, info.Size(), meta.Size)
	}
	if info.ModTime().Unix() != meta.MtimeUnix {
		return fmt.Errorf("image: %s mtime does not match meta", path)
	}
	return nil
}

// fileSHA256 streams a file through sha256 without keeping a second
// copy of the bytes in memory.
func fileSHA256(path string) ([32]byte, error) {
	var sum [32]byte
	file, err := os.Open(path)
	if err != nil {
		return sum, err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if err := joinWriteErr(copyErr, closeErr); err != nil {
		return sum, err
	}
	hash.Sum(sum[:0])
	return sum, nil
}
