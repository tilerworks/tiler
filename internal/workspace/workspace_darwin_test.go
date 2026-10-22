//go:build darwin

package workspace

import (
	"testing"

	"github.com/jiangxin/tiler/internal/container"
)

func TestDarwinMountIsAvailable(t *testing.T) {
	if err := available(); err != nil {
		t.Fatal(err)
	}
}

func TestDarwinWorkspaceUsesSystemHdiutil(t *testing.T) {
	argv, err := container.HdiutilDetachArgv("/mnt")
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := newHdiutil(argv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != container.HdiutilPath {
		t.Fatalf("path = %q", cmd.Path)
	}
	if cmd.Process != nil {
		t.Fatal("command was started")
	}
}
