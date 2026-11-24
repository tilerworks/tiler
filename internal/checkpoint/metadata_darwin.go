//go:build darwin

package checkpoint

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func copyDarwinMetadata(src, dst *os.File, source unix.Stat_t) error {
	var target unix.Stat_t
	if err := unix.Fstat(int(dst.Fd()), &target); err != nil {
		return err
	}
	if source.Uid != target.Uid || source.Gid != target.Gid {
		if err := dst.Chown(int(source.Uid), int(source.Gid)); err != nil {
			return err
		}
	}
	if source.Mode&unix.S_IFMT != unix.S_IFLNK || source.Mode&07777 != target.Mode&07777 {
		if err := unix.Fchmod(int(dst.Fd()), uint32(source.Mode&07777)); err != nil {
			return err
		}
	}
	if err := copyDarwinXattrs(int(src.Fd()), int(dst.Fd())); err != nil {
		return err
	}
	return copyDarwinACL(src, dst)
}

func darwinAttrBytes(read func([]byte) (int, error)) ([]byte, error) {
	n, err := read(nil)
	if err != nil || n == 0 {
		return nil, err
	}
	value := make([]byte, n)
	n, err = read(value)
	if err != nil {
		return nil, err
	}
	if n > len(value) {
		return nil, errors.New("extended attribute changed size")
	}
	return value[:n], nil
}

func darwinXattrNames(fd int) ([][]byte, error) {
	data, err := darwinAttrBytes(func(buf []byte) (int, error) { return unix.Flistxattr(fd, buf) })
	if errors.Is(err, unix.ENOTSUP) {
		return nil, nil
	}
	if err != nil || len(data) == 0 {
		return nil, err
	}
	if data[len(data)-1] != 0 {
		return nil, errors.New("invalid extended attribute list")
	}
	return bytes.Split(data[:len(data)-1], []byte{0}), nil
}

var darwinSetXattr = unix.Fsetxattr

func copyDarwinXattrs(src, dst int) error {
	names, err := darwinXattrNames(src)
	if err != nil {
		return err
	}
	existing, err := darwinXattrNames(dst)
	if err != nil {
		return err
	}
	sourceNames := make(map[string]bool, len(names))
	for _, name := range names {
		key := string(name)
		sourceNames[key] = true
		value, err := darwinAttrBytes(func(buf []byte) (int, error) { return unix.Fgetxattr(src, key, buf) })
		if err != nil {
			return fmt.Errorf("read extended attribute %q: %w", key, err)
		}
		current, err := darwinAttrBytes(func(buf []byte) (int, error) { return unix.Fgetxattr(dst, key, buf) })
		if err == nil && bytes.Equal(current, value) {
			continue
		}
		if err != nil && !errors.Is(err, unix.ENOATTR) && !errors.Is(err, unix.ENOTSUP) {
			return fmt.Errorf("read destination extended attribute %q: %w", key, err)
		}
		if err := darwinSetXattr(dst, key, value, 0); err != nil {
			return fmt.Errorf("write extended attribute %q: %w", key, err)
		}
	}
	for _, name := range existing {
		if key := string(name); !sourceNames[key] {
			if err := unix.Fremovexattr(dst, key); err != nil {
				return fmt.Errorf("remove inherited extended attribute %q: %w", key, err)
			}
		}
	}
	return nil
}

// ATTR_CMN_EXTENDED_SECURITY uses a four-byte result length followed by
// attrreference_t. The reference offset is relative to the reference itself.
func darwinACL(f *os.File) ([]byte, error) {
	attrs := unix.Attrlist{Bitmapcount: unix.ATTR_BIT_MAP_COUNT, Commonattr: unix.ATTR_CMN_EXTENDED_SECURITY}
	var buf [32768]byte
	// x/sys does not expose the descriptor-based libSystem function.
	//nolint:staticcheck // A path-based ACL read would lose the pinned inode.
	_, _, err := syscall.Syscall6(unix.SYS_FGETATTRLIST, f.Fd(), uintptr(unsafe.Pointer(&attrs)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
	if err == unix.ENOTSUP {
		return nil, nil
	}
	if err != 0 {
		return nil, err
	}
	total := int(binary.NativeEndian.Uint32(buf[:4]))
	start := 4 + int(int32(binary.NativeEndian.Uint32(buf[4:8])))
	size := int(binary.NativeEndian.Uint32(buf[8:12]))
	if total < 12 || total > len(buf) {
		return nil, errors.New("invalid extended security attribute length")
	}
	if size == 0 {
		return nil, nil
	}
	if start < 12 || start > total || size > total-start {
		return nil, errors.New("invalid extended security attribute reference")
	}
	return buf[start : start+size], nil
}

func copyDarwinACL(src, dst *os.File) error {
	source, err := darwinACL(src)
	if err != nil {
		return fmt.Errorf("read source ACL: %w", err)
	}
	target, err := darwinACL(dst)
	if err != nil {
		return fmt.Errorf("read destination ACL: %w", err)
	}
	if bytes.Equal(source, target) {
		return nil
	}
	expected := source
	if len(source) == 0 {
		// A kauth_filesec with KAUTH_FILESEC_NOACL clears an inherited ACL.
		source = make([]byte, 44)
		binary.NativeEndian.PutUint32(source[:4], 0x012cc16d)
		binary.NativeEndian.PutUint32(source[36:40], ^uint32(0))
	}
	attrs := unix.Attrlist{Bitmapcount: unix.ATTR_BIT_MAP_COUNT, Commonattr: unix.ATTR_CMN_EXTENDED_SECURITY}
	buf := make([]byte, 8+len(source))
	binary.NativeEndian.PutUint32(buf[:4], 8)
	binary.NativeEndian.PutUint32(buf[4:8], uint32(len(source)))
	copy(buf[8:], source)
	//nolint:staticcheck // A path-based ACL write could modify a replacement inode.
	_, _, errno := syscall.Syscall6(unix.SYS_FSETATTRLIST, dst.Fd(), uintptr(unsafe.Pointer(&attrs)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
	if errno != 0 {
		return fmt.Errorf("write destination ACL: %w", errno)
	}
	actual, err := darwinACL(dst)
	if err != nil {
		return fmt.Errorf("verify destination ACL: %w", err)
	}
	if !bytes.Equal(actual, expected) {
		return errors.New("destination ACL differs after copy")
	}
	return nil
}
