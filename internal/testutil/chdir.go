package testutil

import (
	"os"
	"testing"
)

// Chdir changes the process working directory until test cleanup. Like
// testing.T.Chdir on newer Go versions, it must not be used in parallel
// tests or tests with a parallel ancestor. Setenv enforces that restriction.
func Chdir(t *testing.T, dir string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Check the parallel-test restriction before changing process state.
	t.Setenv("PWD", dir)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
}
