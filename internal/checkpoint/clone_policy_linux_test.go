//go:build linux

package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestTreeCopyCloneCapabilityIsScopedAndConservative(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failure   syscall.Errno
		wantCalls int
	}{
		{"system unsupported", syscall.ENOSYS, 1},
		{"filesystem unsupported", syscall.EOPNOTSUPP, 2},
		{"file specific error", syscall.EINVAL, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := cloneTreeFile
			t.Cleanup(func() { cloneTreeFile = old })
			calls := 0
			cloneTreeFile = func(_, _ *os.File) error {
				calls++
				return fmt.Errorf("%w: %w", errCloneUnsupported, tc.failure)
			}
			root := t.TempDir()
			src := filepath.Join(root, "src")
			if err := os.Mkdir(src, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"one", "two", "three"} {
				if err := os.WriteFile(filepath.Join(src, name), []byte(name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for run := 1; run <= 2; run++ {
				dst := filepath.Join(root, fmt.Sprintf("dst-%d", run))
				if err := os.Mkdir(dst, 0700); err != nil {
					t.Fatal(err)
				}
				sf, err := os.Open(src)
				if err != nil {
					t.Fatal(err)
				}
				df, err := os.Open(dst)
				if err != nil {
					t.Fatal(err)
				}
				err = CopyTree(sf, df)
				_ = sf.Close()
				_ = df.Close()
				if err != nil {
					t.Fatal(err)
				}
				if calls != run*tc.wantCalls {
					t.Fatalf("calls=%d want=%d", calls, run*tc.wantCalls)
				}
				for _, name := range []string{"one", "two", "three"} {
					got, err := os.ReadFile(filepath.Join(dst, name))
					if err != nil || string(got) != name {
						t.Fatalf("%s=%q err=%v", name, got, err)
					}
				}
			}
		})
	}
}

func TestTreeCopyKeepsTryingAfterFileSpecificCloneFailure(t *testing.T) {
	old := cloneTreeFile
	t.Cleanup(func() { cloneTreeFile = old })
	calls := 0
	cloneTreeFile = func(_, _ *os.File) error {
		calls++
		if calls == 2 {
			return nil
		} // The probe succeeds on ordinary files.
		return fmt.Errorf("%w: %w", errCloneUnsupported, syscall.EOPNOTSUPP)
	}
	root := t.TempDir()
	for _, dir := range []string{"src", "dst"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(root, "src", name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	src, err := os.Open(filepath.Join(root, "src"))
	if err != nil {
		t.Fatal(err)
	}
	dst, err := os.Open(filepath.Join(root, "dst"))
	if err != nil {
		t.Fatal(err)
	}
	err = CopyTree(src, dst)
	_ = src.Close()
	_ = dst.Close()
	if err != nil {
		t.Fatal(err)
	}
	// Each failed real file has its own confirming probe.
	if calls != 4 {
		t.Fatalf("clone calls=%d, want 4", calls)
	}
	for _, name := range []string{"one", "two"} {
		got, err := os.ReadFile(filepath.Join(root, "dst", name))
		if err != nil || string(got) != name {
			t.Fatalf("%s=%q err=%v", name, got, err)
		}
	}
}
