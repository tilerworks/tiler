package image

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type hooks struct {
	before func(oldpath, newpath string) error
	rename func(oldpath, newpath string) error
	sync   func(dir string) error
}

func writeTemp(dir, final string, data []byte, seam hooks) (string, error) {
	name, err := tempName(final)
	if err != nil {
		return "", err
	}
	tmp := filepath.Join(dir, name)
	if err := writeSynced(tmp, data); err != nil {
		removeFile(tmp)
		return "", err
	}
	if err := syncDir(dir, seam); err != nil {
		removeFile(tmp)
		return "", err
	}
	return tmp, nil
}

func tempName(final string) (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%d.%s", tempPrefix(final), os.Getpid(), hex.EncodeToString(buf[:])), nil
}

func tempPrefix(final string) string { return "." + final + "." }

func isPayloadTempName(name string) bool {
	return strings.HasPrefix(name, tempPrefix(blocksTempFinal))
}

func writeSynced(name string, data []byte) error {
	file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	return joinWriteErr(writeErr, syncErr, closeErr)
}

func joinWriteErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func dirMatchesBlocks(dir string, blocks []BlockEntry) (bool, error) {
	exists, err := pathExists(dir)
	if err != nil || !exists {
		return false, err
	}
	for _, entry := range blocks {
		if !isBlockName(entry.Name) {
			return false, nil
		}
		ok, err := fileMatches(filepath.Join(dir, entry.Name), entry.Size, entry.SHA256)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func fileMatches(path string, size int64, want string) (bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	sum := sha256.New()
	n, copyErr := io.Copy(sum, file)
	closeErr := file.Close()
	if err := joinWriteErr(copyErr, closeErr); err != nil {
		return false, err
	}
	return hex.EncodeToString(sum.Sum(nil)) == want && n == size, nil
}

func (seam hooks) do(oldpath, newpath string) error {
	if seam.before != nil {
		if err := seam.before(oldpath, newpath); err != nil {
			return err
		}
	}
	var err error
	if seam.rename != nil {
		err = seam.rename(oldpath, newpath)
	} else {
		err = os.Rename(oldpath, newpath)
	}
	if err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(newpath), seam); err != nil {
		return err
	}
	if filepath.Dir(oldpath) == filepath.Dir(newpath) {
		return nil
	}
	return syncDir(filepath.Dir(oldpath), seam)
}

func syncDir(dir string, seam hooks) error {
	if seam.sync != nil {
		return seam.sync(dir)
	}
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	return joinWriteErr(file.Sync(), file.Close())
}

func removeFile(name string) {
	if name != "" {
		_ = os.Remove(name)
	}
}

func removeEmptyAncestors(dir, stop string) {
	for dir != stop && dir != "." && dir != string(filepath.Separator) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

func undoLanded(src, dst string) {
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		removeFile(dst)
	}
}
