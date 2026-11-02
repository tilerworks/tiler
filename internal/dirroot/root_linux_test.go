//go:build linux

package dirroot

import (
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
)

func TestDupIsNotInheritedAcrossExec(t *testing.T) {
	root, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	fd, err := root.dup()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(fd) }()
	if fd <= 2 {
		t.Fatalf("fd = %d, want a descriptor above stderr", fd)
	}
	cmd := exec.Command("sh", "-c", fmt.Sprintf("test ! -e /proc/self/fd/%s", strconv.Itoa(fd)))
	if err := cmd.Run(); err != nil {
		t.Fatalf("child inherited fd %d: %v", fd, err)
	}
}
