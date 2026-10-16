package cli

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// updateGolden regenerates the golden files under testdata when a
// JSON contract changes on purpose: go test ./internal/cli -update.
var updateGolden = flag.Bool("update", false, "update golden files under testdata")

// goldenDir is the package source directory, captured before tests
// change the working directory into a temporary workspace.
var goldenDir = func() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return dir
}()

// assertGolden compares got byte-for-byte with testdata/<name>.golden.
// Run the tests with -update to rewrite the golden file after an
// intended contract change.
func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	dir := filepath.Join(goldenDir, "testdata")
	path := filepath.Join(dir, name+".golden")
	if *updateGolden {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (run go test -update to create): %v", path, err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("%s mismatch\ngot:\n%s\nwant:\n%s", path, got, want)
	}
}
