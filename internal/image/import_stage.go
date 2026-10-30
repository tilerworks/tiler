package image

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/durable"
)

// importStageDir holds unpublished copy payloads inside the target
// library. It is not a version directory and is absent from meta.yaml.
const importStageDir = ".import-stage"

// importCopyBuffer is the one reusable buffer for a streamed copy.
// The allocation does not grow with the payload.
const importCopyBuffer = 32 << 10

// StagedCopy is one unpublished single-file payload. Path is inside
// the target library. SHA256 is the digest of the copied bytes, never
// a sidecar value. Size is the copied byte count. MtimeUnix is the
// source mtime, and the staged file is set to that time. Remove drops
// the unpublished file.
type StagedCopy struct {
	Path      string
	Format    Format
	Size      int64
	MtimeUnix int64
	SHA256    string
	root      string
}

// stageCopyHooks are test seams for one copy. open replaces each
// source open. sync replaces the staged-file sync. digest replaces
// the encoded sum of a single-file copy after the bytes are hashed.
// beforeProbe runs after the source check and before the staged
// container is detected. Split copies do not apply digest, so a
// seam cannot hide a block or logical-image mismatch.
type stageCopyHooks struct {
	open        func(string) (io.ReadCloser, error)
	sync        func(*os.File) error
	digest      func(string) string
	beforeProbe func()
}

// StageImportFile copies one regular file into target-local staging.
// The source is opened once and hashed while it is written. Format
// detection then seeks the staged file, so the remote payload is not
// read in full again. A metadata digest must match those bytes. A
// missing metadata pair records the digest of the copy. A short read, a
// changed source, or a close or sync error removes the staging file.
func StageImportFile(req ImportRequest) (StagedCopy, error) {
	return stageImportFile(req, stageCopyHooks{})
}

func stageImportFile(req ImportRequest, seam stageCopyHooks) (StagedCopy, error) {
	if req.Mode != ImportCopy {
		return StagedCopy{}, importFail(req, errors.New("copy staging requires copy mode"))
	}
	plan, err := PlanImportSource(req)
	if err != nil {
		return StagedCopy{}, err
	}
	if plan.Kind != SourceFile {
		return StagedCopy{}, importFail(req, errors.New("single-file copy requires a regular file"))
	}
	staged, err := copyImportFile(req, plan, seam)
	if err != nil {
		return StagedCopy{}, importFail(req, err)
	}
	return staged, nil
}

func copyImportFile(req ImportRequest, plan SourcePlan, seam stageCopyHooks) (StagedCopy, error) {
	if IsUnknownSHA256(plan.SHA256) {
		return StagedCopy{}, errors.New("digest is the reserved unknown marker")
	}
	before, err := os.Stat(plan.Location)
	if err != nil {
		return StagedCopy{}, err
	}
	src, err := importOpen(plan.Location, seam)
	if err != nil {
		return StagedCopy{}, err
	}
	dst, err := prepareStageFile(plan.Repo)
	if err != nil {
		_ = src.Close()
		return failStage(importStagePath(plan.Repo), req.ImagesDir, err)
	}
	n, sum, copyErr := streamImportCopy(dst, src, seam)
	err = joinWriteErr(copyErr, src.Close())
	if err != nil {
		return failStage(dst.Name(), req.ImagesDir, err)
	}
	staged, err := observeImportCopy(plan.Location, dst.Name(), before, n, sum, req.ImagesDir, seam)
	if err != nil {
		return failStage(dst.Name(), req.ImagesDir, err)
	}
	if plan.SHA256 != "" && staged.SHA256 != plan.SHA256 {
		return failStage(dst.Name(), req.ImagesDir, errors.New("metadata digest does not match payload"))
	}
	return staged, nil
}

