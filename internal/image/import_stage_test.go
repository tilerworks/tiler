package image

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStageImportFileCopiesSourceOnce(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	body := append([]byte("hsqs"), []byte("vendor-payload")...)
	src := filepath.Join(nas, "trick.dmg")
	if err := os.WriteFile(src, body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])
	if err := os.WriteFile(src+".sha256", []byte(want+"  trick.dmg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := importStamp(t, nas)
	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	loc, err := filepath.EvalSymlinks(src)
	if err != nil {
		t.Fatal(err)
	}
	var opens int
	staged := stageImportOK(t, copyReq(images, src), stageCopyHooks{open: func(path string) (io.ReadCloser, error) {
		if path != loc {
			t.Fatalf("copy opened %s", path)
		}
		opens++
		file, openErr := os.Open(path)
		if openErr != nil {
			return nil, openErr
		}
		return boundedCopy{t, file}, nil
	}})
	t.Cleanup(staged.Remove)
	if opens != 1 || staged.SHA256 != want {
		t.Fatalf("opens %d digest %s", opens, staged.SHA256)
	}
	if staged.Format.Type != FormatSquashfs || staged.Format.Compression != CompressionZstd {
		t.Fatalf("format = %+v", staged.Format)
	}
	if staged.Size != info.Size() || staged.MtimeUnix != info.ModTime().Unix() {
		t.Fatalf("staged facts = %+v stat %+v", staged, info)
	}
	got, err := os.Stat(staged.Path)
	if err != nil || got.Size() != staged.Size || got.ModTime().Unix() != staged.MtimeUnix {
		t.Fatalf("staged stat = %+v err %v", got, err)
	}
	if !bytes.Equal(mustRead(t, staged.Path), body) {
		t.Fatal("staged bytes differ from the source")
	}
	repo, err := OpenRepo(images, "vendor/app")
	if err != nil || !strings.HasPrefix(staged.Path, importStagePath(repo)+string(filepath.Separator)) {
		t.Fatalf("path %s repo %s err %v", staged.Path, repo.Dir, err)
	}
	assertUnpublished(t, repo)
	if after := importStamp(t, nas); after != before {
		t.Fatalf("source tree changed\n%s\n%s", before, after)
	}
	staged.Remove()
	if _, statErr := os.Stat(staged.Path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cleanup left %s: %v", staged.Path, statErr)
	}
}

func TestStageImportFileRejectsMismatchedMetadata(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	src := writeFakeSquashfs(t, nas, []byte("mismatch"))
	if err := os.WriteFile(src+".sha256", []byte(strings.Repeat("ab", 32)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := StageImportFile(copyReq(images, src))
	if err == nil || !strings.Contains(err.Error(), "metadata digest does not match") {
		t.Fatalf("mismatch: %v", err)
	}
	if err := os.WriteFile(src+".sha256", []byte(UnknownSHA256+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = StageImportFile(copyReq(images, src)); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("explicit zero copy: %v", err)
	}
}

func TestStageImportFileDetectsStagedContainer(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	src := writeFakeSquashfs(t, nas, []byte("staged-detect"))
	original := mustRead(t, src)
	staged := stageImportOK(t, copyReq(images, src), stageCopyHooks{beforeProbe: func() {
		body := bytes.Repeat([]byte{'z'}, len(original))
		if err := os.WriteFile(src, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}})
	t.Cleanup(staged.Remove)
	if bytes.Equal(mustRead(t, src), original) {
		t.Fatal("source was not replaced before detection")
	}
	if staged.Format.Type != FormatSquashfs || !bytes.Equal(mustRead(t, staged.Path), original) {
		t.Fatalf("format %+v", staged.Format)
	}
}

func TestStageImportFileCleansUpAfterFaults(t *testing.T) {
	withLinuxLinkPolicy(t)
	cases := []stageFault{
		{name: "short read", open: openUnexpected, want: "unexpected EOF"},
		{name: "truncated stream", open: openTruncated, want: "source changed"},
		{name: "close", open: openCloseError, want: "close failed"},
		{name: "sync", sync: func(*os.File) error { return errors.New("sync failed") }, want: "sync failed"},
		{name: "source change", open: openChangingSource, want: "source changed", touched: true},
		{name: "reserved digest", digest: func(string) string { return UnknownSHA256 }, want: "reserved"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			images := t.TempDir()
			nas := t.TempDir()
			src := writeFakeSquashfs(t, nas, []byte("payload-"+tc.name))
			repo := mustStageRepo(t, images)
			if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
				t.Fatal(err)
			}
			keep := filepath.Join(repo.Dir, "keep")
			if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			before := mustRead(t, src)
			stamp := importStamp(t, nas)
			_, err := stageImportFile(copyReq(images, src), stageCopyHooks{open: tc.open, sync: tc.sync, digest: tc.digest})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v", err)
			}
			if !bytes.Equal(mustRead(t, src), before) {
				t.Fatal("source bytes changed")
			}
			if !tc.touched {
				if after := importStamp(t, nas); after != stamp {
					t.Fatalf("source tree changed\n%s\n%s", stamp, after)
				}
			}
			if _, statErr := os.Stat(importStagePath(repo)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("staging remains: %v", statErr)
			}
			if _, statErr := os.Stat(keep); statErr != nil {
				t.Fatalf("existing target file: %v", statErr)
			}
			assertUnpublished(t, repo)
		})
	}
}

func TestStageImportFileRejectsUnstagedInputs(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	file := writeFakeSquashfs(t, nas, []byte("no-link"))
	dir := filepath.Join(nas, "lib")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeImportLibrary(t, dir)
	junk := filepath.Join(nas, "notes.txt")
	if err := os.WriteFile(junk, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := importStamp(t, nas)
	cases := []struct {
		name, source, want string
		mode               ImportMode
	}{
		{name: "link", source: file, mode: ImportLink, want: "copy mode"},
		{name: "directory", source: dir, mode: ImportCopy, want: "regular file"},
		{name: "unknown format", source: junk, mode: ImportCopy, want: "not detected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := copyReq(images, tc.source)
			req.Mode = tc.mode
			_, err := StageImportFile(req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v", err)
			}
			repo := mustStageRepo(t, images)
			if _, statErr := os.Stat(repo.Dir); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("target created: %v", statErr)
			}
		})
	}
	if after := importStamp(t, nas); after != before {
		t.Fatalf("source tree changed\n%s\n%s", before, after)
	}
}

type stageFault struct {
	name    string
	open    func(string) (io.ReadCloser, error)
	sync    func(*os.File) error
	digest  func(string) string
	want    string
	touched bool
}

type boundedCopy struct {
	t *testing.T
	*os.File
}

func (b boundedCopy) Read(p []byte) (int, error) {
	if len(p) > importCopyBuffer {
		b.t.Fatalf("copy buffer %d", len(p))
	}
	return b.File.Read(p)
}

type closeFile struct {
	*os.File
	close func() error
}

func (f closeFile) Close() error {
	err := f.File.Close()
	if f.close == nil {
		return err
	}
	return joinWriteErr(err, f.close())
}

func openUnexpected(string) (io.ReadCloser, error) {
	return errReader(func(p []byte) (int, error) {
		if len(p) > importCopyBuffer {
			return 0, errors.New("unbounded copy")
		}
		return 0, io.ErrUnexpectedEOF
	}), nil
}

func openTruncated(string) (io.ReadCloser, error) {
	return &truncatedCopy{}, nil
}

func openCloseError(path string) (io.ReadCloser, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return closeFile{File: file, close: func() error { return errors.New("close failed") }}, nil
}

func openChangingSource(path string) (io.ReadCloser, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return closeFile{File: file, close: func() error {
		when := time.Now().Add(time.Hour)
		return os.Chtimes(path, when, when)
	}}, nil
}

type truncatedCopy struct{ sent bool }

func (c *truncatedCopy) Read(p []byte) (int, error) {
	if c.sent || len(p) == 0 {
		return 0, io.EOF
	}
	c.sent = true
	p[0] = 'q'
	return 1, nil
}

func (truncatedCopy) Close() error { return nil }

type errReader func([]byte) (int, error)

func (r errReader) Read(p []byte) (int, error) { return r(p) }

func (errReader) Close() error { return nil }

func copyReq(images, source string) ImportRequest {
	return ImportRequest{Mode: ImportCopy, ImagesDir: images, Name: "vendor/app", Source: source}
}

func stageImportOK(t *testing.T, req ImportRequest, seam stageCopyHooks) StagedCopy {
	t.Helper()
	got, err := stageImportFile(req, seam)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func mustStageRepo(t *testing.T, images string) Repo {
	t.Helper()
	repo, err := OpenRepo(images, "vendor/app")
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func assertUnpublished(t *testing.T, repo Repo) {
	t.Helper()
	for _, name := range []string{fileMeta, dirObjects} {
		_, err := os.Stat(filepath.Join(repo.Dir, name))
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s visible: %v", name, err)
		}
	}
}
