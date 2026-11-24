//go:build darwin

package checkpoint

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDarwinCheckpointPreservesMetadata(t *testing.T) {
	_, target := copyFixture(t)
	file := filepath.Join(target, "file")
	if err := os.Link(file, filepath.Join(target, "hardlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(target, "symlink")); err != nil {
		t.Fatal(err)
	}
	fileXattr := setDarwinTestXattr(t, file, false)
	linkXattr := setDarwinTestXattr(t, filepath.Join(target, "symlink"), true)
	aclSupported := true
	if out, err := exec.Command("/bin/chmod", "+a", "everyone allow readattr", target).CombinedOutput(); err != nil {
		if strings.Contains(strings.ToLower(string(out)), "operation not supported") {
			aclSupported = false
		} else {
			t.Fatalf("set source ACL: %s: %v", out, err)
		}
	}
	var wantACL []byte
	if aclSupported {
		src, err := os.Open(target)
		if err != nil {
			t.Fatal(err)
		}
		defer closeMaybe(src)
		wantACL, err = darwinACL(src)
		if err != nil || len(wantACL) == 0 {
			t.Fatalf("read source ACL (%d bytes): %v", len(wantACL), err)
		}
	}
	stamp := time.Unix(1700000000, 123456789)
	if err := os.Chtimes(file, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	wantInfo, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	oldClone := darwinClone
	t.Cleanup(func() { darwinClone = oldClone })
	cloned := false
	probeUnsupported := false
	var cloneErrors []error
	darwinClone = func(srcFD, dstFD int, name string, flags int) error {
		err := oldClone(srcFD, dstFD, name, flags)
		if strings.HasPrefix(name, ".tiler-checkpoint-clone-probe-") {
			probeUnsupported = isCloneNotSupported(err) || errors.Is(err, unix.ENOSYS)
		} else {
			cloned = cloned || err == nil
			cloneErrors = append(cloneErrors, err)
		}
		return err
	}
	if err := copyFixtureTree(target); err != nil {
		t.Fatal(err)
	}
	t.Run("native clone", func(t *testing.T) {
		if len(cloneErrors) == 0 {
			t.Fatal("copy did not attempt native clone")
		}
		for _, err := range cloneErrors {
			if err != nil && !isCloneNotSupported(err) && !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.ENOTTY) {
				t.Fatalf("unexpected native clone error: %v", err)
			}
		}
		if !cloned {
			if !errors.Is(cloneErrors[0], unix.ENOSYS) && !probeUnsupported {
				t.Fatal("clone unsupported was not confirmed by the probe")
			}
			t.Skip("native clone unsupported on fixture filesystem; supported filesystem evidence required")
		}
	})
	gotInfo, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	linkInfo, err := os.Stat(filepath.Join(target, "hardlink"))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(wantInfo, gotInfo) || !os.SameFile(gotInfo, linkInfo) || !gotInfo.ModTime().Equal(wantInfo.ModTime()) || gotInfo.Mode() != wantInfo.Mode() {
		t.Errorf("file identity, hard link, mtime or mode changed: %v %v %v", wantInfo, gotInfo, linkInfo)
	}
	t.Run("ACL", func(t *testing.T) {
		if !aclSupported {
			t.Skip("ACL unsupported on fixture filesystem; supported filesystem evidence required")
		}
		active, err := os.Open(target)
		if err != nil {
			t.Fatal(err)
		}
		defer closeMaybe(active)
		gotACL, err := darwinACL(active)
		if err != nil || !bytes.Equal(gotACL, wantACL) {
			t.Errorf("copied ACL = %x, want %x: %v", gotACL, wantACL, err)
		}
	})
	t.Run("file xattr", func(t *testing.T) {
		if !fileXattr {
			t.Skip("file xattrs unsupported on fixture filesystem; supported filesystem evidence required")
		}
		attr, err := darwinAttrBytes(func(buf []byte) (int, error) { return unix.Getxattr(file, "org.tiler.test", buf) })
		if err != nil || string(attr) != "metadata" {
			t.Errorf("copied xattr = %q: %v", attr, err)
		}
	})
	link := filepath.Join(target, "symlink")
	linkTarget, err := os.Readlink(link)
	if err != nil || linkTarget != "missing" {
		t.Errorf("copied symlink = %q: %v", linkTarget, err)
	}
	t.Run("symlink xattr", func(t *testing.T) {
		if !linkXattr {
			t.Skip("symlink xattrs unsupported on fixture filesystem; supported filesystem evidence required")
		}
		attr, err := darwinAttrBytes(func(buf []byte) (int, error) { return unix.Lgetxattr(link, "org.tiler.test", buf) })
		if err != nil || string(attr) != "link metadata" {
			t.Errorf("copied symlink xattr = %q: %v", attr, err)
		}
	})
}

func TestDarwinCloneFallbackAndErrors(t *testing.T) {
	for _, cloneErr := range []error{unix.ENOTSUP, unix.EACCES, unix.EIO} {
		t.Run(cloneErr.Error(), func(t *testing.T) {
			_, target := copyFixture(t)
			oldClone := darwinClone
			t.Cleanup(func() { darwinClone = oldClone })
			darwinClone = func(int, int, string, int) error { return cloneErr }
			err := copyFixtureTree(target)
			if cloneErr == unix.ENOTSUP {
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(target, "file"))
				if err != nil || string(data) != "original" {
					t.Fatalf("fallback copy = %q: %v", data, err)
				}
				return
			}
			if !errors.Is(err, cloneErr) {
				t.Fatalf("create: %v, want %v", err, cloneErr)
			}
			if _, err := os.Lstat(target + ".checkpoint"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed copy published checkpoint: %v", err)
			}
		})
	}
}