func observeImportCopy(loc, staged string, before os.FileInfo, n int64, sum, root string, seam stageCopyHooks) (StagedCopy, error) {
	after, err := os.Stat(loc)
	if err != nil {
		return StagedCopy{}, err
	}
	if err = checkSourceStable(before, after, n); err != nil {
		return StagedCopy{}, err
	}
	if err = acceptCopyDigest(sum); err != nil {
		return StagedCopy{}, err
	}
	if seam.beforeProbe != nil {
		seam.beforeProbe()
	}
	format, err := probeImportFile(staged)
	if err != nil {
		return StagedCopy{}, fmt.Errorf("staged container: %w", err)
	}
	if err = sealStagedCopy(staged, before); err != nil {
		return StagedCopy{}, err
	}
	return StagedCopy{
		Path: staged, Format: format, Size: n, SHA256: sum,
		MtimeUnix: before.ModTime().Unix(), root: root,
	}, nil
}

func streamImportCopy(dst *os.File, src io.Reader, seam stageCopyHooks) (int64, string, error) {
	n, sum, err := streamHashedCopy(dst, src, nil, seam)
	if err != nil {
		return 0, "", err
	}
	return n, encodeCopyDigest(sum, seam), nil
}

// streamHashedCopy writes src through one fixed buffer. The same
// bytes update the returned digest and also, when set. Callers hash
// a logical image by passing one shared writer for every block.
func streamHashedCopy(dst *os.File, src io.Reader, also io.Writer, seam stageCopyHooks) (int64, []byte, error) {
	hash := sha256.New()
	out := io.Writer(io.MultiWriter(dst, hash))
	if also != nil {
		out = io.MultiWriter(dst, hash, also)
	}
	n, copyErr := io.CopyBuffer(out, src, make([]byte, importCopyBuffer))
	err := joinWriteErr(copyErr, syncStaged(dst, seam), dst.Close())
	if err != nil {
		return 0, nil, err
	}
	return n, hash.Sum(nil), nil
}

func prepareStageFile(repo Repo) (*os.File, error) {
	dir := importStagePath(repo)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	name, err := tempName("copy")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name)
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
}

func sealStagedCopy(path string, before os.FileInfo) error {
	when := before.ModTime()
	if err := os.Chtimes(path, when, when); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path), hooks{})
}

func importStagePath(repo Repo) string {
	return filepath.Join(repo.Dir, importStageDir)
}

func importOpen(path string, seam stageCopyHooks) (io.ReadCloser, error) {
	if seam.open != nil {
		return seam.open(path)
	}
	return os.Open(path)
}

func syncStaged(file *os.File, seam stageCopyHooks) error {
	if seam.sync != nil {
		return seam.sync(file)
	}
	return durable.Sync(file)
}

func encodeCopyDigest(sum []byte, seam stageCopyHooks) string {
	encoded := hex.EncodeToString(sum)
	if seam.digest != nil {
		return seam.digest(encoded)
	}
	return encoded
}

func checkSourceStable(before, after os.FileInfo, copied int64) error {
	if !before.Mode().IsRegular() || !after.Mode().IsRegular() {
		return errors.New("source is not a regular file")
	}
	sizeChanged := before.Size() != after.Size() || copied != before.Size()
	mtimeChanged := before.ModTime().UnixNano() != after.ModTime().UnixNano()
	if before.Mode() != after.Mode() || sizeChanged || mtimeChanged {
		return errors.New("source changed during copy")
	}
	return nil
}

func acceptCopyDigest(sum string) error {
	if IsUnknownSHA256(sum) {
		return errors.New("digest is the reserved unknown marker")
	}
	return nil
}

// Remove deletes this unpublished copy and empty staging directories.
func (s StagedCopy) Remove() {
	discardStage(s.Path, s.root)
}

func failStage(path, stop string, err error) (StagedCopy, error) {
	discardStage(path, stop)
	return StagedCopy{}, err
}

func discardStage(path, stop string) {
	if path != "" {
		clearStageDir(path)
		removeFile(path)
	}
	if path == "" || stop == "" {
		return
	}
	removeEmptyAncestors(filepath.Dir(path), stop)
}

func clearStageDir(path string) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return
	}
	for _, entry := range entries {
		removeFile(filepath.Join(path, entry.Name()))
	}
}
