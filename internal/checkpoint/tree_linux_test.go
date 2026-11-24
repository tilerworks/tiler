//go:build linux

package checkpoint

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

func TestCopyTreePreservesLinksAndMetadata(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	dest := filepath.Join(root, "dest")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("data"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(source, "file"), filepath.Join(source, "hard")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Setxattr(filepath.Join(source, "file"), "user.tiler-checkpoint-test", []byte("value"), 0); err != nil {
		t.Skipf("xattr unavailable: %v", err)
	}
	src, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	dst, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	if err := CopyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(dest, "link"))
	if err != nil || target != "missing" {
		t.Fatalf("link = %q, %v", target, err)
	}
	a, err := os.Stat(filepath.Join(dest, "file"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(filepath.Join(dest, "hard"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(a, b) || a.Mode().Perm() != 0640 {
		t.Fatalf("hard link or mode not preserved: %v %v", a, b)
	}
	value := make([]byte, 100)
	n, err := syscall.Getxattr(filepath.Join(dest, "file"), "user.tiler-checkpoint-test", value)
	if err != nil || string(value[:n]) != "value" {
		t.Fatalf("xattr %q: %v", value[:n], err)
	}
}

func TestCopyTreeRejectsFIFO(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	dest := filepath.Join(root, "dest")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(source, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	dst, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	if err := CopyTree(src, dst); err == nil {
		t.Fatal("FIFO was accepted")
	}
}

func TestVerifyLinkPathRejectsReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink("one", path); err != nil {
		t.Fatal(err)
	}
	var before syscall.Stat_t
	if err := syscall.Lstat(path, &before); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("two", path); err != nil {
		t.Fatal(err)
	}
	if err := verifyLinkPath(path, &before); err == nil {
		t.Fatal("replacement accepted")
	}
}

func TestCheckTreeMountsRejectsRoot(t *testing.T) {
	if err := checkTreeMounts("/"); err == nil {
		t.Fatal("mount tree accepted")
	}
}

func TestCheckTreeMountPointsRejectsSameDeviceBindMount(t *testing.T) {
	mounts := []mountinfo.Mount{{Point: "/data/tree/nested", FSType: "ext4"}}
	if err := checkTreeMountPoints("/data/tree", mounts); err == nil {
		t.Fatal("nested bind mount accepted")
	}
	if err := checkTreeMountPoints("/data/tree-other", mounts); err != nil {
		t.Fatal(err)
	}
}

func TestCopyMetadataReportsDestinationFailure(t *testing.T) {
	root := t.TempDir()
	src, err := os.Create(filepath.Join(root, "src"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	dst, err := os.Create(filepath.Join(root, "dst"))
	if err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	if err := copyMetadata(src, dst); err == nil {
		t.Fatal("closed destination accepted")
	}
}

func TestCopyLinkXattrsReportsMissingSource(t *testing.T) {
	root := t.TempDir()
	if err := copyLinkXattrs(filepath.Join(root, "missing"), filepath.Join(root, "missing-dst")); err == nil {
		t.Fatal("missing source accepted")
	}
}

func TestCopyTreePreservesACL(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	dest := filepath.Join(root, "dest")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dest, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(source, "file")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	acl := make([]byte, 44)
	binary.LittleEndian.PutUint32(acl, 2)
	tags := []uint16{1, 2, 4, 16, 32}
	perms := []uint16{6, 4, 0, 4, 0}
	for i := range tags {
		off := 4 + i*8
		binary.LittleEndian.PutUint16(acl[off:], tags[i])
		binary.LittleEndian.PutUint16(acl[off+2:], perms[i])
		id := uint32(0xffffffff)
		if i == 1 {
			id = uint32(os.Getuid()) + 1
		}
		binary.LittleEndian.PutUint32(acl[off+4:], id)
	}
	if err := syscall.Setxattr(file, "system.posix_acl_access", acl, 0); err != nil {
		t.Skipf("ACL unavailable: %v", err)
	}
	src, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	dst, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	if err := CopyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 100)
	n, err := syscall.Getxattr(filepath.Join(dest, "file"), "system.posix_acl_access", got)
	if err != nil {
		t.Fatal(err)
	}
	if string(got[:n]) != string(acl) {
		t.Fatalf("ACL changed: %x", got[:n])
	}
}