func TestDarwinCheckpointRefusesUncopyableExtendedAttributes(t *testing.T) {
	_, target := copyFixture(t)
	file := filepath.Join(target, "file")
	if !setDarwinTestXattr(t, file, false) {
		t.Skip("file xattrs unsupported on fixture filesystem; supported filesystem evidence required")
	}
	oldClone, oldSetXattr := darwinClone, darwinSetXattr
	t.Cleanup(func() { darwinClone, darwinSetXattr = oldClone, oldSetXattr })
	darwinClone = func(int, int, string, int) error { return unix.ENOTSUP }
	darwinSetXattr = func(int, string, []byte, int) error { return unix.ENOTSUP }
	if err := copyFixtureTree(target); !errors.Is(err, unix.ENOTSUP) {
		t.Fatalf("create: %v", err)
	}
	if _, err := os.Lstat(target + ".checkpoint"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published checkpoint after metadata error: %v", err)
	}
}

func TestDarwinTreeCloneCapabilityIsScoped(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failure   error
		wantCalls int
	}{
		{"system unavailable", unix.ENOSYS, 1},
		{"filesystem unsupported", unix.ENOTSUP, 2},
		{"filesystem operation unsupported", unix.EOPNOTSUPP, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := darwinClone
			t.Cleanup(func() { darwinClone = old })
			calls := 0
			darwinClone = func(int, int, string, int) error { calls++; return tc.failure }
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

func copyFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "file"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	return root, target
}

func copyFixtureTree(target string) error {
	srcPath := target + ".source"
	if err := os.Rename(target, srcPath); err != nil {
		return err
	}
	if err := os.Mkdir(target, 0700); err != nil {
		return err
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer closeMaybe(src)
	dst, err := os.Open(target)
	if err != nil {
		return err
	}
	defer closeMaybe(dst)
	return CopyTree(src, dst)
}

func setDarwinTestXattr(t *testing.T, path string, symlink bool) bool {
	t.Helper()
	value := []byte("metadata")
	var err error
	if symlink {
		value = []byte("link metadata")
		err = unix.Lsetxattr(path, "org.tiler.test", value, 0)
	} else {
		err = unix.Setxattr(path, "org.tiler.test", value, 0)
	}
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENOSYS) {
		return false
	}
	if err != nil {
		t.Fatalf("set fixture xattr: %v", err)
	}
	return true
}
