package benchmark

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadTreeSkipsDotFileNames(t *testing.T) {
	root := t.TempDir()
	writeBytes(t, filepath.Join(root, "a.bin"), bytesOf(100, 1))
	writeBytes(t, filepath.Join(root, ".hidden"), bytesOf(50, 2))
	writeBytes(t, filepath.Join(root, "sub", "b.bin"), bytesOf(chunkSize+10, 3))
	writeBytes(t, filepath.Join(root, "sub", ".secret"), bytesOf(7, 4))
	// A dot directory is still walked. Only the file name is skipped.
	writeBytes(t, filepath.Join(root, ".keep", "c.bin"), []byte("xyz"))

	got, elapsed, err := ReadTree(root)
	if err != nil {
		t.Fatalf("ReadTree: %v", err)
	}
	want := int64(100 + chunkSize + 10 + 3)
	if got != want {
		t.Fatalf("bytes = %d, want %d", got, want)
	}
	if elapsed < 0 {
		t.Fatalf("elapsed = %s", elapsed)
	}
}

func TestReadTreeMissingRoot(t *testing.T) {
	_, _, err := ReadTree(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("missing root was accepted")
	}
}

func writeBytes(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func bytesOf(n int, fill byte) []byte {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = fill
	}
	return buf
}
