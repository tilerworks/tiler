package benchmark

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWritePayloadHonorsSizeAndTruncatesLastFile(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "data")
	const total = int64(10)
	const fileSize = int64(4)
	n, err := WritePayload(dest, total, fileSize)
	if err != nil {
		t.Fatalf("WritePayload: %v", err)
	}
	if n != 3 {
		t.Fatalf("files = %d, want 3", n)
	}
	want := []int64{4, 4, 2}
	var sum int64
	var saw bool
	for i, size := range want {
		got, err := os.ReadFile(filepath.Join(dest, fmt.Sprintf("f%05d", i)))
		if err != nil {
			t.Fatalf("read file %d: %v", i, err)
		}
		if int64(len(got)) != size {
			t.Fatalf("f%05d len = %d, want %d", i, len(got), size)
		}
		sum += int64(len(got))
		if nonZero(got) {
			saw = true
		}
	}
	if sum != total {
		t.Fatalf("total = %d, want %d", sum, total)
	}
	if !saw {
		t.Fatal("payload is all zeros")
	}
}

func TestWritePayloadTruncatesWhenFileSizeExceedsSize(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "data")
	n, err := WritePayload(dest, 3, 10)
	if err != nil {
		t.Fatalf("WritePayload: %v", err)
	}
	if n != 1 {
		t.Fatalf("files = %d, want 1", n)
	}
	got, err := os.ReadFile(filepath.Join(dest, "f00000"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if !nonZero(got) {
		t.Fatal("payload is all zeros")
	}
}

func TestWritePayloadSpansChunkBoundary(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "data")
	total := int64(chunkSize) + 1
	n, err := WritePayload(dest, total, chunkSize)
	if err != nil {
		t.Fatalf("WritePayload: %v", err)
	}
	if n != 2 {
		t.Fatalf("files = %d, want 2", n)
	}
	last, err := os.ReadFile(filepath.Join(dest, "f00001"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(last) != 1 {
		t.Fatalf("last len = %d, want 1", len(last))
	}
}

func TestWritePayloadRefusesAllZero(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "data")
	prev := payloadEntropy
	payloadEntropy = zeroReader{}
	t.Cleanup(func() { payloadEntropy = prev })
	n, err := WritePayload(dest, 32, 16)
	if n != 0 {
		t.Fatalf("files = %d, want 0", n)
	}
	if err == nil || !strings.Contains(err.Error(), "all zeros") {
		t.Fatalf("err = %v", err)
	}
	entries, readErr := os.ReadDir(dest)
	if readErr != nil {
		t.Fatalf("readdir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("left %d files", len(entries))
	}
}

func TestWritePayloadRejectsNonPositiveSizes(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "data")
	if _, err := WritePayload(dest, 0, 4); err == nil {
		t.Fatal("size 0 was accepted")
	}
	if _, err := WritePayload(dest, 4, 0); err == nil {
		t.Fatal("file size 0 was accepted")
	}
}

func TestWritePayloadRemovesFilesOnWriteError(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "data")
	prev := payloadEntropy
	payloadEntropy = &failAfterReader{ok: 8}
	t.Cleanup(func() { payloadEntropy = prev })
	n, err := WritePayload(dest, 16, 8)
	if n != 0 {
		t.Fatalf("files = %d, want 0", n)
	}
	if err == nil {
		t.Fatal("expected write error")
	}
	entries, readErr := os.ReadDir(dest)
	if readErr != nil {
		t.Fatalf("readdir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("left %d files", len(entries))
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// failAfterReader fills ok bytes with non-zero data, then fails.
type failAfterReader struct {
	ok int
}

func (r *failAfterReader) Read(p []byte) (int, error) {
	if r.ok <= 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n := len(p)
	if n > r.ok {
		n = r.ok
	}
	for i := 0; i < n; i++ {
		p[i] = 1
	}
	r.ok -= n
	return n, nil
}
