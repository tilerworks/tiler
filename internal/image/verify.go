package image

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

// VerifySHA256 re-hashes a published file and compares the digest
// with the recorded node facts. Builds call it when the build ends
// so a torn rename cannot pass as a good image.
func VerifySHA256(node ImageMeta, path string) error {
	return verifyFileSHA256(path, node.SHA256)
}

func verifyFileSHA256(path, want string) error {
	bytesPath, err := ResolvePayloadBytesPath(path)
	if err != nil {
		return err
	}
	sum, err := fileSHA256(bytesPath)
	if err != nil {
		return err
	}
	if got := hexDigest(sum); got != want {
		return fmt.Errorf("image: %s does not match sha256", path)
	}
	return nil
}

// QuickCheck compares the recorded size and mtime with the file on
// disk. Later mount commands use it as a cheap staleness probe.
func QuickCheck(node ImageMeta, path string) error {
	return quickCheckFile(path, node.Size, node.MtimeUnix)
}

func quickCheckFile(path string, size, mtime int64) error {
	bytesPath, err := ResolvePayloadBytesPath(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(bytesPath)
	if err != nil {
		return err
	}
	if info.Size() != size {
		return fmt.Errorf("image: %s size %d does not match meta %d",
			path, info.Size(), size)
	}
	if info.ModTime().Unix() != mtime {
		return fmt.Errorf("image: %s mtime does not match meta", path)
	}
	return nil
}

// QuickCheckTarget compares every payload file of a resolved split
// target with its recorded size and mtime. It is the default cheap
// probe before a split mount; the single-file stack form is probed
// through QuickCheck by the caller instead.
func QuickCheckTarget(t Target) error {
	for _, file := range targetFiles(t) {
		if err := quickCheckFile(file.path, file.size, file.mtime); err != nil {
			return fmt.Errorf("mount: %s: %w", file.what, err)
		}
	}
	return nil
}

// VerifyTarget re-hashes every payload file of a resolved split
// target against its recorded digest. It is the full probe behind
// --verify; the single-file stack form is verified through
// VerifySHA256 by the caller instead.
func VerifyTarget(t Target) error {
	for _, file := range targetFiles(t) {
		if err := verifyFileSHA256(file.path, file.sha); err != nil {
			return fmt.Errorf("mount: %s: %w", file.what, err)
		}
	}
	return nil
}

// payloadFile is one resolved payload file of a split target with
// the facts its manifest records.
type payloadFile struct {
	what  string
	path  string
	size  int64
	mtime int64
	sha   string
}

// targetFiles flattens the manifest of a split target. The
// single-file stack form has no split payload files and yields
// nothing. A blocks stack walks every layer's Blocks list when
// Members is set; otherwise it falls back to the full-layer Blocks.
func targetFiles(t Target) []payloadFile {
	if t.Form != PayloadBlocks {
		return nil
	}
	if len(t.Members) > 0 {
		var files []payloadFile
		for _, layer := range t.Members {
			for _, block := range layer.Blocks {
				what := "block " + block.Name
				if layer.ID != "" && layer.ID != t.ID {
					what = "block " + layer.ID + "/" + block.Name
				}
				files = append(files, payloadFile{
					what:  what,
					path:  block.Path,
					size:  block.Size,
					mtime: block.MtimeUnix,
					sha:   block.SHA256,
				})
			}
		}
		return files
	}
	files := make([]payloadFile, 0, len(t.Blocks))
	for _, block := range t.Blocks {
		files = append(files, payloadFile{
			what:  "block " + block.Name,
			path:  block.Path,
			size:  block.Size,
			mtime: block.MtimeUnix,
			sha:   block.SHA256,
		})
	}
	return files
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
