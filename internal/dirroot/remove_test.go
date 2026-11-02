//go:build linux || darwin

package dirroot

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRemoveTreeContinuesAfterListedChildDisappears(t *testing.T) {
	for _, directory := range []bool{false, true} {
		name := "file"
		if directory {
			name = "directory"
		}
		t.Run(name, func(t *testing.T) {
			dir, outside := t.TempDir(), t.TempDir()
			vanished := filepath.Join(dir, "vanished")
			if directory {
				if err := os.Mkdir(vanished, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(vanished, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(dir, "remaining", "nested"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "remaining", "nested", "note"), []byte("inside"), 0o600); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(outside, "note")
			if err := os.WriteFile(marker, []byte("outside"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
				t.Fatal(err)
			}
			root, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			fd, err := root.dup()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = syscall.Close(fd) }()
			names, err := namesFromFD(fd)
			if err != nil {
				t.Fatal(err)
			}
			// Fix the interleaving without timers or filesystem ordering:
			// enumerate, remove the first listed child, then resume deletion.
			for i, child := range names {
				if child == "vanished" {
					names[0], names[i] = names[i], names[0]
					break
				}
			}
			if len(names) != 3 || names[0] != "vanished" {
				t.Fatalf("listed children = %v", names)
			}
			if err := os.Remove(vanished); err != nil {
				t.Fatal(err)
			}
			if err := removeTreeChildren(fd, names); err != nil {
				t.Fatalf("remove listed children: %v", err)
			}
			remaining, err := os.ReadDir(dir)
			if err != nil || len(remaining) != 0 {
				t.Fatalf("remaining children = %v, %v", remaining, err)
			}
			got, err := os.ReadFile(marker)
			if err != nil || string(got) != "outside" {
				t.Fatalf("outside marker = %q, %v", got, err)
			}
		})
	}
}

func TestRemoveTreeChildrenPreservesOtherErrors(t *testing.T) {
	if err := removeTreeChildren(-1, []string{"child"}); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("invalid directory descriptor = %v, want EBADF", err)
	}
}
